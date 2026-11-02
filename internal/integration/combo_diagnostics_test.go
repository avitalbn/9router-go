//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"testing"
)

// A quota failure followed by a winning model must be readable as one chain
// through the real dashboard APIs, with the same correlation ID on both rows.
func TestComboDiagnosticsIncludesQuotaAndWinningModel(t *testing.T) {
	env := newEnv(t)
	broken := env.NewUpstream(t, JSONResponder(http.StatusPaymentRequired, `{"error":{"message":"monthly quota exhausted"}}`))
	working := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "quota-conn", "deepseek", "Quota account", broken, "sk-fake")
	env.AddConnection(t, "working-conn", "groq", "Working account", working, "sk-fake")
	env.AddCombo(t, "diagnostic-combo", "diagnostic", []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"})
	const correlation = "combo-trace-test"
	res := env.Post(t, "/v1/chat/completions", ChatBody("diagnostic", false), WithHeader("X-Request-ID", correlation))
	if res.Status != http.StatusOK {
		t.Fatalf("completion = %d: %s", res.Status, res.Body)
	}
	var list struct {
		Details []struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			RequestID      string `json:"requestId"`
			RequestedModel string `json:"requestedModel"`
		} `json:"details"`
	}
	env.Get(t, "/api/usage/request-details").Decode(t, &list)
	if len(list.Details) != 2 {
		t.Fatalf("got %d detail rows, want quota + success", len(list.Details))
	}
	for _, row := range list.Details {
		if row.RequestID != correlation || row.RequestedModel != "diagnostic" {
			t.Errorf("uncorrelated summary: %+v", row)
		}
		var payload struct {
			Detail struct {
				RequestID string `json:"requestId"`
				Attempts  []struct {
					Provider   string `json:"provider"`
					Model      string `json:"model"`
					Reason     string `json:"reason"`
					StatusCode int    `json:"statusCode"`
					Skipped    bool   `json:"skipped"`
				} `json:"attempts"`
			} `json:"detail"`
		}
		env.Get(t, "/api/usage/request-details/"+url.PathEscape(row.ID)).Decode(t, &payload)
		trail := payload.Detail.Attempts
		dispatched := trail[:0]
		for _, a := range trail {
			if !a.Skipped {
				dispatched = append(dispatched, a)
			}
		}
		trail = dispatched
		want := 1
		if row.Status == "success" {
			want = 2
		}
		if len(trail) != want {
			t.Fatalf("%s row trail = %+v, want %d attempts", row.Status, trail, want)
		}
		if payload.Detail.RequestID != correlation || trail[0].StatusCode != 402 || trail[0].Reason != "quota_exceeded" {
			t.Fatalf("missing quota diagnostic: %+v", payload)
		}
		if want == 2 && (trail[1].Provider != "groq" || trail[1].Model != "llama-3.3-70b-versatile" || trail[1].StatusCode != 200 || trail[1].Reason != "success") {
			t.Errorf("missing winner: %+v", trail)
		}
	}
}

func TestComboDiagnosticsRecordsUndispatchedLeg(t *testing.T) {
	env := newEnv(t)
	env.AddCombo(t, "unavailable-combo", "unavailable", []string{"deepseek/deepseek-chat"})
	res := env.Post(t, "/v1/chat/completions", ChatBody("unavailable", false))
	if res.Status != 502 {
		t.Fatalf("unavailable = %d: %s", res.Status, res.Body)
	}
	var list struct {
		Details []struct {
			ID string `json:"id"`
		} `json:"details"`
	}
	env.Get(t, "/api/usage/request-details").Decode(t, &list)
	if len(list.Details) != 1 {
		t.Fatalf("undispatched combo has %d rows, want one", len(list.Details))
	}
	var payload struct {
		Detail struct {
			Attempts []struct {
				Skipped bool   `json:"skipped"`
				Model   string `json:"model"`
				Error   string `json:"error"`
			} `json:"attempts"`
		} `json:"detail"`
	}
	env.Get(t, "/api/usage/request-details/"+url.PathEscape(list.Details[0].ID)).Decode(t, &payload)
	a := payload.Detail.Attempts
	if len(a) != 1 || !a[0].Skipped || a[0].Model != "deepseek-chat" || a[0].Error == "" {
		t.Fatalf("missing unavailable leg: %+v", a)
	}
}
