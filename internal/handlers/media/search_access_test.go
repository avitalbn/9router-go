package media

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
)

func TestNativeSearch_PreservesUpstreamModelAccess(t *testing.T) {
	for _, tc := range []struct {
		name, allowed, body string
		status              int
	}{
		{"literal model permission", "tavily", `{"model":"tavily","query":"access"}`, http.StatusOK},
		{"literal provider permission", "tavily", `{"provider":"tavily","query":"access"}`, http.StatusOK},
		{"qualified seat permission", "tavily/search", `{"model":"tavily/search","query":"access"}`, http.StatusOK},
		{"different provider remains denied", "exa/search", `{"model":"tavily","query":"access"}`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprint(w, `{"results":[{"title":"Permitted result","url":"https://example.com/access"}]}`)
			}))
			defer upstream.Close()
			database, cleanup := setupSearchTestDB(t)
			defer cleanup()
			repo := db.NewRepo(database)
			if err := repo.SetAllowedModels("1", []string{tc.allowed}); err != nil {
				t.Fatal(err)
			}
			seedSearchConn(t, database, "tavily", "tavily-access", "test-key")
			overrideProviderURL(t, "tavily", upstream.URL+"/search")
			request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(tc.body))
			request = request.WithContext(context.WithValue(request.Context(), middleware.ApiKeyContextKey, &models.APIKey{ID: "1"}))
			rec := httptest.NewRecorder()
			newTestMediaHandler(repo).HandleSearch(rec, request)
			if rec.Code != tc.status {
				t.Fatalf("access status = %d, want %d, body %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusForbidden {
				if calls != 0 {
					t.Fatalf("denied model reached upstream %d times", calls)
				}
				return
			}
			var response nativeSearchResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Title != "Permitted result" {
				t.Fatalf("permitted result lost: %+v", response.Results)
			}
		})
	}
}
