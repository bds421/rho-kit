package openapifilter

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestRoutesServeOnlyKeptOperationsAndListMissingOnes(t *testing.T) {
	spec := load(t)
	if err := keepExtra.Apply(spec); err != nil {
		t.Fatal(err)
	}
	routes := NewRoutes(spec, "/api")
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	routes.HandleFunc("GET /api/open", ok)
	routes.HandleFunc("POST /api/closed", ok)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/open", http.StatusNoContent},
		{http.MethodPost, "/api/closed", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if got := routes.Missing(); !slices.Equal(got, []string{"DELETE /api/open"}) {
		t.Errorf("missing %v", got)
	}
}
