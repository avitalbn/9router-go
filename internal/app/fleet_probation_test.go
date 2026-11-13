package app_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/app"
)

func TestFleetProbationModelProbeThroughServerMiddleware(t *testing.T) {
	repo := openWiringRepo(t)
	const probeKey = "fleet-local-probe-key"
	if err := repo.CreateApiKey("fleet-probe", probeKey, "probation probe", "test-machine"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_ROLE", "authority")
	root := t.TempDir()
	t.Setenv("FLEET_STATE_DIR", root)
	for _, name := range []string{"probation", "drain"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	router, _ := app.ProvideRouter(repo, app.ProvideTokenSaverConfig(repo, app.CLIParams{}))
	for _, path := range []string{"/models", "/v1/models", "/v1/v1/models"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+probeKey)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"data"`) {
			t.Fatalf("probation models probe %s: HTTP %d, body %s", path, rec.Code, rec.Body.String())
		}
	}
	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("probation bypassed model authentication: HTTP %d", unauthenticated.Code)
	}
	// The normalized inference route must remain gated, even with a drain
	// marker present; allowing models cannot release ordinary traffic.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("probation admitted inference: HTTP %d", rec.Code)
	}
}
