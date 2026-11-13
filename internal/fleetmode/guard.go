package fleetmode

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func drainTracked(r *http.Request) bool {
	// Provider quota/catalog GETs can refresh credentials too. Track them
	// along with inference; exclude only local probes, assets and read-only
	// dashboard SSE streams that can safely close during maintenance.
	switch r.URL.Path {
	case "/", "/health", "/api/health", "/api/version", "/api/fleet/status", "/api/version/shutdown", "/api/usage/stream", "/usage/stream", "/api/console/logs/stream":
		return false
	}
	for _, prefix := range []string{"/assets/", "/providers/", "/fonts/"} {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return false
		}
	}
	return true
}
func blockedMutation(r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
		return false
	}
	p := strings.TrimSuffix(r.URL.Path, "/")
	if p == "/api/version/update" || p == "/api/version/auto-update" {
		return true
	}
	if Role() != "follower" {
		return false
	}
	if p == "/api/settings" {
		return !localSettingsWrite(r)
	}
	for _, prefix := range []string{"/api/combos", "/api/provider-nodes", "/api/proxy-pools", "/api/models", "/api/model-aliases", "/api/custom-models", "/api/disabled-models", "/api/pricing", "/api/mitm/aliases", "/api/settings/database"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	if strings.HasPrefix(p, "/api/connections") || strings.HasPrefix(p, "/api/providers") {
		if strings.HasSuffix(p, "/test") || strings.HasSuffix(p, "/renew-deepseek") || strings.Contains(p, "/browser-login") || strings.Contains(p, "/import-chrome") {
			return false
		}
		return !(r.Method == "PUT" && credentialWrite(r))
	}
	return false
}
func readObject(r *http.Request) (map[string]any, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err == nil && m != nil
}
func localSettingsWrite(r *http.Request) bool {
	m, ok := readObject(r)
	if !ok {
		return false
	}
	for k := range m {
		switch k {
		case "currentPassword", "newPassword", "requireApiKey", "retentionDays", "maxRequestRecords", "headroomEnabled", "headroomUrl", "tailscaleEnabled", "tailscaleUrl", "tunnelEnabled", "tunnelUrl", "tunnelDashboardAccess", "navVisibility", "displayCurrency", "animationLevel":
		default:
			return false
		}
	}
	return true
}
func credentialWrite(r *http.Request) bool {
	m, ok := readObject(r)
	if !ok {
		return false
	}
	for k, v := range m {
		switch k {
		case "apiKey", "accessToken", "refreshToken", "idToken", "expiresAt", "expiresIn", "scope", "tokenType":
		case "providerSpecificData":
			nested, ok := v.(map[string]any)
			if !ok {
				return false
			}
			for field := range nested {
				switch field {
				case "authMethod", "chatgptAccountId", "chatgptPlanType", "copilotToken", "copilotTokenExpiresAt", "email", "githubEmail", "githubLogin", "githubName", "githubUserId", "organizationId", "projectId", "systemId", "userId":
				default:
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// CredentialReady is evaluated after vault hydration. Desired enabled state
// stays in SQLite; only this process's effective scheduling state is changed.
func CredentialReady(authType, data string) bool {
	if authType == "none" {
		return true
	}
	var m map[string]any
	if json.Unmarshal([]byte(data), &m) != nil {
		return false
	}
	for _, k := range []string{"apiKey", "accessToken", "refreshToken", "cookie", "sessionToken"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}
