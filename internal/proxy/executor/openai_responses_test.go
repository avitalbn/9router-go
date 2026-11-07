package executor

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

func TestForwardOpenAIResponsesToolsAndReasoning(t *testing.T) {
	for _, tc := range []struct {
		model  string
		stream bool
	}{
		{"gpt-6.1-sol", false},
		{"gpt-6.1-sol", true},
		{"gpt-6.1-sol-2026-09-29-global-std", false},
		{"gpt-6.1-sol-2026-09-29-global-std", true},
	} {
		t.Run(fmt.Sprintf("%s/stream=%t", tc.model, tc.stream), func(t *testing.T) {
			model, stream := tc.model, tc.stream
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input     []any `json:"input"`
					Stream    bool  `json:"stream"`
					Reasoning struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
					Tools []struct {
						Type string `json:"type"`
						Name string `json:"name"`
					} `json:"tools"`
				}
				if err := json.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Path != "/v1/responses" || len(body.Input) != 1 || !body.Stream || body.Reasoning.Effort != "high" || len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Name != "get_weather" {
					t.Errorf("invalid Responses request: path=%s body=%+v", r.URL.Path, body)
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"message":"invalid Responses request"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_weather","call_id":"call_weather","name":"get_weather","arguments":""}}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\":\"Paris\"}"}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"city\":\"Paris\"}"}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp_weather","usage":{"input_tokens":12,"output_tokens":8}}}`+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()

			rec := httptest.NewRecorder()
			err := ForwardOpenAI(rec, &Request{
				Ctx: t.Context(), Client: server.Client(),
				Provider:  "openai-compatible-chat-hai-proxy",
				Config:    &providers.ProviderConfig{BaseURL: server.URL + "/v1/responses"},
				ModelName: model, IsStream: stream,
				Body: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Weather in Paris?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"reasoning_effort":"high","stream":%t}`, model, stream)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !stream {
				var response struct {
					Choices []struct {
						FinishReason string `json:"finish_reason"`
						Message      struct {
							ToolCalls []struct {
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"message"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Choices) != 1 || response.Choices[0].FinishReason != "tool_calls" || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
					t.Fatalf("tool completion = %s, error %v", rec.Body.String(), err)
				}
				return
			}
			var name, args, finish string
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				var chunk struct {
					Choices []struct {
						FinishReason string `json:"finish_reason"`
						Delta        struct {
							ToolCalls []struct {
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
					t.Fatal(err)
				}
				for _, choice := range chunk.Choices {
					if choice.FinishReason != "" {
						finish = choice.FinishReason
					}
					for _, call := range choice.Delta.ToolCalls {
						name += call.Function.Name
						args += call.Function.Arguments
					}
				}
			}
			if name != "get_weather" || args != `{"city":"Paris"}` || finish != "tool_calls" || !strings.Contains(rec.Body.String(), "data: [DONE]") {
				t.Fatalf("streamed tool call: name=%q args=%q finish=%q, body=%s", name, args, finish, rec.Body.String())
			}
		})
	}
}
