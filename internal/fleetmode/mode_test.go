package fleetmode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedMutationBoundaries(t *testing.T) {
	t.Setenv("FLEET_ROLE", "follower")
	t.Setenv("FLEET_STATE_DIR", t.TempDir())
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/combos", "{}", 409}, {"DELETE", "/api/proxy-pools/p", "", 409},
		{"PUT", "/api/settings", `{"providerThinking":{"p":"high"}}`, 409},
		{"PUT", "/api/settings", `{"newPassword":"local","currentPassword":"old"}`, 200},
		{"PUT", "/api/connections/id", `{"apiKey":"local-key"}`, 200},
		{"PUT", "/api/connections/id", `{"providerSpecificData":{"enabledModels":["changed"]}}`, 409},
		{"POST", "/api/version/update", "{}", 409}, {"POST", "/v1/chat/completions", "{}", 200},
		{"POST", "/api/oauth/codex/refresh", "{}", 200}, {"GET", "/api/combos", "", 200},
	} {
		t.Run(test.path+test.body, func(t *testing.T) {
			h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
			if w.Code != test.status {
				t.Fatalf("got %d want %d", w.Code, test.status)
			}
		})
	}
}
func TestProbationBlocksTrafficAndBackgroundWork(t *testing.T) {
	t.Setenv("FLEET_ROLE", "authority")
	root := t.TempDir()
	t.Setenv("FLEET_STATE_DIR", root)
	if err := os.WriteFile(filepath.Join(root, "probation"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan bool, 1)
	go func() { ready <- AwaitProbation(ctx) }()
	select {
	case <-ready:
		t.Fatal("refresh gate opened before probation completed")
	case <-time.After(30 * time.Millisecond):
	}
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, path := range []string{"/v1/chat/completions", "/api/oauth/codex/refresh"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 503 {
			t.Fatal("probation allowed state-changing request")
		}
	}
	if err := os.Remove(filepath.Join(root, "probation")); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("gate cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("gate never opened")
	}
}
func TestCredentialReadiness(t *testing.T) {
	if CredentialReady("oauth", `{"accessToken":""}`) {
		t.Fatal("empty oauth session accepted")
	}
	if !CredentialReady("oauth", `{"refreshToken":"local"}`) {
		t.Fatal("existing refresh session excluded")
	}
	if !CredentialReady("none", "{}") {
		t.Fatal("keyless provider excluded")
	}
}

func TestProbationReadsRemainAvailableWhileDrainMarkerExists(t *testing.T) {
	t.Setenv("FLEET_ROLE", "follower")
	root := t.TempDir()
	t.Setenv("FLEET_STATE_DIR", root)
	for _, name := range []string{"probation", "drain"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, path := range []string{"/health", "/api/settings", "/v1/models", "/api/fleet/status"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("probation probe %s blocked by old drain marker", path)
		}
	}
	if err := os.Remove(filepath.Join(root, "probation")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 503 {
		t.Fatal("drain admitted a catalog request capable of token refresh")
	}
}
