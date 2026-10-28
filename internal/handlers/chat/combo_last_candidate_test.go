package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// comboTestConn is one provider connection a combo test seeds, pointed at a
// fake upstream.
type comboTestConn struct {
	id, provider, baseURL string
}

// seedComboConns builds a test database holding only the given connections
// and one combo over entries.
func seedComboConns(t *testing.T, comboName string, entries []string, conns ...comboTestConn) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}
	for _, c := range conns {
		data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock", "baseUrl": c.baseURL})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, ?, 'apikey', ?, 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			c.id, c.provider, c.id, string(data)); err != nil {
			t.Fatalf("insert %s: %v", c.id, err)
		}
	}
	models, _ := json.Marshal(entries)
	if _, err := database.Exec(`INSERT INTO combos (id, name, models, createdAt, updatedAt) VALUES
		(?, ?, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, "id-"+comboName, comboName, string(models)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}
	return db.NewRepo(database)
}

func sendComboTurn(h *ChatHandler, combo string, messages, stream bool) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":64,"stream":%t}`, combo, stream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	if messages {
		h.HandleMessages(rec, req)
	} else {
		h.HandleChatCompletions(rec, req)
	}
	return rec
}

// TestComboTimeoutSparesLastDispatchableMember pins #776: the member that
// keeps the global deadline is the last one the loop can still dispatch, not
// the last index. When the final entry will be skipped, a timer on the member
// before it would cut off the only remaining attempt and answer 504 where the
// same combo without the setting answers.
func TestComboTimeoutSparesLastDispatchableMember(t *testing.T) {
	const comboName = "last-dispatchable"
	cases := []struct {
		name string
		// final is the combo's last entry; seed adds what it needs.
		final string
		seed  func(t *testing.T, repo *db.Repo)
		conns func(fallbackURL string) []comboTestConn
		// cut: the slow member must be cut and the final entry must answer.
		cut bool
	}{
		{name: "final entry has no connection", final: "xai/grok-2"},
		{name: "final entry does not resolve", final: "no-such-combo"},
		{
			name:  "final entry is model-locked",
			final: "xai/grok-2",
			conns: func(url string) []comboTestConn { return []comboTestConn{{"mock-c", "xai", url}} },
			seed: func(t *testing.T, repo *db.Repo) {
				if err := repo.LockConnectionModel("mock-c", "grok-2", 600, 1); err != nil {
					t.Fatalf("lock final member: %v", err)
				}
			},
		},
		{
			name:  "dispatchable final entry still advances the combo",
			final: "xai/grok-2",
			conns: func(url string) []comboTestConn { return []comboTestConn{{"mock-c", "xai", url}} },
			cut:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachComboEndpoint(t, func(t *testing.T, messages, stream bool) {
				slow := newMemberStub(t, stream, 300*time.Millisecond, "slow but fine")
				final := newMemberStub(t, stream, 0, "final member")
				conns := []comboTestConn{{"mock-a", "deepseek", slow.url}}
				if tc.conns != nil {
					conns = append(conns, tc.conns(final.url)...)
				}
				repo := seedComboConns(t, comboName, []string{"deepseek/deepseek-chat", tc.final}, conns...)
				if tc.seed != nil {
					tc.seed(t, repo)
				}
				if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{comboName: map[string]any{"firstTokenTimeoutMs": 100}}}); err != nil {
					t.Fatal(err)
				}

				rec := sendComboTurn(NewChatHandler(repo), comboName, messages, stream)

				out := rec.Body.String()
				want, unwanted := "slow but fine", "final member"
				if tc.cut {
					want, unwanted = unwanted, want
				}
				if rec.Code != http.StatusOK || !strings.Contains(out, want) || strings.Contains(out, unwanted) {
					t.Fatalf("want %q, got %d %s", want, rec.Code, out)
				}
			})
		})
	}
}

// A combo none of whose members has a connection answers with the reason, the
// way a single unconnected model does, instead of "no valid entries".
func TestComboNamesMissingConnections(t *testing.T) {
	forEachComboEndpoint(t, func(t *testing.T, messages, stream bool) {
		repo := seedComboConns(t, "unconnected", []string{"xai/grok-2", "groq/qwen3-32b", "xai/grok-3"})

		rec := sendComboTurn(NewChatHandler(repo), "unconnected", messages, stream)

		out := rec.Body.String()
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, out)
		}
		for _, provider := range []string{"xai", "groq"} {
			if !strings.Contains(out, "no active connections for provider: "+provider) {
				t.Errorf("error does not name %s: %s", provider, out)
			}
		}
		if strings.Count(out, "provider: xai") != 1 {
			t.Errorf("a provider listed twice should be named once: %s", out)
		}
	})
}

// A member that reached its upstream keeps the error the client can act on: a
// connection missing elsewhere in the combo must not replace it.
func TestComboUpstreamErrorOutranksMissingConnection(t *testing.T) {
	for _, order := range [][]string{
		{"deepseek/deepseek-chat", "xai/grok-2"},
		{"xai/grok-2", "deepseek/deepseek-chat"},
	} {
		t.Run(order[0]+" first", func(t *testing.T) {
			rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"deepseek key revoked"}}`))
			}))
			t.Cleanup(rejecting.Close)
			repo := seedComboConns(t, "mixed", order, comboTestConn{"mock-a", "deepseek", rejecting.URL})

			rec := sendComboTurn(NewChatHandler(repo), "mixed", false, false)

			if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "deepseek key revoked") {
				t.Fatalf("want the upstream 401, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
