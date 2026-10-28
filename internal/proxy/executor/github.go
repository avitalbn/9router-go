package executor

import (
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

// ForwardGithub handles GitHub Copilot requests. It routes Responses-only GPT
// models (those that return 400 "not accessible via the /chat/completions
// endpoint" or "The requested model is not supported") to the /responses lane
// transparently — falling back on the first failure and caching the decision
// for subsequent calls so the round-trip only happens once per model per
// process lifetime.
//
// Claude and Gemini models are never escalated to /responses. The Copilot
// context-window overflow normalization (context_length_exceeded) runs on
// errors from both lanes.
//
// Upstream parity: executors/github.js execute() +
// executeWithResponsesEndpoint().
func ForwardGithub(w http.ResponseWriter, req *Request) error {
	// Fast-path: model was previously discovered to need /responses.
	if _, known := githubResponsesModels.Load(req.ModelName); known {
		return forwardGithubResponses(w, req)
	}

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
	if githubSupportsResponsesEndpoint(req.ModelName) && githubIsChatOnlyError(ue.Body) {
		githubResponsesModels.Store(req.ModelName, struct{}{})
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
		transformed, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardGithub transform body: %w", err)
		}
		body = transformed
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
