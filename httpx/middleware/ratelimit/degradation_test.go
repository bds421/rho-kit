package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// stubHealth implements HealthIndicator for testing.
type stubHealth struct {
	healthy atomic.Bool
}

func (s *stubHealth) Healthy() bool { return s.healthy.Load() }

// passthroughHandler implements DegradationHandler, always returns nil.
type passthroughHandler struct{}

func (passthroughHandler) OnUnavailable(_ context.Context) error { return nil }

// failFastHandler implements DegradationHandler, always returns an error.
type failFastHandler struct{}

func (failFastHandler) OnUnavailable(_ context.Context) error {
	return errors.New("service unavailable")
}

func TestRateLimiter_Degradation_Passthrough(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(false)

	rl := NewRateLimiter(1, time.Minute,
		WithDegradation(health, passthroughHandler{}),
	)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// When unhealthy with passthrough, requests should pass through without rate limiting.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}
}

func TestRateLimiter_Degradation_FailFast(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(false)

	rl := NewRateLimiter(1, time.Minute,
		WithDegradation(health, failFastHandler{}),
	)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestRateLimiter_Degradation_HealthyUsesNormalRateLimiting(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(true)

	rl := NewRateLimiter(1, time.Minute,
		WithDegradation(health, passthroughHandler{}),
	)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want %d", rec.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.1:1234"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

func TestRateLimiter_Degradation_TransitionFromHealthyToUnhealthy(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(true)

	rl := NewRateLimiter(1, time.Minute,
		WithDegradation(health, passthroughHandler{}),
	)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want %d", rec.Code, http.StatusOK)
	}

	health.healthy.Store(false)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.1:1234"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("after degradation: got %d, want %d", rec2.Code, http.StatusOK)
	}
}

func TestRateLimiter_NoDegradation_BackwardCompatible(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want %d", rec.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "10.0.0.1:1234"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

func TestKeyedRateLimiter_Degradation_Passthrough(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(false)

	rl := NewKeyedRateLimiter(1, time.Minute,
		WithKeyedDegradation(health, passthroughHandler{}),
	)

	handler := KeyedRateLimitMiddleware(rl, func(r *http.Request) string {
		return r.Header.Get("X-API-Key")
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "key1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}
}

func TestKeyedRateLimiter_Degradation_FailFast(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(false)

	rl := NewKeyedRateLimiter(1, time.Minute,
		WithKeyedDegradation(health, failFastHandler{}),
	)

	handler := KeyedRateLimitMiddleware(rl, func(r *http.Request) string {
		return r.Header.Get("X-API-Key")
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "key1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestKeyedRateLimiter_Degradation_HealthyUsesNormalRateLimiting(t *testing.T) {
	health := &stubHealth{}
	health.healthy.Store(true)

	rl := NewKeyedRateLimiter(1, time.Minute,
		WithKeyedDegradation(health, passthroughHandler{}),
	)

	handler := KeyedRateLimitMiddleware(rl, func(r *http.Request) string {
		return r.Header.Get("X-API-Key")
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "key1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want %d", rec.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-API-Key", "key1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

func TestWithDegradation_PanicsOnNilHealth(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil health indicator")
		}
	}()
	WithDegradation(nil, passthroughHandler{})
}

func TestWithDegradation_PanicsOnNilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil handler")
		}
	}()
	WithDegradation(&stubHealth{}, nil)
}

func TestWithKeyedDegradation_PanicsOnNilHealth(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil health indicator")
		}
	}()
	WithKeyedDegradation(nil, passthroughHandler{})
}

func TestWithKeyedDegradation_PanicsOnNilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil handler")
		}
	}()
	WithKeyedDegradation(&stubHealth{}, nil)
}
