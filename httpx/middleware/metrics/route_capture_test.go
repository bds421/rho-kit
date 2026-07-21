package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/httpx/v2/middleware/metrics"
)

// TestCaptureRoute_PropagatesPatternPastWithContextClone is the
// regression pin for review-08: when any middleware between metrics and
// the mux clones the request (r.WithContext), r.Pattern on the outer
// request stays empty. CaptureRoute + the metrics context slot must
// still surface the matched route label.
func TestCaptureRoute_PropagatesPatternPastWithContextClone(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewHTTPMetrics(metrics.WithRegisterer(reg))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Outer metrics → clone (simulates requestid/tracing) → CaptureRoute → mux
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clone like middleware that calls WithContext.
		metrics.CaptureRoute(mux).ServeHTTP(w, r.WithContext(r.Context()))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/items/42", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, mf := range families {
		if mf.GetName() != "http_requests_total" {
			continue
		}
		for _, met := range mf.GetMetric() {
			for _, lp := range met.GetLabel() {
				if lp.GetName() == "route" {
					routes = append(routes, lp.GetValue())
					if lp.GetValue() == "unmatched" {
						t.Fatalf("route=unmatched after CaptureRoute; labels=%v", routes)
					}
					if lp.GetValue() == "/api/v1/items/{id}" {
						return // success
					}
				}
			}
		}
	}
	t.Fatalf("expected route=/api/v1/items/{id}, got %v", routes)
}
