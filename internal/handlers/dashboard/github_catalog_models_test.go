package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// githubModelsRoute mounts the connection-models handler on a chi router and
// returns a recorder for a single GET against the given connection id.
func githubModelsRoute(t *testing.T, connID string, h *DashboardHandler) *httptest.ResponseRecorder {
	t.Helper()
	rtr := chi.NewRouter()
	rtr.Get("/api/providers/{id}/models", h.HandleGetConnectionModels)
	req := httptest.NewRequest(http.MethodGet, "/api/providers/"+connID+"/models", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)
	return rec
}

// TestConnectionModels_GitHub_HappyPath verifies the full flow for a
// personal-account GitHub Copilot connection:
//   - token exchange is called with the right GitHub API headers,
//   - the /models call lands on the endpoint returned by the exchange,
//   - a model whose model_picker_enabled=false is filtered out,
//   - the standard {provider,connectionId,models} shape is returned with
//     capabilities limits and supports preserved.
func TestConnectionModels_GitHub_HappyPath(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	const fakeToken = "ghu_oauth_token"
	const fakeCopilotToken = "tid=copilot_fresh_token"
	const fakeEndpoint = "https://api.githubcopilot.com"

	tokenExchangeBody := []byte(`{
		"token": "` + fakeCopilotToken + `",
		"expires_at": "2099-01-01T00:00:00Z",
		"endpoints": {"api": "` + fakeEndpoint + `"}
	}`)
	modelsBody := []byte(`{
		"data": [
			{
				"id": "gpt-5-mini",
				"name": "GPT-5 Mini",
				"model_picker_enabled": true,
				"capabilities": {
					"type": "chat",
					"limits": {
						"max_context_window_tokens": 128000,
						"max_prompt_tokens": 120000,
						"max_output_tokens": 16384
					},
					"supports": {
						"vision": false,
						"tool_calls": true,
						"reasoning_effort": ["low", "medium", "high"]
					}
				},
				"supported_endpoints": ["/chat/completions"]
			},
			{
				"id": "internal-tool",
				"name": "Internal Only",
				"model_picker_enabled": false,
				"capabilities": {"type": "chat"},
				"supported_endpoints": ["/chat/completions"]
			}
		]
	}`)

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		switch rawURL {
		case "https://api.github.com/copilot_internal/v2/token":
			if headers["Authorization"] != "Bearer "+fakeToken {
				t.Errorf("token exchange: Authorization = %q, want Bearer %s", headers["Authorization"], fakeToken)
			}
			if headers["X-GitHub-Api-Version"] == "" {
				t.Error("token exchange: missing X-GitHub-Api-Version header")
			}
			return http.StatusOK, tokenExchangeBody, nil
		case fakeEndpoint + "/models":
			if headers["Authorization"] != "Bearer "+fakeCopilotToken {
				t.Errorf("/models: Authorization = %q, want Bearer %s", headers["Authorization"], fakeCopilotToken)
			}
			if headers["copilot-integration-id"] == "" {
				t.Error("/models: missing copilot-integration-id header")
			}
			return http.StatusOK, modelsBody, nil
		default:
			t.Errorf("unexpected URL: %s", rawURL)
			return http.StatusInternalServerError, nil, nil
		}
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-happy"
	if err := repo.CreateProviderConnectionFull(connID, "github", "oauth", "GitHub Personal", nil, `{"accessToken":"`+fakeToken+`"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Provider     string `json:"provider"`
		ConnectionID string `json:"connectionId"`
		Models       []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Capabilities struct {
				Type   string `json:"type"`
				Limits struct {
					MaxContextWindowTokens float64 `json:"max_context_window_tokens"`
					MaxOutputTokens        float64 `json:"max_output_tokens"`
				} `json:"limits"`
				Supports struct {
					ToolCalls bool `json:"tool_calls"`
					Vision    bool `json:"vision"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Provider != "github" || out.ConnectionID != connID {
		t.Errorf("provider=%q connectionId=%q, want github/%s", out.Provider, out.ConnectionID, connID)
	}
	if len(out.Models) != 1 {
		t.Fatalf("got %d model(s), want 1 (picker-disabled filtered); body: %s", len(out.Models), rec.Body.String())
	}
	m := out.Models[0]
	if m.ID != "gpt-5-mini" {
		t.Errorf("id = %q, want gpt-5-mini", m.ID)
	}
	if m.Name != "GPT-5 Mini" {
		t.Errorf("name = %q, want GPT-5 Mini", m.Name)
	}
	if m.Capabilities.Type != "chat" {
		t.Errorf("capabilities.type = %q, want chat", m.Capabilities.Type)
	}
	if m.Capabilities.Limits.MaxContextWindowTokens != 128000 {
		t.Errorf("max_context_window_tokens = %v, want 128000", m.Capabilities.Limits.MaxContextWindowTokens)
	}
	if !m.Capabilities.Supports.ToolCalls {
		t.Error("tool_calls should be true")
	}
	if m.Capabilities.Supports.Vision {
		t.Error("vision should be false")
	}
}

// TestConnectionModels_GitHub_BusinessEndpoint verifies that the business
// endpoint returned by the Copilot token exchange is used for /models when
// the account is on an enterprise plan (endpoints.api = api.business.*).
func TestConnectionModels_GitHub_BusinessEndpoint(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	const businessBase = "https://api.business.githubcopilot.com"
	const bizToken = "tid=biz_copilot_token"

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		switch rawURL {
		case "https://api.github.com/copilot_internal/v2/token":
			return http.StatusOK, []byte(`{"token":"` + bizToken + `","endpoints":{"api":"` + businessBase + `"}}`), nil
		case businessBase + "/models":
			if headers["Authorization"] != "Bearer "+bizToken {
				t.Errorf("business /models auth = %q, want Bearer %s", headers["Authorization"], bizToken)
			}
			return http.StatusOK, []byte(`{"data":[{"id":"gpt-6.1-sol","name":"GPT-6.1 Sol","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"vision":false,"tool_calls":true,"reasoning_effort":["low","medium","high","xhigh","max"]}},"supported_endpoints":["/responses","ws:/responses"]}]}`), nil
		default:
			t.Errorf("unexpected URL: %s (expected exchange then %s/models)", rawURL, businessBase)
			return http.StatusInternalServerError, nil, nil
		}
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-biz"
	if err := repo.CreateProviderConnectionFull(connID, "github", "oauth", "GitHub Business", nil, `{"accessToken":"ghu_biz_token"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Models) != 1 || out.Models[0].ID != "gpt-6.1-sol" {
		t.Errorf("models = %+v, want [{id:gpt-6.1-sol}]", out.Models)
	}
}

// TestConnectionModels_GitHub_TokenExchangeFallback verifies that when the
// Copilot token exchange fails (e.g. an API key connection with no GitHub
// OAuth access), the handler falls back to calling /models on the standard
// endpoint with the stored credential.
func TestConnectionModels_GitHub_TokenExchangeFallback(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	const directKey = "ghp_direct_apikey"

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		if rawURL == "https://api.github.com/copilot_internal/v2/token" {
			// non-200 → exchange failed; handler must fall back
			return http.StatusUnauthorized, []byte(`{"message":"Bad credentials"}`), nil
		}
		if rawURL == "https://api.githubcopilot.com/models" {
			if headers["Authorization"] != "Bearer "+directKey {
				t.Errorf("fallback /models auth = %q, want Bearer %s", headers["Authorization"], directKey)
			}
			return http.StatusOK, []byte(`{"data":[{"id":"gpt-5-mini","name":"GPT-5 Mini","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions"]}]}`), nil
		}
		t.Errorf("unexpected URL: %s", rawURL)
		return http.StatusInternalServerError, nil, nil
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-fallback"
	if err := repo.CreateProviderConnectionFull(connID, "github", "apikey", "GitHub API Key", nil, `{"apiKey":"`+directKey+`"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Models) != 1 || out.Models[0].ID != "gpt-5-mini" {
		t.Errorf("fallback models = %+v, want [{gpt-5-mini}]", out.Models)
	}
}

// TestConnectionModels_GitHub_NoToken verifies that a connection without any
// stored credential returns 401 before making any upstream call.
func TestConnectionModels_GitHub_NoToken(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		t.Errorf("probe called unexpectedly: %s %s", method, rawURL)
		return http.StatusInternalServerError, nil, nil
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-notoken"
	if err := repo.CreateProviderConnectionFull(connID, "github", "oauth", "GitHub No Token", nil, `{}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestConnectionModels_GitHub_UpstreamError verifies that a non-200 from the
// Copilot /models endpoint is forwarded as-is (401 stays 401, not swallowed).
func TestConnectionModels_GitHub_UpstreamError(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		if rawURL == "https://api.github.com/copilot_internal/v2/token" {
			return http.StatusOK, []byte(`{"token":"ct","endpoints":{"api":"https://api.githubcopilot.com"}}`), nil
		}
		// /models: stale or revoked token
		return http.StatusUnauthorized, []byte(`{"message":"Bad credentials"}`), nil
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-upstreamerr"
	if err := repo.CreateProviderConnectionFull(connID, "github", "oauth", "GitHub Stale", nil, `{"accessToken":"stale"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestConnectionModels_GitHub_EmptyData verifies that an empty data array
// returns an empty models list rather than null or an error.
func TestConnectionModels_GitHub_EmptyData(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		if rawURL == "https://api.github.com/copilot_internal/v2/token" {
			return http.StatusOK, []byte(`{"token":"ct","endpoints":{"api":"https://api.githubcopilot.com"}}`), nil
		}
		return http.StatusOK, []byte(`{"data":[]}`), nil
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-empty"
	if err := repo.CreateProviderConnectionFull(connID, "github", "oauth", "GitHub Empty", nil, `{"accessToken":"tok"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Models == nil {
		t.Error("models should be [] not null")
	}
	if len(out.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(out.Models))
	}
}

// TestConnectionModels_GitHub_GhAlias verifies that connections stored with
// the "gh" alias (older stored connections) are handled identically to "github".
func TestConnectionModels_GitHub_GhAlias(t *testing.T) {
	origProbe := validateProbeDo
	defer func() { validateProbeDo = origProbe }()

	validateProbeDo = func(_ context.Context, method, rawURL string, headers map[string]string, body []byte) (int, []byte, error) {
		if rawURL == "https://api.github.com/copilot_internal/v2/token" {
			return http.StatusOK, []byte(`{"token":"ct","endpoints":{"api":"https://api.githubcopilot.com"}}`), nil
		}
		return http.StatusOK, []byte(`{"data":[{"id":"gpt-5-mini","name":"GPT-5 Mini","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions"]}]}`), nil
	}

	repo, cleanup := setupTestDB(t)
	defer cleanup()

	connID := "conn-gh-alias"
	if err := repo.CreateProviderConnectionFull(connID, "gh", "oauth", "GitHub Alias", nil, `{"accessToken":"tok"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := githubModelsRoute(t, connID, &DashboardHandler{Repo: repo})
	if rec.Code != http.StatusOK {
		t.Fatalf("gh alias: status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Provider string `json:"provider"`
		Models   []any  `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Provider != "gh" {
		t.Errorf("provider = %q, want gh (stored provider is preserved)", out.Provider)
	}
	if len(out.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(out.Models))
	}
}
