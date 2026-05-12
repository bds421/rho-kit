package ratelimit

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCleanupInterval_SaturatesOnLargeWindow(t *testing.T) {
	if got := cleanupInterval(time.Second); got != 2*time.Second {
		t.Fatalf("cleanupInterval(time.Second)=%v, want 2s", got)
	}
	if got := cleanupInterval(maxDurationValue); got != maxDurationValue {
		t.Fatalf("cleanupInterval(maxDuration)=%v, want %v", got, maxDurationValue)
	}
}

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if allowed, _ := rl.allow("1.2.3.4"); !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	if allowed, _ := rl.allow("1.2.3.4"); allowed {
		t.Fatal("4th request should be denied")
	}

	if allowed, _ := rl.allow("5.6.7.8"); !allowed {
		t.Fatal("different IP should be allowed")
	}
}

func TestRateLimiterAllowRejectsEmptyIPWithoutStoring(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)

	allowed, remaining := rl.allow("")
	if allowed {
		t.Fatal("empty IP should not be allowed")
	}
	if remaining != 0 {
		t.Fatalf("remaining = %s, want 0 for invalid IP", remaining)
	}
	if got := rateLimiterEntryCount(rl); got != 0 {
		t.Fatalf("empty IP was stored; entries = %d", got)
	}
}

func TestRateLimiterWindowReset(t *testing.T) {
	now := time.Now()
	rl := NewRateLimiter(2, 50*time.Millisecond, WithClock(func() time.Time { return now }))

	rl.allow("1.2.3.4") //nolint:errcheck
	rl.allow("1.2.3.4") //nolint:errcheck

	if allowed, _ := rl.allow("1.2.3.4"); allowed {
		t.Fatal("should be denied after limit")
	}

	now = now.Add(60 * time.Millisecond)

	if allowed, _ := rl.allow("1.2.3.4"); !allowed {
		t.Fatal("should be allowed after window reset")
	}
}

func TestRateLimiterCleanup(t *testing.T) {
	now := time.Now()
	rl := NewRateLimiter(5, 50*time.Millisecond, WithClock(func() time.Time { return now }))

	rl.allow("1.2.3.4") //nolint:errcheck
	rl.allow("5.6.7.8") //nolint:errcheck

	now = now.Add(60 * time.Millisecond)

	rl.cleanup()

	count := 0
	for i := range rl.shards {
		s := &rl.shards[i]
		s.mu.Lock()
		count += s.visitors.Len()
		s.mu.Unlock()
	}

	if count != 0 {
		t.Fatalf("cleanup should have removed stale entries, got %d", count)
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)

	handler := Middleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want %d", i+1, w.Code, http.StatusOK)
		}
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd request: got %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	retryAfter := w.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After header should be present on 429 response")
	}
}

func TestRateLimiterMiddleware_InvalidClientIPReturns400WithoutStoring(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	called := false
	handler := Middleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "not an ip"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if called {
		t.Fatal("next handler must not run when client IP cannot be determined")
	}
	if got := rateLimiterEntryCount(rl); got != 0 {
		t.Fatalf("invalid client IP was stored; entries = %d", got)
	}
}

func TestRateLimiterXForwardedFor(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)

	handler := Middleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("1st request: got %d, want %d", w.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "10.0.0.1:1234"
	req2.Header.Set("X-Forwarded-For", "203.0.113.1")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd request: got %d, want %d", w2.Code, http.StatusTooManyRequests)
	}

	req3 := httptest.NewRequest("GET", "/", nil)
	req3.RemoteAddr = "10.0.0.2:5678"
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("3rd request different IP: got %d, want %d", w3.Code, http.StatusOK)
	}
}

func TestRateLimiterXForwardedForMultipleIPs(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute, WithTrustedProxies([]string{"10.0.0.0/24", "198.51.100.0/24"}))

	handler := Middleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 198.51.100.1, 10.0.0.1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("1st request: got %d, want %d", w.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "10.0.0.2:5678"
	req2.Header.Set("X-Forwarded-For", "203.0.113.50, 198.51.100.2")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd request same real IP: got %d, want %d", w2.Code, http.StatusTooManyRequests)
	}

	req3 := httptest.NewRequest("GET", "/", nil)
	req3.RemoteAddr = "10.0.0.1:1234"
	req3.Header.Set("X-Forwarded-For", "198.51.100.99, 10.0.0.5")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("3rd request different real IP: got %d, want %d", w3.Code, http.StatusOK)
	}
}

func TestRateLimiterTrustedProxies(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute, WithTrustedProxies([]string{"10.0.0.0/24"}))

	handler := Middleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("trusted proxy request: got %d, want %d", w.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "192.168.1.1:5678"
	req2.Header.Set("X-Forwarded-For", "203.0.113.1")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("untrusted proxy request: got %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestWithTrustedProxies_OptionReuseClonesOutput(t *testing.T) {
	opt := WithTrustedProxies([]string{"192.0.2.0/24"})

	rl1 := NewRateLimiter(1, time.Minute, opt)
	rl2 := NewRateLimiter(1, time.Minute, opt)

	rl1.trustedProxies[0].IP = net.ParseIP("10.0.0.0")
	if !rl2.trustedProxies[0].Contains(net.ParseIP("192.0.2.10")) {
		t.Fatalf("second option application shared trusted proxy state: %v", rl2.trustedProxies[0])
	}
}

func TestRateLimiterRun_StopsOnCancel(t *testing.T) {
	rl := NewRateLimiter(5, 10*time.Millisecond)
	rl.allow("1.2.3.4")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- rl.Start(ctx)
	}()

	// Let at least one cleanup tick fire.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
		// Run returned successfully.
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}

func TestRateLimiterRun_RejectsNilContext(t *testing.T) {
	rl := NewRateLimiter(5, 10*time.Millisecond)
	var ctx context.Context
	err := rl.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "non-nil context") {
		t.Fatalf("expected non-nil context error, got %v", err)
	}
}

func TestRateLimiterRun_RejectsInvalidLimiter(t *testing.T) {
	var rl RateLimiter
	if err := rl.Start(context.Background()); err != ErrInvalidLimiter {
		t.Fatalf("Run error = %v, want ErrInvalidLimiter", err)
	}
}

func TestRateLimiterRun_RejectsSecondStart(t *testing.T) {
	rl := NewRateLimiter(5, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Start(ctx) }()
	waitForRateLimiterRunStarted(t, rl)

	err := rl.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRateLimiterRun_RejectsRestartAfterCancel(t *testing.T) {
	rl := NewRateLimiter(5, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Start(ctx) }()
	waitForRateLimiterRunStarted(t, rl)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	err := rl.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}
}

func waitForRateLimiterRunStarted(t *testing.T, rl *RateLimiter) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rl.startMu.Lock()
		started := rl.started
		rl.startMu.Unlock()
		if started {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("RateLimiter.Start did not start")
}

func TestClientIP_DirectConnection(t *testing.T) {
	rl := NewRateLimiter(10, time.Minute)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.1:4321"

	ip := rl.ClientIP(req)
	if ip != "203.0.113.1" {
		t.Errorf("ClientIP = %q, want 203.0.113.1", ip)
	}
}

func TestClientIP_UntrustedProxy(t *testing.T) {
	// With a non-trusted remote addr, X-Forwarded-For should be ignored.
	rl := NewRateLimiter(10, time.Minute, WithTrustedProxies([]string{"10.0.0.0/8"}))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.1:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")

	ip := rl.ClientIP(req)
	if ip != "203.0.113.1" {
		t.Errorf("ClientIP = %q, want 203.0.113.1 (untrusted proxy)", ip)
	}
}

func TestClientIP_TrustedProxy(t *testing.T) {
	rl := NewRateLimiter(10, time.Minute, WithTrustedProxies([]string{"10.0.0.0/8"}))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	ip := rl.ClientIP(req)
	if ip != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want 203.0.113.50 (real client)", ip)
	}
}

func TestWithTrustedProxies_PlainIP(t *testing.T) {
	// Test that plain IPs (not CIDRs) are accepted.
	rl := NewRateLimiter(10, time.Minute, WithTrustedProxies([]string{"10.0.0.1"}))

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	ip := rl.ClientIP(req)
	if ip != "203.0.113.50" {
		t.Errorf("ClientIP = %q, want 203.0.113.50", ip)
	}
}

func TestWithTrustedProxies_PanicsOnInvalid(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on invalid trusted proxy")
		}
		if r != "ratelimit: invalid trusted proxy" {
			t.Fatalf("panic = %q, want %q", r, "ratelimit: invalid trusted proxy")
		}
	}()
	_ = WithTrustedProxies([]string{"secret-token", "10.0.0.0/8"})
}

func TestRateLimiterWithClock(t *testing.T) {
	now := time.Now()
	rl := NewRateLimiter(1, time.Minute, WithClock(func() time.Time { return now }))

	if allowed, _ := rl.allow("1.2.3.4"); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _ := rl.allow("1.2.3.4"); allowed {
		t.Fatal("second request should be denied")
	}

	now = now.Add(2 * time.Minute)

	if allowed, _ := rl.allow("1.2.3.4"); !allowed {
		t.Fatal("should be allowed after clock advance")
	}
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithClock(nil)
}

func TestNewRateLimiter_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	_ = NewRateLimiter(1, time.Minute, nil)
}

func TestRateLimiterMiddleware_PanicsOnInvalidInputs(t *testing.T) {
	t.Run("zero limiter", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on zero limiter")
			}
		}()
		var zero RateLimiter
		_ = Middleware(&zero)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	})
	t.Run("nil limiter", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on nil limiter")
			}
		}()
		_ = Middleware(nil)
	})
	t.Run("nil next", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on nil next handler")
			}
		}()
		_ = Middleware(NewRateLimiter(1, time.Minute))(nil)
	})
}

func rateLimiterEntryCount(rl *RateLimiter) int {
	var count int
	for i := range rl.shards {
		s := &rl.shards[i]
		s.mu.Lock()
		count += s.visitors.Len()
		s.mu.Unlock()
	}
	return count
}
