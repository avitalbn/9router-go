package media

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

func TestNativeSearch_BareProviderRoutingAndResultLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"bare model without a connection", `{"model":"searxng","query":"routing","max_results":1}`},
		{"provider alias without a model", `{"provider":"searxng","query":"routing","max_results":1}`},
		{"explicit search lane", `{"model":"searxng/search","query":"routing","max_results":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Query().Get("q") != "routing" || r.Header.Get("Authorization") != "" {
					http.Error(w, "invalid SearXNG request", http.StatusBadRequest)
					return
				}
				fmt.Fprint(w, `{"results":[{"title":"First hit","url":"https://example.com/first"},{"title":"Second hit","url":"https://example.com/second"}]}`)
			}))
			defer upstream.Close()
			database, cleanup := setupSearchTestDB(t)
			defer cleanup()
			overrideProviderURL(t, "searxng", upstream.URL)
			handler := newTestMediaHandler(db.NewRepo(database))
			rec := httptest.NewRecorder()
			handler.HandleSearch(rec, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var response nativeSearchResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Provider != "searxng" || len(response.Results) != 1 || response.Results[0].Title != "First hit" || response.Results[0].URL != "https://example.com/first" {
				t.Fatalf("provider or result limit changed: %+v", response)
			}
			if response.Metrics.TotalResultsAvailable != float64(2) {
				t.Errorf("available results = %v, want 2", response.Metrics.TotalResultsAvailable)
			}
		})
	}
}

func TestNativeSearch_ProviderResultBoundaries(t *testing.T) {
	for _, tc := range []struct {
		provider string
		limit    int
	}{{"tavily", 20}, {"exa", 100}, {"searxng", 50}} {
		t.Run(tc.provider, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"results":[`)
				for i := range 125 {
					if i != 0 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, `{"title":"Hit %d","url":"https://example.com/%d"}`, i, i)
				}
				fmt.Fprint(w, "]}")
			}))
			defer upstream.Close()
			database, cleanup := setupSearchTestDB(t)
			defer cleanup()
			if tc.provider != "searxng" {
				seedSearchConn(t, database, tc.provider, "conn-"+tc.provider, "test-key")
			}
			overrideProviderURL(t, tc.provider, upstream.URL+"/search")
			handler := newTestMediaHandler(db.NewRepo(database))
			rec := httptest.NewRecorder()
			body := fmt.Sprintf(`{"model":%q,"query":"limits","max_results":999}`, tc.provider)
			handler.HandleSearch(rec, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var response nativeSearchResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != tc.limit || response.Results[tc.limit-1].Title != fmt.Sprintf("Hit %d", tc.limit-1) {
				t.Fatalf("result cap = %d, want first %d hits", len(response.Results), tc.limit)
			}
			if response.Results[0].Score != nil {
				t.Errorf("missing vendor score must be null, got %v", response.Results[0].Score)
			}
		})
	}
}

func TestNativeSearch_SearxngInstanceOverride(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("q") != "instance" {
			http.Error(w, "wrong instance request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"results":[{"title":"Instance hit","url":"https://example.com/instance"}]}`)
	}))
	defer upstream.Close()
	database, cleanup := setupSearchTestDB(t)
	defer cleanup()
	handler := newTestMediaHandler(db.NewRepo(database))
	body := fmt.Sprintf(`{"provider":"searxng","query":"instance","provider_options":{"baseUrl":%q}}`, upstream.URL)
	rec := httptest.NewRecorder()
	handler.HandleSearch(rec, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("instance override status = %d, body %s", rec.Code, rec.Body.String())
	}
	var response nativeSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Title != "Instance hit" {
		t.Fatalf("wrong instance result: %+v", response.Results)
	}
}
