package chat

import (
	"9router/proxy/internal/db"
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAttemptDiagnosticsClassifyFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		reason attemptReason
	}{
		{"successful combo", nil, 200, attemptSuccess},
		{"monthly quota", &upstreamError{StatusCode: 402}, 402, attemptQuota},
		{"rate limit", &upstreamError{StatusCode: 429}, 429, attemptRateLimit},
		{"invalid credential", &upstreamError{StatusCode: 401}, 401, attemptAuth},
		{"deadline", context.DeadlineExceeded, 504, attemptTimeout},
		{"provider failure", errors.New("connection refused"), 500, attemptFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newFallbackAttempt(context.Background(), "c", "a", "p", "m", 0, tt.err, 10)
			if got.StatusCode != tt.status || got.Reason != tt.reason {
				t.Fatalf("diagnostic = %+v, want %d/%s", got, tt.status, tt.reason)
			}
			if _, err := time.Parse(time.RFC3339Nano, got.StartedAt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAttemptDiagnosticsDistinguishTimeoutFromClientAbort(t *testing.T) {
	tests := []struct {
		name   string
		expire bool
		status int
		reason attemptReason
	}{
		{"combo response timer", true, 504, attemptTimeout},
		{"client disconnected", false, 499, attemptCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate := newComboResponseTimeout(parent, httptest.NewRecorder(), time.Hour)
			defer gate.finish()
			if tt.expire {
				gate.expire()
			} else {
				cancel()
			}
			got := newFallbackAttempt(gate.ctx, "c", "a", "p", "m", 0, context.Canceled, 10)
			if got.StatusCode != tt.status || got.Reason != tt.reason {
				t.Fatalf("diagnostic = %+v, want %d/%s", got, tt.status, tt.reason)
			}
		})
	}
}

func TestDetailTrailIncludesCurrentWithoutMutatingRecorder(t *testing.T) {
	rec := NewFallbackAttemptRecorder()
	ctx := WithFallbackAttemptRecorder(context.Background(), rec)
	RecordAttemptFromContext(ctx, "c1", "a1", "p1", "m1", 402, errors.New("quota"), 10)
	got := detailAttemptTrail(ctx, &UsageLogInfo{Provider: "p2", Model: "m2", ConnectionID: "c2"}, 200, nil, 20)
	if len(got) != 2 || got[1].Model != "m2" || got[1].StatusCode != 200 {
		t.Fatalf("trail omits current success: %+v", got)
	}
	if len(rec.Snapshot()) != 1 {
		t.Fatal("persisting row mutated caller's recorder")
	}
}

// StartedAt arrives in the process timezone; a literal Z must never label it
// as UTC without converting, or the dashboard can show a start after the end.
func TestRequestDetailStartTimeUsesUTC(t *testing.T) {
	tests := []struct {
		name    string
		failure bool
	}{{"success", false}, {"failure", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			handler := NewChatHandler(db.NewRepo(database))
			start := time.Now().In(time.FixedZone("UTC+2", 2*60*60)).Add(-time.Second)
			info := &UsageLogInfo{Provider: "deepseek", Model: "deepseek-chat", StartedAt: start}
			if tt.failure {
				handler.LogFailure(context.Background(), info, nil, errors.New("failed"), 1000, nil, nil)
			} else {
				handler.LogUsage(context.Background(), info, nil, 1000, nil, nil)
			}
			var raw string
			if err := database.QueryRow("SELECT data FROM requestDetails LIMIT 1").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				StartedAt string `json:"startedAt"`
				EndedAt   string `json:"endedAt"`
			}
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			got, err := time.Parse(time.RFC3339Nano, payload.StartedAt)
			if err != nil {
				t.Fatal(err)
			}
			if delta := got.Sub(start); delta < -time.Millisecond || delta > time.Millisecond {
				t.Fatalf("stored start %s is not instant %s", payload.StartedAt, start)
			}
		})
	}
}
