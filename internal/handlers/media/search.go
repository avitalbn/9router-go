package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

const nativeSearchTimeout = 10 * time.Second

type searchType string

const (
	searchWeb  searchType = "web"
	searchNews searchType = "news"
)

type searchBody struct {
	Query           string         `json:"query"`
	Prompt          string         `json:"prompt"`
	MaxResults      int            `json:"max_results"`
	SearchType      searchType     `json:"search_type"`
	Language        string         `json:"language"`
	Country         string         `json:"country"`
	TimeRange       string         `json:"time_range"`
	Offset          int            `json:"offset"`
	DomainFilter    []string       `json:"domain_filter"`
	ProviderOptions map[string]any `json:"provider_options"`
}

func isNativeSearchProvider(provider string) bool {
	return provider == "tavily" || provider == "exa" || provider == "searxng"
}

// Keep database aliases, combos and custom prefixes ahead of bare provider IDs.
func (h *MediaHandler) normalizeSearchModel(model string) string {
	provider := providers.ResolveAlias(model)
	if !isNativeSearchProvider(provider) || strings.Contains(model, "/") {
		return model
	}
	if alias, err := h.Repo.GetModelAlias(model); err != nil || alias != "" {
		return model
	}
	if combo, err := h.Repo.GetComboByName(model); err != nil || combo != nil {
		return model
	}
	if node, _, err := h.Repo.GetProviderNodeByPrefix(model); err != nil || node != nil {
		return model
	}
	return provider + "/search"
}

func parseSearchBody(body []byte) (*searchBody, error) {
	var req searchBody
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, searchError(http.StatusBadRequest, "invalid JSON in search request")
	}
	if req.Query == "" {
		req.Query = req.Prompt
	}
	req.Query = sanitizeSearchQuery(req.Query)
	if req.Query == "" {
		return nil, searchError(http.StatusBadRequest, "missing or invalid query")
	}
	if req.MaxResults <= 0 {
		req.MaxResults = defaultSearchMaxResults
	}
	if req.SearchType != searchNews {
		req.SearchType = searchWeb
	}
	return &req, nil
}

func nativeSearchLimits(provider string) (int, float64) {
	switch provider {
	case "tavily":
		return 20, 0.008
	case "exa":
		return 100, 0.007
	default:
		return 50, 0
	}
}

func (h *MediaHandler) handleNativeSearch(w http.ResponseWriter, r *http.Request, body []byte, info *chat.ModelInfo) error {
	start := time.Now()
	input, err := parseSearchBody(body)
	if err != nil {
		return err
	}
	limit, cost := nativeSearchLimits(info.Provider)
	input.MaxResults = min(input.MaxResults, limit)
	ctx, cancel := context.WithTimeout(r.Context(), nativeSearchTimeout)
	defer cancel()
	preferred := r.Header.Get("x-connection-id")
	if preferred == "" {
		preferred = r.Header.Get("x-provider-connection-id")
	}
	if preferred == "" {
		preferred = info.ConnectionID
	}
	conn, data, cfg, err := h.nativeSearchConnection(ctx, info.Provider, info.Model, preferred)
	if err != nil {
		return err
	}
	target, err := h.nativeSearchURL(ctx, cfg, data, input)
	if err != nil {
		return err
	}
	req, err := buildNativeSearchRequest(ctx, info.Provider, target, input)
	if err != nil {
		return err
	}
	for k, v := range cfg.StaticHeaders {
		req.Header.Set(k, v)
	}
	if !cfg.NoAuth {
		handlerutil.SetAuthHeader(req, chat.ExtractAPIKey(data), cfg.AuthHeader, cfg.AuthScheme)
	}
	client, err := h.ChatH.GetClientForConnection(data)
	if err != nil {
		return searchError(http.StatusBadGateway, fmt.Sprintf("search client: %v", err))
	}
	upstreamStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nativeSearchFailure(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return searchError(resp.StatusCode, fmt.Sprintf("%s returned %d: %s", info.Provider, resp.StatusCode, strings.TrimSpace(string(message))))
	}
	var vendor struct {
		Results []nativeSearchItem `json:"results"`
	}
	if err := json.UnmarshalRead(resp.Body, &vendor); err != nil {
		return nativeSearchFailure(ctx, fmt.Errorf("decode %s search: %w", info.Provider, err))
	}
	latency := time.Since(upstreamStart).Milliseconds()
	results := normalizeNativeSearchResults(info.Provider, vendor.Results[:min(input.MaxResults, len(vendor.Results))])
	handlerutil.WriteJSON(w, http.StatusOK, nativeSearchResponse{
		SearchResponse: SearchResponse{
			Provider: info.Provider, Query: input.Query,
			Usage:   SearchUsage{QueriesUsed: 1, SearchCostUSD: cost},
			Metrics: SearchMetrics{ResponseTimeMS: time.Since(start).Milliseconds(), UpstreamLatencyMS: latency, TotalResultsAvailable: len(vendor.Results)},
			Errors:  []any{},
		},
		Results: results,
	})
	h.logNativeSearchUsage(r.Context(), conn, data, info, body, latency)
	return nil
}

func nativeSearchFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return searchError(499, "client closed search request")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return searchError(http.StatusGatewayTimeout, "search request timed out")
	}
	return searchError(http.StatusBadGateway, fmt.Sprintf("search upstream: %v", err))
}

func (h *MediaHandler) nativeSearchConnection(ctx context.Context, provider, model, preferred string) (*models.ProviderConnection, *chat.ConnectionData, *providers.ProviderConfig, error) {
	conn, data, err := h.ChatH.GetBestConnectionWithContext(ctx, provider, preferred, nil, model)
	if ctx.Err() != nil {
		return nil, nil, nil, nativeSearchFailure(ctx, ctx.Err())
	}
	if err != nil {
		return nil, nil, nil, searchError(http.StatusNotFound, mediaConnectionError(err, provider))
	}
	cfg, err := h.ChatH.GetProviderConfig(provider, data)
	if err != nil {
		return nil, nil, nil, searchError(http.StatusBadGateway, fmt.Sprintf("search provider config: %v", err))
	}
	if !cfg.NoAuth && chat.ExtractAPIKey(data) == "" {
		return nil, nil, nil, searchError(http.StatusUnauthorized, "no credentials for provider: "+provider)
	}
	return conn, data, cfg, nil
}

func (h *MediaHandler) nativeSearchURL(ctx context.Context, cfg *providers.ProviderConfig, data *chat.ConnectionData, input *searchBody) (string, error) {
	target := cfg.SearchURL
	if data.BaseURL != "" {
		target = data.BaseURL
	}
	if value, ok := data.ProviderSpecificData["baseUrl"].(string); ok && strings.TrimSpace(value) != "" {
		target = strings.TrimSpace(value)
	}
	if value, ok := input.ProviderOptions["baseUrl"].(string); ok && strings.TrimSpace(value) != "" {
		target = strings.TrimSpace(value)
	}
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", searchError(http.StatusBadRequest, "invalid search baseUrl")
	}
	if targetPrivate(ctx, u.Hostname()) {
		return "", searchError(http.StatusForbidden, "private search address blocked")
	}
	return strings.TrimRight(target, "/"), nil
}

func buildNativeSearchRequest(ctx context.Context, provider, target string, input *searchBody) (*http.Request, error) {
	method, payload, target, err := nativeSearchPayload(provider, target, input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, searchError(http.StatusBadRequest, fmt.Sprintf("build search request: %v", err))
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

// Preserve the generic media path's account activity and request ledger.
func (h *MediaHandler) logNativeSearchUsage(ctx context.Context, conn *models.ProviderConnection, data *chat.ConnectionData, info *chat.ModelInfo, body []byte, latency int64) {
	if conn == nil {
		return
	}
	_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
	usage := &shared.UsageLogInfo{
		Provider: info.Provider, Model: info.Model, ConnectionID: conn.ID,
		APIKey: chat.ExtractAPIKey(data), Endpoint: "/v1/search",
	}
	h.ChatH.LogUsage(ctx, usage, nil, latency, body, nil)
}
