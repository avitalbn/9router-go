package chat

import (
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFleetProbationDoesNotDiscoverOrRefreshLiveCatalog(t *testing.T) {
	t.Setenv("FLEET_ROLE", "authority")
	root := t.TempDir()
	t.Setenv("FLEET_STATE_DIR", root)
	if err := os.WriteFile(filepath.Join(root, "probation"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// No Repo or HTTP client: entering discovery would panic or perform I/O.
	h := &ChatHandler{}
	conn := &models.ProviderConnection{ID: "local", Data: `{"refreshToken":"must-stay-local"}`}
	cd := &shared.ConnectionData{AccessToken: "local"}
	if got := h.resolveLiveCatalog(context.Background(), conn, cd, "kiro"); got != nil {
		t.Fatal("probation discovered live catalog")
	}
}
