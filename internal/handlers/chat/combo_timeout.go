package chat

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// comboResponseTimeout serializes timeout expiry with response commitment. A
// canceled candidate can never write into the next candidate's response. Once
// committed, the timer is disarmed without canceling the live upstream stream.
type comboResponseTimeout struct {
	http.ResponseWriter
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	timer     *time.Timer
	committed bool
	expired   bool
	header    http.Header
}

func newComboResponseTimeout(ctx context.Context, w http.ResponseWriter, timeout time.Duration) *comboResponseTimeout {
	ctx, cancel := context.WithCancel(ctx)
	t := &comboResponseTimeout{ResponseWriter: w, ctx: ctx, cancel: cancel, header: make(http.Header)}
	t.timer = time.AfterFunc(timeout, t.expire)
	return t
}

func (t *comboResponseTimeout) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.committed {
		t.expired = true
		t.cancel()
	}
}

func (t *comboResponseTimeout) Header() http.Header {
	return t.header
}

func (t *comboResponseTimeout) commitLocked() bool {
	if t.expired {
		return false
	}
	if !t.committed {
		for key, values := range t.header {
			t.ResponseWriter.Header()[key] = values
		}
	}
	t.committed = true
	t.timer.Stop()
	return true
}

func (t *comboResponseTimeout) WriteHeader(code int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.commitLocked() {
		t.ResponseWriter.WriteHeader(code)
	}
}

func (t *comboResponseTimeout) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.commitLocked() {
		return 0, context.DeadlineExceeded
	}
	return t.ResponseWriter.Write(b)
}

func (t *comboResponseTimeout) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Flush alone is not usable output and must not disarm the timer.
	if t.committed {
		if f, ok := t.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (t *comboResponseTimeout) finish() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer.Stop()
	t.cancel()
	return t.expired
}

func (t *comboResponseTimeout) timedOut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expired
}

func (h *ChatHandler) comboResponseTimeout(comboName string) time.Duration {
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return 0
	}
	return time.Duration(settings.ComboStrategies[comboName].FirstTokenTimeoutMs) * time.Millisecond
}

func comboTimeoutError() *upstreamError {
	return &upstreamError{StatusCode: http.StatusGatewayTimeout, Body: []byte(`{"error":{"message":"model response timeout","type":"timeout_error","code":504}}`)}
}
