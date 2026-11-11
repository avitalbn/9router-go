package media

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func setupSearchTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	database, cleanup := setupMultimodalTestDB(t)
	if err := db.EnsureCoreSchema(database); err != nil {
		cleanup()
		t.Fatalf("search schema: %v", err)
	}
	return database, cleanup
}

func seedSearchConn(t *testing.T, database *sql.DB, provider, id, apiKey string) {
	t.Helper()
	data, err := json.Marshal(struct {
		APIKey string `json:"apiKey"`
	}{apiKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id,provider,authType,name,priority,isActive,data,createdAt,updatedAt) VALUES (?,?,'apikey',?,1,1,?,'2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`, id, provider, provider+" Search Test", string(data)); err != nil {
		t.Fatal(err)
	}
}

func overrideProviderURL(t *testing.T, provider, searchURL string) {
	t.Helper()
	original := providers.KnownProviders[provider]
	custom := original
	custom.SearchURL = searchURL
	custom.BaseURL = searchURL
	providers.KnownProviders[provider] = custom
	t.Cleanup(func() { providers.KnownProviders[provider] = original })
}

func TestNativeSearch_VendorProtocolAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		provider, response, snippet, content string
		cost                                 float64
	}{
		{"tavily", `{"results":[{"title":"Tavily hit","url":"https://www.example.com/page?track=1","content":"Summary","raw_content":"Full text","score":1.5,"published_date":"2026-11-01"}]}`, "Summary", "Full text", 0.008},
		{"exa", `{"results":[{"title":"Exa hit","url":"https://www.example.com/page?track=1","highlights":["Highlight"],"text":"Full text","score":-0.1,"publishedDate":"2026-11-01","author":"Author","image":"https://example.com/image"}]}`, "Highlight", "Full text", 0.007},
		{"searxng", `{"results":[{"title":"SearXNG hit","url":"https://www.example.com/page?track=1","content":"Summary","engines":["google","bing"],"publishedDate":"2026-11-01"}]}`, "Summary", "", 0},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.provider == "searxng" {
					if r.Method != http.MethodGet || r.URL.Query().Get("q") != "AI news" || r.URL.Query().Get("categories") != "news" || r.URL.Query().Get("format") != "json" || r.Header.Get("Authorization") != "" {
						http.Error(w, "invalid SearXNG protocol", 400)
						return
					}
				} else {
					var request struct {
						Query      string `json:"query"`
						MaxResults int    `json:"max_results"`
						NumResults int    `json:"numResults"`
						Topic      string `json:"topic"`
						Category   string `json:"category"`
						Model      string `json:"model"`
						Text       bool   `json:"text"`
						Highlights bool   `json:"highlights"`
					}
					if err := json.UnmarshalRead(r.Body, &request); err != nil {
						t.Error(err)
						http.Error(w, "invalid JSON", 400)
						return
					}
					if r.Method != http.MethodPost || request.Query != "AI news" || request.Model != "" {
						http.Error(w, "invalid search protocol", 400)
						return
					}
					if tc.provider == "tavily" && (request.MaxResults != 1 || request.Topic != "news" || r.Header.Get("Authorization") != "Bearer test-key") {
						http.Error(w, "invalid Tavily protocol", 400)
						return
					}
					if tc.provider == "exa" && (request.NumResults != 1 || request.Category != "news" || !request.Text || !request.Highlights || r.Header.Get("x-api-key") != "test-key") {
						http.Error(w, "invalid Exa protocol", 400)
						return
					}
				}
				fmt.Fprint(w, tc.response)
			}))
			defer upstream.Close()
			database, cleanup := setupSearchTestDB(t)
			defer cleanup()
			if tc.provider != "searxng" {
				seedSearchConn(t, database, tc.provider, "conn-"+tc.provider, "test-key")
			}
			overrideProviderURL(t, tc.provider, upstream.URL+"/search")
			rec := httptest.NewRecorder()
			body := fmt.Sprintf(`{"model":%q,"query":"  AI   news ","search_type":"news","max_results":1}`, tc.provider)
			newTestMediaHandler(db.NewRepo(database)).HandleSearch(rec, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var response nativeSearchResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Provider != tc.provider || response.Query != "AI news" || len(response.Results) != 1 {
				t.Fatalf("wrong search envelope: %+v", response)
			}
			result := response.Results[0]
			if result.Snippet != tc.snippet || result.DisplayURL != "example.com/page" || result.Position != 1 || result.Citation.Provider != tc.provider || result.PublishedAt != "2026-11-01" {
				t.Fatalf("wrong normalized result: %+v", result)
			}
			if tc.content != "" {
				content := result.Content
				if content == nil || content.Text != tc.content || content.Format != "text" {
					t.Errorf("full text lost: %v", result.Content)
				}
			} else if result.Content != nil {
				t.Errorf("unexpected full text: %v", result.Content)
			}
			if response.Usage.SearchCostUSD != tc.cost {
				t.Errorf("cost %v, want %v", response.Usage.SearchCostUSD, tc.cost)
			}
			if tc.provider == "tavily" && result.Score != float64(1) {
				t.Errorf("Tavily score not clamped: %v", result.Score)
			}
			if tc.provider == "exa" && (result.Score != float64(0) || result.Metadata["author"] != "Author") {
				t.Errorf("Exa metadata lost: %+v", result)
			}
			if tc.provider == "searxng" && (result.Score != nil || result.Metadata["source_type"] != "google, bing") {
				t.Errorf("SearXNG metadata lost: %+v", result)
			}
		})
	}
}

func TestNativeSearch_ErrorsAndComboFallback(t *testing.T) {
	for _, tc := range []struct {
		name, model  string
		status, want int
	}{{"upstream bad request", "tavily", 400, 400}, {"upstream unauthorized", "exa", 401, 401}, {"combo fallback", "search-combo", 401, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":"vendor refused"}`)
			}))
			defer failed.Close()
			success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"results":[{"title":"Fallback hit","url":"https://example.com/fallback"}]}`)
			}))
			defer success.Close()
			database, cleanup := setupSearchTestDB(t)
			defer cleanup()
			seedSearchConn(t, database, "tavily", "tavily-conn", "test-key")
			seedSearchConn(t, database, "exa", "exa-conn", "test-key")
			overrideProviderURL(t, "tavily", failed.URL+"/search")
			exaURL := failed.URL
			if tc.model == "search-combo" {
				exaURL = success.URL
			}
			overrideProviderURL(t, "exa", exaURL+"/search")
			if _, err := database.Exec(`INSERT INTO combos (id,name,models,kind,createdAt,updatedAt) VALUES ('combo','search-combo','["tavily/search","exa/search"]','webSearch','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			newTestMediaHandler(db.NewRepo(database)).HandleSearch(rec, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(fmt.Sprintf(`{"model":%q,"query":"fallback"}`, tc.model))))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d, body %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 200 {
				var response nativeSearchResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Provider != "exa" || len(response.Results) != 1 || response.Results[0].Title != "Fallback hit" {
					t.Fatalf("wrong fallback response: %+v", response)
				}
			}
		})
	}
}
