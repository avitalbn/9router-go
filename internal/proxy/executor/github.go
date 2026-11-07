package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

const copilotContextOverflowMsgPrefix = "Your input exceeds the context window of this model."
const ContextLengthExceededCode = "context_length_exceeded"

// githubResponsesModels is the runtime-learned set of GitHub Copilot model IDs
// that require the /responses endpoint. The first time a /chat/completions
// request for a model returns the "not accessible via the /chat/completions
// endpoint" or "The requested model is not supported" error, the model is
// added here and subsequent calls skip straight to /responses.
//
// Pre-populated at init with confirmed Responses-only models (live catalog
// evidence: supported_endpoints=['/responses','ws:/responses']). Upstream
// parity: executors/github.js knownCodexModels.
var githubResponsesModels sync.Map

func init() {
	// gpt-6.1-sol is confirmed Responses-only on GitHub Copilot: its live
	// catalog entry declares supported_endpoints=['/responses','ws:/responses']
	// and /chat/completions returns 400 for it.
	githubResponsesModels.Store("gpt-6.1-sol", struct{}{})
}

// githubResponsesBaseURL derives the /responses endpoint URL from the
// /chat/completions base URL the registry stores. Both canonical hosts
// (api.githubcopilot.com, api.business.githubcopilot.com) follow the same
// origin-relative path convention.
func githubResponsesBaseURL(chatURL string) string {
	base := strings.TrimRight(chatURL, "/")
	// Already a /responses URL – leave it intact (e.g. a custom connection
	// pointed directly at the responses endpoint).
	if strings.HasSuffix(strings.ToLower(base), "/responses") {
		return base
	}
	// Strip /chat/completions suffix (or any other trailing path) to get the
	// origin, then append /responses.
	if idx := strings.LastIndex(strings.ToLower(base), "/chat/completions"); idx > 0 {
		return base[:idx] + "/responses"
	}
	return base + "/responses"
}

// githubSupportsResponsesEndpoint reports whether model may be escalated to
// GitHub Copilot's /responses endpoint. Claude and Gemini models must never be
// sent there: Copilot's /responses gate is GPT/OpenAI-only and answers 400
// "does not support Responses API" for them instead of surfacing the real
// /chat/completions error (upstream parity: executors/github.js
// supportsResponsesEndpoint, #1062).
func githubSupportsResponsesEndpoint(model string) bool {
	m := strings.ToLower(model)
	return !strings.Contains(m, "claude") && !strings.Contains(m, "gemini")
}

// githubIsChatOnlyError reports whether a 400 body is the specific Copilot
// error signalling that the model can only be reached via the /responses
// endpoint. Matches the two error strings upstream checks before the
// /responses escalation (executors/github.js execute()).
func githubIsChatOnlyError(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "not accessible via the /chat/completions endpoint") ||
		strings.Contains(s, "the requested model is not supported")
}

// copilotMessagesPath is the Claude Messages endpoint on the Copilot inference host.
const copilotMessagesPath = "/v1/messages"

// ForwardGithub handles GitHub Copilot requests, normalizing Copilot context overflow
// errors (400 invalid_request_body with "Your input exceeds...") into standard
// context_length_exceeded error codes (upstream decolua/9router PR #5280 parity).
//
// Claude models (claude-3-7-sonnet, claude-3-5-sonnet, claude-opus-*, ...) are
// forwarded directly to Copilot's native Claude Messages endpoint at
// https://api.githubcopilot.com/v1/messages using the Claude Messages wire format,
// instead of being converted to /chat/completions (OpenAI wire format).
func ForwardGithub(w http.ResponseWriter, req *Request) error {
	model, ok := githubModel(req)
	_, known := githubResponsesModels.Load(copilotModelSlug(model))
	if ok && (known || IsCopilotResponsesModelSlug(model)) {
		return forwardGithubResponses(w, req)
	}
	if ok && isClaudeModel(model) {
		return ForwardGithubClaudeMessages(w, req, model)
	}
	return forwardGithubOpenAI(w, req)
}

// IsCopilotResponsesModel reports whether a GitHub Copilot model id is served by
// the Responses-native upstream rather than the Chat Completions bridge:
// GPT-6.x and GPT-5.5+ (bare gpt-6 / gpt-5 / gpt-5.4 excluded).
func IsCopilotResponsesModel(provider, model string) bool {
	if provider != "github" && provider != "ghe-copilot" {
		return false
	}
	model = copilotModelSlug(model)
	lower := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(lower, "gpt-6.") || strings.HasPrefix(lower, "gpt-6-") {
		return true
	}
	if strings.HasPrefix(lower, "gpt-5.") {
		return copilotGPT5Minor(lower) >= 5
	}
	return false
}

// copilotModelSlug strips an optional provider prefix and any "(level)" thinking
// suffix from a Copilot model id, leaving the wire id.
func copilotModelSlug(model string) string {
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	if idx := strings.IndexByte(model, '('); idx != -1 {
		model = model[:idx]
	}
	return model
}

// copilotGPT5Minor parses the minor number out of "gpt-5.<minor>..."; returns -1
// when it cannot be parsed.
func copilotGPT5Minor(lower string) int {
	rest := strings.TrimPrefix(lower, "gpt-5.")
	var n int
	for i, ch := range rest {
		if ch < '0' || ch > '9' {
			if i == 0 {
				return -1
			}
			break
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// IsCopilotResponsesModelSlug is the model-only form of IsCopilotResponsesModel,
// for ForwardGithub where the provider is already GitHub Copilot.
func IsCopilotResponsesModelSlug(model string) bool {
	return IsCopilotResponsesModel("github", model)
}

// forwardGithubOpenAI is the original GitHub Copilot behaviour: forward through the
// shared OpenAI-compat path and normalize Copilot context-overflow errors.
func forwardGithubOpenAI(w http.ResponseWriter, req *Request) error {
	err := ForwardOpenAI(w, req)
	if err == nil {
		return nil
	}

	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadRequest {
		return err
	}

	// Escalate to /responses for GPT models that the /chat/completions endpoint
	// can no longer serve. Never escalate Claude or Gemini: Copilot's /responses
	// gate is GPT-only and would answer a different 400 ("does not support
	// Responses API") instead of surfacing the real rejection (#1062 parity).
	model, _ := githubModel(req)
	if githubSupportsResponsesEndpoint(model) && githubIsChatOnlyError(ue.Body) {
		githubResponsesModels.Store(copilotModelSlug(model), struct{}{})
		return forwardGithubResponses(w, req)
	}

	// Normalize Copilot context-window overflow errors before returning.
	normalizeGithubContextError(ue)
	return err
}

// forwardGithubResponses forwards to GitHub Copilot's /responses endpoint.
// A Chat Completions client body is converted to Responses API format first;
// a native /v1/responses client body is relayed unchanged. handleCodexStream
// translates the Responses stream back to Chat Completions for non-Responses
// clients, or passes it through for Responses clients.
//
// The /responses lane is always consumed as a streaming event-source regardless
// of the client's preference; a non-streaming client has the frames
// re-aggregated by handleCodexStream.
//
// Upstream parity: executors/github.js executeWithResponsesEndpoint().
func forwardGithubResponses(w http.ResponseWriter, req *Request) error {
	body := req.Body
	if clientFormatFor(reqCtx(req)) != providers.FormatOpenAIResponses {
		// Chat Completions client — translate to Responses API wire format.
		transformed, _, err := buildResponsesRequest(req.Body, false)
		if err != nil {
			return fmt.Errorf("ForwardGithub transform body: %w", err)
		}
		body = transformed
		// Copilot rejects temperature on its Responses-native GPT models.
		var transformedFields map[string]any
		if err := json.Unmarshal(body, &transformedFields); err == nil {
			delete(transformedFields, "temperature")
			var err error
			body, err = marshalStable(transformedFields)
			if err != nil {
				return fmt.Errorf("ForwardGithub responses marshal: %w", err)
			}
		}
	}
	// Responses clients: body is already in Responses format; pass through.

	cfg := *req.Config
	cfg.BaseURL = githubResponsesBaseURL(req.Config.BaseURL)

	// Chat clients consume upstream SSE; native Responses clients retain their stream preference.
	upstreamStream := req.IsStream || clientFormatFor(reqCtx(req)) != providers.FormatOpenAIResponses
	resp, err := proxy.ForwardOpenAI(reqCtx(req), req.Client, &cfg, req.APIKey, body, upstreamStream)
	if err != nil {
		var ue *proxy.UpstreamError
		if errors.As(err, &ue) && ue.StatusCode == http.StatusBadRequest {
			normalizeGithubContextError(ue)
		}
		return fmt.Errorf("ForwardGithub (responses): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		var ue *proxy.UpstreamError
		if errors.As(err, &ue) && ue.StatusCode == http.StatusBadRequest {
			normalizeGithubContextError(ue)
		}
		return err
	}

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(reqCtx(req), resp.Body, 0, "github-responses")
		defer stallReader.Close()
		return handleCodexStream(w, req, stallReader)
	}
	return handleCodexStream(w, req, resp.Body)
}

// ForwardGithubClaudeMessages forwards a Claude-model request to Copilot's native
// Claude Messages endpoint (/v1/messages) in Claude Messages format, reusing the
// Claude stream/non-stream response handlers so a Claude client gets a passthrough
// and an OpenAI client gets a Claude→OpenAI translation.
func ForwardGithubClaudeMessages(w http.ResponseWriter, req *Request, model string) error {
	body := ensureMessagesFor(req.Body, "github", model)

	base := strings.TrimRight(req.Config.BaseURL, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	url := base + copilotMessagesPath

	headers := make(map[string]string, len(req.Config.StaticHeaders)+1)
	for k, v := range req.Config.StaticHeaders {
		headers[k] = v
	}
	if !req.Config.NoAuth {
		headers[req.Config.AuthHeader] = "Bearer " + req.APIKey
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "application/json"
	}
	headers["Content-Type"] = "application/json"

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := proxy.DoRequest(ctx, req.Client, "POST", url, headers, body)
	if err != nil {
		return fmt.Errorf("ForwardGithubClaudeMessages upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if req.IsStream {
		return handleClaudeMessagesStream(w, req, resp.Body)
	}
	return handleClaudeMessagesNonStream(w, req, resp.Body)
}

// githubModel returns the model id carried by the request, preferring the
// resolved ModelName and falling back to the body's "model" field.
func githubModel(req *Request) (string, bool) {
	if req.ModelName != "" {
		return req.ModelName, true
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &probe); err == nil && probe.Model != "" {
		return probe.Model, true
	}
	return "", false
}

// isClaudeModel reports whether a model id names an Anthropic Claude model,
// tolerating an optional provider prefix (e.g. "github/claude-3-7-sonnet").
func isClaudeModel(model string) bool {
	base := model
	if _, after, ok := strings.Cut(base, "/"); ok {
		base = after
	}
	return strings.HasPrefix(strings.ToLower(base), "claude-")
}

func normalizeGithubContextError(ue *proxy.UpstreamError) {
	if len(ue.Body) == 0 {
		return
	}

	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	if err := json.Unmarshal(ue.Body, &payload); err != nil {
		return
	}

	if payload.Error.Code == "invalid_request_body" && strings.HasPrefix(payload.Error.Message, copilotContextOverflowMsgPrefix) {
		payload.Error.Code = ContextLengthExceededCode
		payload.Error.Type = "invalid_request_error"
		if newBody, err := json.Marshal(payload); err == nil {
			ue.Body = newBody
		}
	}
}
