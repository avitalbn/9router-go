package chat

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The model response timeout (#750) under the smart strategy (System One
// routing with the default Jev router, #623). The router reorders the combo
// before any candidate runs, so these pin the interplay a client can observe:
// which member counts as the last one, whether the evaluation spends the
// candidate's budget, and what a timeout does to the session pin and bench.

const (
	smartTimeoutCombo = "smart-timeout"
	// seedComboWithTwoUpstreams stores the deepseek member first.
	smartStoredFirst = "deepseek/deepseek-chat"
	smartStoredLast  = "groq/qwen3-32b"
	// smartStall outlives every assertion window: a member that stalls this
	// long is only ever left behind by the response timeout.
	smartStall = 3 * time.Second
)

// jevRouterStub is a fake System One endpoint behind the default router
// model. choose receives the 1-based evaluation number.
type jevRouterStub struct {
	calls     atomic.Int32
	lastModel atomic.Value
}

func newJevRouterStub(t *testing.T, delay time.Duration, choose func(call int32) string) *jevRouterStub {
	t.Helper()
	stub := &jevRouterStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := stub.calls.Add(1)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		stub.lastModel.Store(req.Model)
		if delay > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(delay):
			}
		}
		_, _ = fmt.Fprintf(w, `{"answers":{"route":{"choice":%q,"confidence":0.95}}}`, choose(n))
	}))
	t.Cleanup(srv.Close)
	stubDefaultSmartRouter(t, srv)
	return stub
}

// memberStub is one combo member's upstream. A streaming member sends a
// role-only frame at once (not usable content), then its answer after delay.
// It consumes the request body first, as a real provider does: until it has,
// net/http cannot notice the gateway hanging up, and aborted would never fire.
type memberStub struct {
	calls   atomic.Int32
	aborted chan struct{}
	url     string
}

func newMemberStub(t *testing.T, stream bool, delay time.Duration, answer string) *memberStub {
	t.Helper()
	stub := &memberStub{aborted: make(chan struct{}, 8)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
			w.(http.Flusher).Flush()
		}
		if delay > 0 {
			select {
			case <-r.Context().Done():
				stub.aborted <- struct{}{}
				return
			case <-time.After(delay):
			}
		}
		if stream {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + answer + "\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + answer + `"}}]}`))
	}))
	t.Cleanup(srv.Close)
	stub.url = srv.URL
	return stub
}

func newSmartTimeoutHandler(t *testing.T, storedFirst, storedLast *memberStub, strategy map[string]any) *ChatHandler {
	t.Helper()
	repo := seedComboWithTwoUpstreams(t, "combo-"+smartTimeoutCombo, storedFirst.url, storedLast.url, smartTimeoutCombo)
	cfg := map[string]any{"fallbackStrategy": "smart"}
	maps.Copy(cfg, strategy)
	if err := repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{smartTimeoutCombo: cfg}}); err != nil {
		t.Fatalf("write combo strategy: %v", err)
	}
	return NewChatHandler(repo)
}

func sendSmartTimeoutTurn(h *ChatHandler, messages, stream bool, session string) (*httptest.ResponseRecorder, time.Duration) {
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"write a sql query"}],"stream":%t}`, smartTimeoutCombo, stream)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if session != "" {
		req.Header.Set("x-session-id", session)
	}
	rec := httptest.NewRecorder()
	start := time.Now()
	if messages {
		h.HandleMessages(rec, req)
	} else {
		h.HandleChatCompletions(rec, req)
	}
	return rec, time.Since(start)
}

func forEachComboEndpoint(t *testing.T, run func(t *testing.T, messages, stream bool)) {
	for _, messages := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			name := "chat"
			if messages {
				name = "messages"
			}
			if stream {
				name += "-stream"
			}
			t.Run(name, func(t *testing.T) { run(t, messages, stream) })
		}
	}
}

func TestSmartComboTimeoutAdvancesPastChosenModel(t *testing.T) {
	forEachComboEndpoint(t, func(t *testing.T, messages, stream bool) {
		fast := newMemberStub(t, stream, 0, "fast member")
		stalled := newMemberStub(t, stream, smartStall, "stalled member")
		router := newJevRouterStub(t, 0, func(int32) string { return smartStoredLast })
		h := newSmartTimeoutHandler(t, fast, stalled, map[string]any{"firstTokenTimeoutMs": 100})

		rec, elapsed := sendSmartTimeoutTurn(h, messages, stream, "sess-advance")

		out := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(out, "fast member") || strings.Contains(out, "stalled member") {
			t.Fatalf("want the next member's answer, got %d %s", rec.Code, out)
		}
		if elapsed >= smartStall/2 {
			t.Fatalf("turn took %v: the timeout did not cut the chosen member", elapsed)
		}
		if got, _ := router.lastModel.Load().(string); got != "jev-1.13-free" {
			t.Errorf("router evaluated with model %q, want the default jev-1.13-free", got)
		}
		if stalled.calls.Load() != 1 {
			t.Errorf("System One's choice was called %d times, want 1 (it must be tried first)", stalled.calls.Load())
		}
		if pin := h.getSmartStickyModel(smartTimeoutCombo, "sess-advance", []string{smartStoredFirst, smartStoredLast}); pin != "" {
			t.Errorf("timed-out choice kept the session pin %q", pin)
		}
		if locked, _ := h.Repo.IsConnectionModelLocked("mock-b", "qwen3-32b"); locked {
			t.Error("a response timeout locked the chosen member's account")
		}
		// The abandoned request must be hung up, not left generating tokens
		// the client will never see.
		select {
		case <-stalled.aborted:
		case <-time.After(smartStall / 2):
			t.Error("the timed-out member's upstream request was never aborted")
		}
	})
}

// A timer on the member System One moved to the front is only correct if
// "last" is read from the routed order: the stored order would spare the
// stalled choice and cut the member that can actually answer.
func TestSmartComboTimeoutSparesRoutedLastMember(t *testing.T) {
	forEachComboEndpoint(t, func(t *testing.T, messages, stream bool) {
		slow := newMemberStub(t, stream, 300*time.Millisecond, "routed-last member")
		stalled := newMemberStub(t, stream, smartStall, "stalled member")
		newJevRouterStub(t, 0, func(int32) string { return smartStoredLast })
		h := newSmartTimeoutHandler(t, slow, stalled, map[string]any{"firstTokenTimeoutMs": 100})

		rec, elapsed := sendSmartTimeoutTurn(h, messages, stream, "")

		out := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(out, "routed-last member") {
			t.Fatalf("routed-last member was cut short: %d %s", rec.Code, out)
		}
		if strings.Contains(out, "stalled member") || elapsed >= smartStall/2 {
			t.Fatalf("the routed-first choice was spared instead (%v): %s", elapsed, out)
		}
	})
}

// The candidate budget starts when the candidate does: a slow System One
// evaluation must not push the chosen member over its timeout.
func TestSmartComboTimeoutExcludesRouterEvaluation(t *testing.T) {
	forEachComboEndpoint(t, func(t *testing.T, messages, stream bool) {
		chosen := newMemberStub(t, stream, 50*time.Millisecond, "chosen member")
		other := newMemberStub(t, stream, 0, "other member")
		router := newJevRouterStub(t, 400*time.Millisecond, func(int32) string { return smartStoredFirst })
		h := newSmartTimeoutHandler(t, chosen, other, map[string]any{"firstTokenTimeoutMs": 200})

		rec, _ := sendSmartTimeoutTurn(h, messages, stream, "")

		if out := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(out, "chosen member") {
			t.Fatalf("chosen member did not answer: %d %s", rec.Code, out)
		}
		if router.calls.Load() != 1 {
			t.Fatalf("router evaluated %d times, want 1", router.calls.Load())
		}
		if other.calls.Load() != 0 {
			t.Fatal("router latency was charged to the chosen member's timeout")
		}
	})
}

// Within its grace period a timed-out member stays out of the chain even when
// System One keeps choosing it.
func TestSmartComboTimeoutBenchOverridesRouterChoice(t *testing.T) {
	fast := newMemberStub(t, false, 0, "fast member")
	stalled := newMemberStub(t, false, smartStall, "stalled member")
	newJevRouterStub(t, 0, func(int32) string { return smartStoredLast })
	h := newSmartTimeoutHandler(t, fast, stalled, map[string]any{"firstTokenTimeoutMs": 100})

	for turn := 1; turn <= 2; turn++ {
		rec, elapsed := sendSmartTimeoutTurn(h, false, false, "")
		if !strings.Contains(rec.Body.String(), "fast member") {
			t.Fatalf("turn %d not served: %d %s", turn, rec.Code, rec.Body.String())
		}
		if turn == 2 && elapsed >= 100*time.Millisecond {
			t.Errorf("turn 2 took %v: it waited on the benched member's timeout", elapsed)
		}
	}
	if got := stalled.calls.Load(); got != 1 {
		t.Fatalf("benched choice called %d times over two turns, want 1", got)
	}
}

// A strict-lock session pinned to a member that then times out must
// re-evaluate on its next turn instead of returning to the stalled member.
// Grace is off so only the pin, not the bench, can keep the member away.
func TestSmartComboTimeoutReleasesStrictPin(t *testing.T) {
	fast := newMemberStub(t, false, 0, "fast member")
	stalled := newMemberStub(t, false, smartStall, "stalled member")
	router := newJevRouterStub(t, 0, func(call int32) string {
		if call == 1 {
			return smartStoredLast
		}
		return smartStoredFirst
	})
	h := newSmartTimeoutHandler(t, fast, stalled, map[string]any{
		"firstTokenTimeoutMs": 100, "firstTokenGraceMs": 0,
		"smartPolicy": "strict", "sessionSticky": true, "confidenceThreshold": 0.8,
	})

	for turn := 1; turn <= 2; turn++ {
		rec, _ := sendSmartTimeoutTurn(h, false, false, "sess-strict")
		if !strings.Contains(rec.Body.String(), "fast member") {
			t.Fatalf("turn %d not served: %d %s", turn, rec.Code, rec.Body.String())
		}
	}
	if got := stalled.calls.Load(); got != 1 {
		t.Fatalf("stalled member called %d times: the strict pin survived its timeout", got)
	}
	if got := router.calls.Load(); got != 2 {
		t.Fatalf("router evaluated %d times, want 2 (turn 2 must re-evaluate)", got)
	}
	if pin := h.getSmartStickyModel(smartTimeoutCombo, "sess-strict", []string{smartStoredFirst, smartStoredLast}); pin != smartStoredFirst {
		t.Errorf("session pin = %q, want the re-evaluated %s", pin, smartStoredFirst)
	}
}
