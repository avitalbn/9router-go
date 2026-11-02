package chat

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"
)

const maxFallbackAttempts = 64

type FallbackAttempt struct {
	ConnectionID string        `json:"connectionId"`
	Account      string        `json:"account"`
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	StatusCode   int           `json:"statusCode"`
	Error        string        `json:"error,omitempty"`
	TimeoutMs    int64         `json:"timeoutMs,omitempty"`
	LatencyMs    int64         `json:"latencyMs"`
	StartedAt    string        `json:"startedAt,omitempty"`
	Skipped      bool          `json:"skipped,omitempty"`
	Reason       attemptReason `json:"reason,omitempty"`
}

type FallbackAttemptRecorder struct {
	mu       sync.Mutex
	attempts []FallbackAttempt
}

func NewFallbackAttemptRecorder() *FallbackAttemptRecorder {
	return &FallbackAttemptRecorder{
		attempts: make([]FallbackAttempt, 0, 10),
	}
}

func (r *FallbackAttemptRecorder) Record(attempt FallbackAttempt) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) >= maxFallbackAttempts {
		return
	}
	r.attempts = append(r.attempts, attempt)
}

func (r *FallbackAttemptRecorder) Snapshot() []FallbackAttempt {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) == 0 {
		return nil
	}
	out := make([]FallbackAttempt, len(r.attempts))
	copy(out, r.attempts)
	return out
}

type fallbackAttemptRecorderContextKey struct{}

func WithFallbackAttemptRecorder(ctx context.Context, recorder *FallbackAttemptRecorder) context.Context {
	return context.WithValue(ctx, fallbackAttemptRecorderContextKey{}, recorder)
}

func FallbackAttemptRecorderFrom(ctx context.Context) *FallbackAttemptRecorder {
	if ctx == nil {
		return nil
	}
	rec, _ := ctx.Value(fallbackAttemptRecorderContextKey{}).(*FallbackAttemptRecorder)
	return rec
}

func RecordAttemptFromContext(ctx context.Context, connID, account, provider, model string, statusCode int, err error, latencyMs int64) {
	recorder := FallbackAttemptRecorderFrom(ctx)
	if recorder == nil {
		return
	}

	attempt := newFallbackAttempt(ctx, connID, account, provider, model, statusCode, err, latencyMs)

	recorder.Record(attempt)
}

func upstreamStatusCode(err error) (int, bool) {
	for err != nil {
		value := reflect.ValueOf(err)
		if value.Kind() == reflect.Pointer && !value.IsNil() {
			value = value.Elem()
		}
		if value.Kind() == reflect.Struct {
			if field := value.FieldByName("StatusCode"); field.IsValid() && field.Kind() == reflect.Int {
				return int(field.Int()), true
			}
		}

		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapper.Unwrap()
	}
	return 0, false
}

// newFallbackAttempt keeps stored diagnostics consistent across combo and account routing.
func newFallbackAttempt(ctx context.Context, connID, account, provider, model string, status int, err error, latency int64) FallbackAttempt {
	status, reason := classifyAttempt(ctx, status, err)
	attempt := FallbackAttempt{ConnectionID: connID, Account: account, Provider: provider, Model: model,
		StatusCode: status, Reason: reason, LatencyMs: latency,
		TimeoutMs: candidateTimeoutFrom(ctx).Milliseconds(),
		StartedAt: time.Now().UTC().Add(-time.Duration(latency) * time.Millisecond).Format(time.RFC3339Nano)}
	if err != nil {
		message := err.Error()
		var ue *upstreamError
		if errors.As(err, &ue) && len(ue.Body) > 0 {
			message = extractErrorText(ue.Body)
		}
		if reason == attemptTimeout {
			message = "response timeout: " + message
		}
		attempt.Error = truncateDetailText(message, maxPersistedErrorLen, "...[truncated]")
	}
	return attempt
}

type attemptReason string

const (
	attemptUnavailable attemptReason = "unavailable"
	attemptSuccess     attemptReason = "success"
	attemptTimeout     attemptReason = "timeout"
	attemptCanceled    attemptReason = "client_canceled"
	attemptQuota       attemptReason = "quota_exceeded"
	attemptRateLimit   attemptReason = "rate_limited"
	attemptAuth        attemptReason = "authentication"
	attemptFailure     attemptReason = "upstream_error"
)

func classifyAttempt(ctx context.Context, status int, err error) (int, attemptReason) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, attemptTimeout
	}
	if isClientCanceled(ctx, err) {
		return StatusClientClosedRequest, attemptCanceled
	}
	if status == 0 {
		status = http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
			if code, ok := upstreamStatusCode(err); ok {
				status = code
			}
		}
	}
	switch status {
	case http.StatusPaymentRequired:
		return status, attemptQuota
	case http.StatusTooManyRequests:
		return status, attemptRateLimit
	case http.StatusUnauthorized, http.StatusForbidden:
		return status, attemptAuth
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return status, attemptTimeout
	}
	if err == nil && status >= 200 && status < 300 {
		return status, attemptSuccess
	}
	return status, attemptFailure
}

// detailAttemptTrail includes the current attempt before its caller records it.
// Do not mutate the recorder here: the caller still owns account rotation and
// may classify a stream as empty after forwarding has returned.
func detailAttemptTrail(ctx context.Context, info *UsageLogInfo, status int, err error, latency int64) []FallbackAttempt {
	rec := FallbackAttemptRecorderFrom(ctx)
	if rec == nil {
		return nil
	}
	trail := rec.Snapshot()
	if info.Provider == "" {
		return trail
	}
	current := newFallbackAttempt(ctx, info.ConnectionID, info.AccountLabel(), info.Provider, info.Model, status, err, latency)
	if len(trail) >= maxFallbackAttempts {
		trail = trail[:maxFallbackAttempts-1]
	}
	return append(trail, current)
}

// recordSkippedCandidate explains a leg that never made an HTTP request.
func recordSkippedCandidate(ctx context.Context, provider, model, conn string, err error) {
	rec := FallbackAttemptRecorderFrom(ctx)
	if rec == nil {
		return
	}
	a := newFallbackAttempt(ctx, conn, "", provider, model, 0, err, 0)
	a.Skipped = true
	a.Reason = attemptUnavailable
	a.StatusCode = 0
	rec.Record(a)
}
