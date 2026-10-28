package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

func TestForwardGithubContextOverflowNormalization(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		wantCode     string
		wantModified bool
	}{
		{
			name:   "copilot context window overflow normalized",
			status: http.StatusBadRequest,
			body: `{
				"error": {
					"code": "invalid_request_body",
					"message": "Your input exceeds the context window of this model. Please adjust your input and try again.",
					"type": "invalid_request_error"
				}
			}`,
			wantCode:     "context_length_exceeded",
			wantModified: true,
		},
		{
			name:   "other invalid_request_body untouched",
			status: http.StatusBadRequest,
			body: `{
				"error": {
					"code": "invalid_request_body",
					"message": "Unknown parameter 'foo'.",
					"type": "invalid_request_error"
				}
			}`,
			wantCode:     "invalid_request_body",
			wantModified: false,
		},
		{
			name:   "500 internal error untouched",
			status: http.StatusInternalServerError,
			body: `{
				"error": {
					"code": "server_error",
					"message": "Internal error"
				}
			}`,
			wantCode:     "server_error",
			wantModified: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			rec := httptest.NewRecorder()
			cfg := &providers.ProviderConfig{
				BaseURL: server.URL,
			}
			req := &Request{
				Ctx:       t.Context(),
				Client:    server.Client(),
				Provider:  "github",
				Config:    cfg,
				Endpoint:  server.URL,
				ModelName: "gpt-4o",
				Body:      []byte(`{"model":"gpt-4o","messages":[]}`),
			}

			err := ForwardGithub(rec, req)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}

			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected UpstreamError, got %T: %v", err, err)
			}

			var parsed struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(ue.Body, &parsed); err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}

			if parsed.Error.Code != tc.wantCode {
				t.Errorf("expected code %q, got %q", tc.wantCode, parsed.Error.Code)
			}
		})
	}
}

// minimalResponsesStream is a GitHub /responses SSE payload that produces one
// text delta followed by a response.completed event. ProcessCodexEvent converts
// it to Chat Completions chunks so the consumer never sees raw Responses events.
const minimalResponsesStream = "" +
	`data: {"type":"response.output_text.delta","delta":"Hi","output_index":0,"content_index":0}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n" +
	"data: [DONE]\n\n"

// chatOnlyErrorBody is the 400 payload Copilot returns for models that are not
// served by /chat/completions (parity with executors/github.js execute()).
const chatOnlyErrorBody = `{"error":{"message":"this model is not accessible via the /chat/completions endpoint","code":"model_not_supported","type":"invalid_request_error"}}`

// newGithubMockServer builds a test server that returns chatOnlyError on every
// path except /responses, which streams responsesBody.
func newGithubMockServer(t *testing.T, chatStatusCode int, chatBody, responsesBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			var body struct {
				Input  []any `json:"input"`
				Stream bool  `json:"stream"`
			}
			if err := json.UnmarshalRead(r.Body, &body); err != nil || len(body.Input) != 1 || !body.Stream {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"code":"invalid_request_body","message":"Responses requires input and stream:true"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, responsesBody)
			return
		}
		w.WriteHeader(chatStatusCode)
		fmt.Fprint(w, chatBody)
	}))
}

// TestForwardGithubResponsesFallback_Stream validates that a Chat Completions
// streaming client receives well-formed SSE chunks when the /chat/completions
// endpoint returns the Responses-only 400 and the executor retries on /responses.
// The mock upstream drives the discovery path (model not pre-warmed); the
// output must be Chat Completions format, not raw Responses events.
func TestForwardGithubResponsesFallback_Stream(t *testing.T) {
	server := newGithubMockServer(t, http.StatusBadRequest, chatOnlyErrorBody, minimalResponsesStream)
	defer server.Close()

	rec := httptest.NewRecorder()
	cfg := &providers.ProviderConfig{BaseURL: server.URL}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		Endpoint:  server.URL,
		ModelName: "gpt-responses-only-test-stream",
		Body:      []byte(`{"model":"gpt-responses-only-test-stream","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		IsStream:  true,
	}

	err := ForwardGithub(rec, req)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	body := rec.Body.String()
	// Must have at least one Chat Completions chunk.
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("expected Chat Completions chunks in stream, got:\n%s", body)
	}
	// Must contain the delta text from the mock.
	if !strings.Contains(body, `"Hi"`) {
		t.Errorf("expected delta content %q in stream, got:\n%s", "Hi", body)
	}
	// Raw Responses events must not leak to the Chat Completions client.
	if strings.Contains(body, "response.output_text.delta") {
		t.Errorf("raw Responses event type leaked to Chat Completions client:\n%s", body)
	}
	// The stream must be closed with [DONE].
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("stream never closed with [DONE]:\n%s", body)
	}
}

// TestForwardGithubResponsesFallback_NonStream validates the non-streaming
// contract: after a /chat/completions 400, the /responses stream is
// re-aggregated into a single chat.completion JSON object.
func TestForwardGithubResponsesFallback_NonStream(t *testing.T) {
	server := newGithubMockServer(t, http.StatusBadRequest, chatOnlyErrorBody, minimalResponsesStream)
	defer server.Close()

	rec := httptest.NewRecorder()
	cfg := &providers.ProviderConfig{BaseURL: server.URL}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		Endpoint:  server.URL,
		ModelName: "gpt-responses-only-test-nonstream",
		Body:      []byte(`{"model":"gpt-responses-only-test-nonstream","messages":[{"role":"user","content":"hello"}]}`),
		IsStream:  false,
	}

	err := ForwardGithub(rec, req)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	body := rec.Body.String()
	// Non-streaming path must produce a chat.completion object, not SSE frames.
	if !strings.Contains(body, `"chat.completion"`) {
		t.Errorf("expected chat.completion object in non-streaming response, got:\n%s", body)
	}
	// Content must include the delta text.
	if !strings.Contains(body, "Hi") {
		t.Errorf("expected delta text %q in response, got:\n%s", "Hi", body)
	}
	var completion struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil || completion.Model != req.ModelName || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "Hi" {
		t.Fatalf("aggregated completion = %s, error %v", rec.Body.String(), err)
	}
	// SSE framing must not reach the client.
	if strings.Contains(body, "data:") {
		t.Errorf("SSE framing leaked into non-streaming response:\n%s", body)
	}
}

// TestForwardGithubClaudeNotEscalated guards the isolation contract: a 400
// "model not supported" for a Claude model must never trigger the /responses
// escalation. The /responses gate is GPT-only; routing Claude there returns a
// different 400 that obscures the real error (#1062 parity).
func TestForwardGithubClaudeNotEscalated(t *testing.T) {
	var responsesHit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			responsesHit = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, chatOnlyErrorBody)
	}))
	defer server.Close()

	rec := httptest.NewRecorder()
	cfg := &providers.ProviderConfig{BaseURL: server.URL}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		Endpoint:  server.URL,
		ModelName: "claude-fable-5.1",
		Body:      []byte(`{"model":"claude-fable-5.1","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		IsStream:  true,
	}

	err := ForwardGithub(rec, req)
	if err == nil {
		t.Fatal("expected error for unsupported Claude model, got nil")
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 UpstreamError, got: %v", err)
	}
	if responsesHit {
		t.Error("Claude model was escalated to /responses, violating provider isolation")
	}
}

// TestForwardGithubGeminiNotEscalated mirrors the Claude test for Gemini
// models: Copilot's /responses endpoint is GPT-only and must not receive
// Gemini requests even when /chat/completions refuses them.
func TestForwardGithubGeminiNotEscalated(t *testing.T) {
	var responsesHit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			responsesHit = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, chatOnlyErrorBody)
	}))
	defer server.Close()

	rec := httptest.NewRecorder()
	cfg := &providers.ProviderConfig{BaseURL: server.URL}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		Endpoint:  server.URL,
		ModelName: "gemini-4-argon",
		Body:      []byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		IsStream:  true,
	}

	err := ForwardGithub(rec, req)
	if err == nil {
		t.Fatal("expected error for Gemini model on chat endpoint, got nil")
	}
	if responsesHit {
		t.Error("Gemini model was escalated to /responses, violating provider isolation")
	}
}

// TestForwardGithubPreKnownResponses validates that a model in
// githubResponsesModels (pre-warmed or previously discovered) goes directly to
// /responses without attempting /chat/completions first. This covers the
// confirmed gpt-6.1-sol case and any runtime-learned model from a prior call.
func TestForwardGithubPreKnownResponses(t *testing.T) {
	var chatHit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, minimalResponsesStream)
			return
		}
		// /chat/completions — must not be reached.
		chatHit = true
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"should not be called","code":"unexpected"}}`)
	}))
	defer server.Close()

	rec := httptest.NewRecorder()
	cfg := &providers.ProviderConfig{BaseURL: server.URL}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		Endpoint:  server.URL,
		ModelName: "gpt-6.1-sol", // pre-warmed in init()
		Body:      []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}],"stream":true}`),
		IsStream:  true,
	}

	err := ForwardGithub(rec, req)
	if err != nil {
		t.Fatalf("expected nil error for pre-known model, got: %v", err)
	}
	if chatHit {
		t.Error("gpt-6.1-sol tried /chat/completions before /responses — pre-warming broke")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("expected Chat Completions stream output, got:\n%s", body)
	}
}

// TestForwardGithubResponsesClientPassthrough verifies that a /v1/responses
// client for a pre-known model (gpt-6.1-sol) receives the upstream Responses
// events byte-for-byte — no Chat Completions round-trip. previous_response_id
// and item ids must survive intact.
func TestForwardGithubResponsesClientPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, minimalResponsesStream)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, chatOnlyErrorBody)
	}))
	defer server.Close()

	// Simulate a /v1/responses client: context has the Responses client format.
	ctx := translator.WithResponsesBridge(
		translator.WithClientFormat(
			context.Background(),
			translator.ClientFormatResponses,
		),
	)

	rec := httptest.NewRecorder()
	cfg := &providers.ProviderConfig{BaseURL: server.URL}
	req := &Request{
		Ctx:       ctx,
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		Endpoint:  server.URL,
		ModelName: "gpt-6.1-sol",
		// Responses-format body: has "input" not "messages".
		Body:     []byte(`{"model":"gpt-6.1-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`),
		IsStream: true,
	}

	err := ForwardGithub(rec, req)
	if err != nil {
		t.Fatalf("expected nil error for Responses client, got: %v", err)
	}

	body := rec.Body.String()
	// A Responses client must see Responses events, not Chat Completions chunks.
	if strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("Chat Completions chunks reached a /v1/responses client:\n%s", body)
	}
	// The upstream Responses events must pass through.
	if !strings.Contains(body, "response.") {
		t.Errorf("no Responses events in passthrough output:\n%s", body)
	}
}

func TestForwardGithubResponsesUpstreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"revoked", http.StatusUnauthorized, `{"error":{"code":"invalid_token","message":"expired"}}`, "invalid_token"},
		{"unsupported", http.StatusBadRequest, `{"error":{"code":"model_not_supported","message":"The requested model is not supported."}}`, "model_not_supported"},
		{"overflow", http.StatusBadRequest, `{"error":{"code":"invalid_request_body","message":"Your input exceeds the context window of this model."}}`, "context_length_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			rec := httptest.NewRecorder()
			err := ForwardGithub(rec, &Request{
				Ctx: t.Context(), Client: server.Client(), Provider: "github",
				Config:    &providers.ProviderConfig{BaseURL: server.URL},
				ModelName: "gpt-6.1-sol", Body: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hello"}]}`),
			})
			var ue *proxy.UpstreamError
			if !errors.As(err, &ue) || ue.StatusCode != tc.status {
				t.Fatalf("upstream refusal = %v, want status %d", err, tc.status)
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(ue.Body, &payload); err != nil || payload.Error.Code != tc.code {
				t.Fatalf("refusal body = %s, want code %s", ue.Body, tc.code)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("refusal became a success body: %s", rec.Body.String())
			}
		})
	}
}

func TestForwardGithubNativeNonStreamPreservesReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Stream             bool   `json:"stream"`
			PreviousResponseID string `json:"previous_response_id"`
			Store              bool   `json:"store"`
		}
		if err := json.UnmarshalRead(r.Body, &input); err != nil || input.Stream || !input.Store || input.PreviousResponseID != "resp_previous" {
			t.Errorf("replay fields lost: %+v, error %v", input, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_next","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`)
	}))
	defer server.Close()
	rec := httptest.NewRecorder()
	ctx := translator.WithResponsesBridge(translator.WithClientFormat(t.Context(), translator.ClientFormatResponses))
	err := ForwardGithub(rec, &Request{
		Ctx: ctx, Client: server.Client(), Provider: "github", Config: &providers.ProviderConfig{BaseURL: server.URL},
		ModelName: "gpt-6.1-sol", Body: []byte(`{"model":"gpt-6.1-sol","input":"hello","previous_response_id":"resp_previous","store":true,"stream":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     string `json:"id"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.ID != "resp_next" || len(response.Output) != 1 || len(response.Output[0].Content) != 1 || response.Output[0].Content[0].Text != "OK" {
		t.Fatalf("native non-stream response = %s, error %v", rec.Body.String(), err)
	}
}
