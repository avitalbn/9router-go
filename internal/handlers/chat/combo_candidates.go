package chat

import (
	"net/http"
	"slices"
	"strings"
)

// comboConnFailures keeps why combo members got no connection, so a combo
// none of whose members could be dispatched names the reason instead of an
// anonymous "no valid entries".
type comboConnFailures []string

func (f *comboConnFailures) note(err error) {
	if msg := err.Error(); !slices.Contains(*f, msg) {
		*f = append(*f, msg)
	}
}

// terminalError is the error a combo answers with when no member could be
// dispatched at all; nil when every member at least had a connection.
func (f comboConnFailures) terminalError() *upstreamError {
	if len(f) == 0 {
		return nil
	}
	return &upstreamError{StatusCode: http.StatusBadGateway, Body: upstreamErrorBody("all combo models failed: " + strings.Join(f, "; "))}
}
