package metrics_test

import (
	"context"
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

	routes := routeLabels(t, reg)
	for i, route := range routes {
		if route == "unmatched" {
			t.Fatalf("route=unmatched after CaptureRoute; labels=%v", routes[:i+1])
		}
		if route == "/api/v1/items/{id}" {
			return // success
		}
	}
	t.Fatalf("expected route=/api/v1/items/{id}, got %v", routes)
}

// routeLabels returns the route labels of http_requests_total in gather order.
func routeLabels(t *testing.T, reg *prometheus.Registry) []string {
	t.Helper()
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
				}
			}
		}
	}
	return routes
}

// The slot is written by CaptureRoute on the goroutine that serves the
// inner chain and read by outer middleware on the goroutine that owns the
// request; under timeout.Timeout those are different goroutines and the
// outer one returns as soon as the context is cancelled. Pass condition is
// "no data race under -race", nothing else: a plain-string slot fails
// this the moment the reader wins the schedule.
func TestCaptureRoute_ConcurrentOuterReadIsRaceFree(t *testing.T) {
	for range 200 {
		ctx := metrics.EnsureRoutePatternSlot(context.Background())
		mux := http.NewServeMux()
		mux.HandleFunc("GET /items/{id}", func(http.ResponseWriter, *http.Request) {})
		req := httptest.NewRequest(http.MethodGet, "/items/1", nil).WithContext(ctx)

		done := make(chan struct{})
		go func() {
			defer close(done)
			metrics.CaptureRoute(mux).ServeHTTP(httptest.NewRecorder(), req)
		}()
		// Outer reader racing the writer, as finishHTTPSpan / the metrics
		// defer do after a cancelled request.
		got := metrics.RoutePatternFromContext(ctx)
		<-done
		if got != "" && got != "GET /items/{id}" {
			t.Fatalf("torn or foreign pattern observed: %q", got)
		}
		if after := metrics.RoutePatternFromContext(ctx); after != "GET /items/{id}" {
			t.Fatalf("pattern after completion = %q", after)
		}
	}
}

// An outer mux routes "/" to an inner mux, as a service that mounts a
// generated API router under its own does. Each mux is wrapped in
// CaptureRoute and a middleware clones the request between them. The
// request is labelled with the inner pattern, method stripped; a path the
// inner mux does not match keeps the outer pattern.
func TestCaptureRoute_NestedMuxInnerPatternWins(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewHTTPMetrics(metrics.WithRegisterer(reg))

	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/v1/items/{id}", func(http.ResponseWriter, *http.Request) {})
	clone := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(r.Context()))
		})
	}
	outer := http.NewServeMux()
	outer.Handle("/", clone(metrics.CaptureRoute(inner)))
	outer.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	h := m.Middleware(clone(metrics.CaptureRoute(outer)))

	for _, path := range []string{"/api/v1/items/42", "/healthz", "/elsewhere"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	got := map[string]bool{}
	for _, route := range routeLabels(t, reg) {
		got[route] = true
	}
	want := map[string]bool{"/api/v1/items/{id}": true, "/healthz": true, "/": true}
	if len(got) != len(want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for route := range want {
		if !got[route] {
			t.Fatalf("routes = %v, want %v", got, want)
		}
	}
}
