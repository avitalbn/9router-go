package chat

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

func TestCompatibleResponsesNodeClientFormats(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input              any    `json:"input"`
					Stream             bool   `json:"stream"`
					PreviousResponseID string `json:"previous_response_id"`
					Store              bool   `json:"store"`
					Reasoning          struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				}
				if err := json.UnmarshalRead(r.Body, &body); err != nil || r.URL.Path != "/v1/responses" || body.Input == nil || body.Reasoning.Effort != "high" || len(body.Tools) != 1 || body.Tools[0].Name != "get_weather" {
					t.Errorf("Responses request: %+v, path=%s, error=%v", body, r.URL.Path, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if native {
					if body.Stream || !body.Store || body.PreviousResponseID != "resp_previous" {
						t.Errorf("native replay fields changed: %+v", body)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"resp_next","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Paris"}]}]}`)
					return
				}
				if !body.Stream {
					t.Error("Chat client needs upstream Responses SSE")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.output_text.delta","delta":"Paris","output_index":0,"content_index":0}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"id":"resp_next","usage":{"input_tokens":12,"output_tokens":2}}}`+"\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			_, err := database.Exec(`INSERT INTO providerConnections (id,provider,authType,name,priority,isActive,data,createdAt,updatedAt) VALUES ('conn-responses','openai-compatible-chat-custom','apikey','Custom',1,1,?,'2026-11-07T00:00:00Z','2026-11-07T00:00:00Z')`, `{"apiKey":"test","providerSpecificData":{"baseUrl":"`+upstream.URL+`/v1","apiType":"responses"}}`)
			if err != nil {
				t.Fatal(err)
			}
			handler := NewChatHandler(db.NewRepo(database))
			body := []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Paris?"}],"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`)
			ctx := t.Context()
			if native {
				ctx = translator.WithClientFormat(ctx, translator.ClientFormatResponses)
				body = []byte(`{"model":"gpt-6.1-sol","input":"Paris?","previous_response_id":"resp_previous","store":true,"stream":false,"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}`)
			}
			rec := httptest.NewRecorder()
			handler.handleSingleModel(ctx, rec, body, &ModelInfo{Provider: "openai-compatible-chat-custom", Model: "gpt-6.1-sol", ConnectionID: "conn-responses"}, false, false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				ID      string `json:"id"`
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if native {
				if response.ID != "resp_next" || !strings.Contains(rec.Body.String(), `"text":"Paris"`) || len(response.Choices) != 0 {
					t.Fatalf("native response changed: %s", rec.Body.String())
				}
			} else if len(response.Choices) != 1 || response.Choices[0].Message.Content != "Paris" {
				t.Fatalf("Chat response not translated: %s", rec.Body.String())
			}
		})
	}
}
