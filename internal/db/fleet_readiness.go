package db

import (
	"9router/proxy/internal/fleetmode"
	"9router/proxy/internal/models"
	"encoding/json"
)

func applyFleetReadiness(conn *models.ProviderConnection) {
	if fleetmode.Role() != "follower" || fleetmode.CredentialReady(conn.AuthType, conn.Data) {
		return
	}
	conn.IsActive = 0
	var m map[string]any
	if json.Unmarshal([]byte(conn.Data), &m) != nil || m == nil {
		return
	}
	m["fleetAuthenticationRequired"] = true
	m["testStatus"] = "reauth_required"
	m["lastError"] = "Authenticate this connection on this device; fleet credentials stay local."
	if b, err := json.Marshal(m); err == nil {
		conn.Data = string(b)
	}
}
