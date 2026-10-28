package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The dashboard warns on a combo card when the Smart Router model's provider
// has no credentials. It mirrors smart_router.go querySystemOneRouter, which
// proceeds without a connection only for registry providers with NoAuth or a
// DefaultAPIKey. That list is copied into routerCredentials.ts; this test
// fails when the registry and the copy drift apart.
func TestRouterCredentialFreeProvidersMatchDashboard(t *testing.T) {
	var want []string
	for id, cfg := range KnownProviders {
		if cfg.NoAuth || cfg.DefaultAPIKey != "" {
			want = append(want, id)
		}
	}
	sort.Strings(want)

	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "components", "combos", "routerCredentials.ts"))
	if err != nil {
		t.Fatalf("read routerCredentials.ts: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "// credential-free:start")
	end := strings.Index(text, "// credential-free:end")
	if start < 0 || end < start {
		t.Fatalf("credential-free markers missing in routerCredentials.ts")
	}
	var got []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(text[start:end], -1) {
		got = append(got, m[1])
	}
	sort.Strings(got)

	if !slices.Equal(want, got) {
		t.Fatalf("REGISTRY_CREDENTIAL_FREE_PROVIDERS drifted from providers.KnownProviders (NoAuth || DefaultAPIKey)\nregistry:  %v\ndashboard: %v", want, got)
	}
}
