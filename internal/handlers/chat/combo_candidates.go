package chat

import (
	"net/http"
	"slices"
	"strings"

	"9router/proxy/internal/providers"
)

// comboEntryTriable reports whether the combo loop would dispatch entry right
// now: it resolves, its provider/model is not locked, and a connection could
// be picked for it. It mirrors the loop's own skips and reads state only —
// getBestConnectionWithContext cannot be reused here because it advances the
// connection rotation.
func (h *ChatHandler) comboEntryTriable(entry string, excludeIDs []string) bool {
	info := h.resolveModelEntry(entry)
	if info == nil || !h.Repo.IsProviderAvailable(info.Provider, info.Model) {
		return false
	}
	if cfg, ok := providers.KnownProviders[info.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
		return true
	}
	conns, err := h.Repo.GetProviderConnections(info.Provider, true)
	if err == nil && len(conns) == 0 {
		if fallback, ok := CredentialFallbacks[info.Provider]; ok {
			conns, err = h.Repo.GetProviderConnections(fallback, true)
		}
	}
	if err != nil {
		return false
	}
	return slices.ContainsFunc(conns, func(c *ProviderConnection) bool {
		ineligible, _ := h.pinnedConnectionIneligible(c, excludeIDs, info.Model)
		return !ineligible
	})
}

// comboIsLastTriable reports whether models[i] is the last entry the loop can
// still dispatch. "Last" decides whether a candidate keeps the global deadline
// and passes an empty answer through, so it has to name the member actually
// tried: an index check also counts a final entry the loop is about to skip
// (#776), and a response timeout then cuts off the only remaining attempt.
func (h *ChatHandler) comboIsLastTriable(models []string, i int, excludeIDs []string) bool {
	for _, later := range models[i+1:] {
		if h.comboEntryTriable(later, excludeIDs) {
			return false
		}
	}
	return true
}

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
