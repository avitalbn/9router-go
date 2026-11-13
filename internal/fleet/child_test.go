package fleet

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("FLEET_FAKE_CHILD") == "1" {
		fakeGateway()
		return
	}
	os.Exit(m.Run())
}
func fakeGateway() {
	mux := http.NewServeMux()
	stop := make(chan struct{}, 1)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var v any
		switch r.URL.Path {
		case "/health":
			v = Object{"status": "ok"}
		case "/api/fleet/status":
			v = Object{"role": os.Getenv("FLEET_ROLE"), "runningCommit": strings.Repeat("a", 40), "activeRequests": 0}
		case "/api/settings":
			v = Object{}
		case "/v1/models":
			v = Object{"data": []any{}}
		case "/api/version/shutdown":
			v = Object{"ok": true}
			defer func() { stop <- struct{}{} }()
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>gateway</html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
	server := &http.Server{Addr: "127.0.0.1:" + os.Getenv("PORT"), Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.ListenAndServe() }()
	<-stop
	_ = server.Shutdown(context.Background())
}
func TestDeploymentRollbackRestartsLastGoodChildAndPreservesCredentials(t *testing.T) {
	src, source := fixture(t, "source")
	dst, target := fixture(t, "local")
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = source.Exec("UPDATE combos SET name='candidate'")
	s := exportTest(t, src)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := DeviceConfig{Root: root, DataDir: filepath.Dir(filepath.Dir(dst)), Role: Follower, Host: "127.0.0.1", Port: port, Environment: map[string]string{"FLEET_FAKE_CHILD": "1"}}
	for _, f := range []string{"machine-id", "auth/cli-secret"} {
		if err := WritePrivate(filepath.Join(c.DataDir, f), []byte("local-secret")); err != nil {
			t.Fatal(err)
		}
	}
	old := Active{Commit: strings.Repeat("a", 40), Gateway: exe}
	g := &agent{config: c, active: old}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	g.process, err = startChild(c, old)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stopChild(c, g.process) }()
	if err = waitHealthy(ctx, c, old); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(c.state("probation"))
	fingerprint, err := privateFingerprint(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	bad := Active{Commit: strings.Repeat("b", 40), Gateway: filepath.Join(root, "missing-binary")}
	if err = g.deploy(ctx, bad, s); err == nil {
		t.Fatal("failed candidate accepted")
	}
	after, err := privateFingerprint(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if after != fingerprint {
		t.Fatal("rollback lost credentials or local state")
	}
	restored := exportTest(t, dst)
	if restored.Tables["combos"][0]["name"] != "coding" {
		t.Fatal("rollback did not restore pre-candidate config")
	}
	if err = waitHealthy(ctx, c, old); err != nil {
		t.Fatal("last-good child was not restarted", err)
	}
	if _, err = os.Stat(c.state("pending.json")); !os.IsNotExist(err) {
		t.Fatal("recovery journal was not resolved")
	}
}
