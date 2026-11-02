package chat

import (
	"context"
	"time"
)

// A combo whose every leg is unavailable never enters the forwarder, so it
// needs its own failure row. Dispatched legs already write their own rows.
func (h *ChatHandler) logUndispatchedCombo(ctx context.Context, combo string, body []byte, err *upstreamError, started time.Time) {
	rec := FallbackAttemptRecorderFrom(ctx)
	if rec == nil {
		return
	}
	trail := rec.Snapshot()
	if len(trail) == 0 {
		return
	}
	for _, a := range trail {
		if !a.Skipped {
			return
		}
	}
	if err == nil {
		err = &upstreamError{StatusCode: 502, Body: upstreamErrorBody("all combo models failed: no valid entries")}
	}
	h.LogFailure(ctx, &UsageLogInfo{Model: combo, RequestedModel: combo, ComboName: combo, StartedAt: started, Endpoint: forwardEndpoint(ctx, "/v1/chat/completions")}, nil, err, time.Since(started).Milliseconds(), body, nil)
}
