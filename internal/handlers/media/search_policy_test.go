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

func TestNativeSearch_RespectsUpstreamMediaPool(t *testing.T) {
	for _, provider := range []string{"tavily", "exa", "searxng"} {
		for _, policy := range []struct{ name, data string }{
			{"media disabled", `{"apiKey":"test-key","mediaEnabled":false}`},
			{"search kind disabled", `{"apiKey":"test-key","providerSpecificData":{"disabledKinds":["webSearch"]}}`},
		} {
			t.Run(provider+"/"+policy.name, func(t *testing.T) {
				calls := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{"results":[]}`) }))
				defer upstream.Close()
				database, cleanup := setupSearchTestDB(t)
				defer cleanup()
				seedSearchConn(t, database, provider, "disabled", "test-key")
				if _, err := database.Exec(`UPDATE providerConnections SET data=? WHERE id='disabled'`, policy.data); err != nil {
					t.Fatal(err)
				}
				overrideProviderURL(t, provider, upstream.URL+"/search")
				request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(fmt.Sprintf(`{"model":%q,"query":"pool policy"}`, provider)))
				request.Header.Set("x-connection-id", "disabled")
				rec := httptest.NewRecorder()
				newTestMediaHandler(db.NewRepo(database)).HandleSearch(rec, request)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("disabled search pool status = %d, body %s", rec.Code, rec.Body.String())
				}
				if calls != 0 {
					t.Fatalf("disabled pool made %d upstream calls", calls)
				}
			})
		}
	}
}

func TestNativeSearch_PreservesAliasAndComboPrecedence(t *testing.T) {
	for _, kind := range []string{"alias", "combo"} {
		t.Run(kind, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"results":[{"title":"Exa target","url":"https://example.com/target"}]}`)
			}))
			defer upstream.Close()
			database, cleanup := setupSearchTestDB(t)
			defer cleanup()
			seedSearchConn(t, database, "exa", "exa-target", "test-key")
			overrideProviderURL(t, "exa", upstream.URL+"/search")
			var err error
			if kind == "alias" {
				_, err = database.Exec(`INSERT INTO kv (scope,key,value) VALUES ('modelAliases','tavily','"exa/search"')`)
			} else {
				// Upstream, not this repair, normalizes bare web-combo seats.
				_, err = database.Exec(`INSERT INTO combos (id,name,kind,models,createdAt,updatedAt) VALUES ('combo-tavily','tavily','webSearch','["exa"]','2026-11-11T00:00:00Z','2026-11-11T00:00:00Z')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			newTestMediaHandler(db.NewRepo(database)).HandleSearch(rec, httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"model":"tavily","query":"precedence"}`)))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s routing status %d: %s", kind, rec.Code, rec.Body.String())
			}
			var response nativeSearchResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Provider != "exa" || len(response.Results) != 1 || response.Results[0].Title != "Exa target" {
				t.Fatalf("%s target was replaced by bare-provider routing: %+v", kind, response)
			}
		})
	}
}
