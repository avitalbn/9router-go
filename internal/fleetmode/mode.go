// Package fleetmode contains the gateway's explicit managed-mode controls.
// An unset FLEET_ROLE leaves standalone behavior unchanged.
package fleetmode

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

var active atomic.Int64
var admission sync.Mutex

func Role() string    { return os.Getenv("FLEET_ROLE") }
func Enabled() bool   { return Role() == "authority" || Role() == "follower" }
func Probation() bool { return Enabled() && marked("probation") }
func marked(name string) bool {
	root := os.Getenv("FLEET_STATE_DIR")
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
func AwaitProbation(ctx context.Context) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for Enabled() && marked("probation") {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
	return ctx.Err() == nil
}
func Revision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "unknown"
}
func Status(w http.ResponseWriter, r *http.Request) {
	admission.Lock()
	count := active.Load()
	admission.Unlock()
	value := map[string]any{"role": Role(), "runningCommit": Revision(), "authorityUrl": os.Getenv("FLEET_AUTHORITY_URL"), "activeRequests": count, "probation": marked("probation"), "draining": marked("drain")}
	if root := os.Getenv("FLEET_STATE_DIR"); root != "" {
		// status.json is written by the local helper; it never contains secrets.
		if b, err := os.ReadFile(filepath.Join(root, "status.json")); err == nil {
			var state map[string]any
			if json.Unmarshal(b, &state) == nil {
				value["sync"] = state
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func Guard(next http.Handler) http.Handler {
	if !Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probing := marked("probation")
		if probing && !probationRead(r) {
			reject(w, http.StatusServiceUnavailable, "Gateway is validating a fleet deployment")
			return
		}
		if blockedMutation(r) {
			reject(w, http.StatusConflict, "Configuration is managed by the fleet authority; edit it on the authority dashboard")
			return
		}
		if !probing && drainTracked(r) {
			admission.Lock()
			if marked("drain") {
				admission.Unlock()
				reject(w, http.StatusServiceUnavailable, "Gateway is draining for a fleet update")
				return
			}
			active.Add(1)
			admission.Unlock()
			defer active.Add(-1)
		}
		next.ServeHTTP(w, r)
	})
}
func reject(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func probationRead(r *http.Request) bool {
	if r.Method != "GET" {
		return false
	}
	switch r.URL.Path {
	case "/", "/health", "/api/health", "/api/version", "/api/settings", "/api/fleet/status", "/v1/models":
		return true
	}
	return false
}
