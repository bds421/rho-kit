package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKeyedRateLimiter_Allow(t *testing.T) {
	rl := NewKeyedRateLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		allowed, _ := rl.Allow("key1")
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	allowed, retryAfter := rl.Allow("key1")
	if allowed {
		t.Fatal("4th request should be denied")
	}
	if retryAfter < 1 {
		t.Errorf("retryAfter should be >= 1, got %d", retryAfter)
	}
}

func TestKeyedRateLimiter_SeparateKeys(t *testing.T) {
	rl := NewKeyedRateLimiter(1, time.Minute)

	allowed1, _ := rl.Allow("key1")
	if !allowed1 {
		t.Fatal("key1 first request should be allowed")
	}

	allowed2, _ := rl.Allow("key2")
	if !allowed2 {
		t.Fatal("key2 first request should be allowed (different key)")
	}

	allowed1Again, _ := rl.Allow("key1")
	if allowed1Again {
		t.Fatal("key1 second request should be denied")
	}
}

func TestKeyedRateLimiter_WindowExpiry(t *testing.T) {
	rl := NewKeyedRateLimiter(1, 10*time.Millisecond)

	allowed, _ := rl.Allow("key1")
	if !allowed {
		t.Fatal("first request should be allowed")
	}

	allowed, _ = rl.Allow("key1")
	if allowed {
		t.Fatal("second request should be denied")
	}

	time.Sleep(15 * time.Millisecond)

	allowed, _ = rl.Allow("key1")
	if !allowed {
		t.Fatal("request after window expiry should be allowed")
	}
}

func TestKeyedRateLimiter_Cleanup(t *testing.T) {
	rl := NewKeyedRateLimiter(1, 10*time.Millisecond)
	rl.Allow("key1")
	rl.Allow("key2")

	time.Sleep(15 * time.Millisecond)
	rl.cleanup()

	var count int
	for i := range rl.shards {
		s := &rl.shards[i]
		s.mu.Lock()
		count += s.entries.Len()
		s.mu.Unlock()
	}

	if count != 0 {
		t.Fatalf("cleanup should have removed stale entries, got %d", count)
	}
}

func TestValidateKeyRejectsUnsafeKeys(t *testing.T) {
	tests := map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("a", MaxKeyBytes+1),
		"null":         "tenant\x00a",
		"newline":      "tenant\na",
		"carriage":     "tenant\ra",
		"space":        "tenant a",
		"tab":          "tenant\ta",
		"invalid utf8": string([]byte{0xff}),
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateKey(key)
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("ValidateKey(%q) did not return ErrInvalidKey", key)
			}
			if name == "too long" && (strings.Contains(err.Error(), "256") || strings.Contains(err.Error(), "257")) {
				t.Fatalf("key length error leaked limits: %v", err)
			}
		})
	}
}

func TestKeyedRateLimiter_AllowKeyRejectsInvalidKeysWithoutStoring(t *testing.T) {
	rl := NewKeyedRateLimiter(1, time.Minute)

	allowed, retryAfter, err := rl.AllowKey(strings.Repeat("a", MaxKeyBytes+1))
	if allowed {
		t.Fatal("invalid key should not be allowed")
	}
	if retryAfter != 0 {
		t.Fatalf("retryAfter = %d, want 0 for invalid key", retryAfter)
	}
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("AllowKey error = %v, want ErrInvalidKey", err)
	}
	if strings.Contains(err.Error(), "256") || strings.Contains(err.Error(), "257") {
		t.Fatalf("AllowKey error leaked key lengths: %v", err)
	}
	if got := keyedEntryCount(rl); got != 0 {
		t.Fatalf("invalid key was stored; entries = %d", got)
	}
}

func TestKeyedRateLimiter_AllowFailsClosedOnInvalidKey(t *testing.T) {
	rl := NewKeyedRateLimiter(1, time.Minute)

	allowed, retryAfter := rl.Allow("")
	if allowed {
		t.Fatal("invalid key should fail closed")
	}
	if retryAfter < 1 {
		t.Fatalf("retryAfter = %d, want >= 1", retryAfter)
	}
	if got := keyedEntryCount(rl); got != 0 {
		t.Fatalf("invalid key was stored; entries = %d", got)
	}
}

func TestKeyedRateLimiter_AllowKeyRejectsUninitializedLimiter(t *testing.T) {
	var zero KeyedRateLimiter
	_, _, err := zero.AllowKey("key")
	if !errors.Is(err, ErrInvalidLimiter) {
		t.Fatalf("AllowKey error = %v, want ErrInvalidLimiter", err)
	}
}

func TestKeyedRateLimiter_RunStopsOnCancel(t *testing.T) {
	rl := NewKeyedRateLimiter(5, 10*time.Millisecond)
	_, _, err := rl.AllowKey("key")
	if err != nil {
		t.Fatalf("AllowKey returned %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Start(ctx) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}

func TestKeyedRateLimiter_RunRejectsNilContext(t *testing.T) {
	rl := NewKeyedRateLimiter(5, 10*time.Millisecond)
	var ctx context.Context
	err := rl.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "non-nil context") {
		t.Fatalf("expected non-nil context error, got %v", err)
	}
}

func TestKeyedRateLimiter_RunRejectsInvalidLimiter(t *testing.T) {
	var rl KeyedRateLimiter
	if err := rl.Start(context.Background()); !errors.Is(err, ErrInvalidLimiter) {
		t.Fatalf("Run error = %v, want ErrInvalidLimiter", err)
	}
}

func TestKeyedRateLimiter_RunRejectsSecondStart(t *testing.T) {
	rl := NewKeyedRateLimiter(5, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Start(ctx) }()
	waitForKeyedRateLimiterRunStarted(t, rl)

	err := rl.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestKeyedRateLimiter_RunRejectsRestartAfterCancel(t *testing.T) {
	rl := NewKeyedRateLimiter(5, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Start(ctx) }()
	waitForKeyedRateLimiterRunStarted(t, rl)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	err := rl.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}
}

func waitForKeyedRateLimiterRunStarted(t *testing.T, rl *KeyedRateLimiter) {
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
	t.Fatal("KeyedRateLimiter.Start did not start")
}

func TestKeyedMiddleware(t *testing.T) {
	rl := NewKeyedRateLimiter(2, time.Minute)

	handler := KeyedMiddleware(rl, func(r *http.Request) string {
		return r.Header.Get("X-API-Key")
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-API-Key", "api-key-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "api-key-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd request: expected 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header should be present on 429 response")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-API-Key", "api-key-2")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("different key: expected 200, got %d", rec2.Code)
	}
}

func TestKeyedMiddleware_InvalidKeyReturns400WithoutStoring(t *testing.T) {
	rl := NewKeyedRateLimiter(2, time.Minute)
	called := false
	handler := KeyedMiddleware(rl, func(r *http.Request) string {
		return r.Header.Get("X-API-Key")
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", strings.Repeat("a", MaxKeyBytes+1))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler must not run for invalid rate-limit keys")
	}
	if got := keyedEntryCount(rl); got != 0 {
		t.Fatalf("invalid key was stored; entries = %d", got)
	}
}

func TestKeyedMiddleware_PanicsOnNilInputs(t *testing.T) {
	t.Run("nil limiter", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on nil limiter")
			}
		}()
		KeyedMiddleware(nil, func(*http.Request) string { return "key" })
	})
	t.Run("nil key func", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic on nil key func")
			}
		}()
		KeyedMiddleware(NewKeyedRateLimiter(1, time.Minute), nil)
	})
}

func TestKeyedMiddleware_KeyFuncPanicReturns503(t *testing.T) {
	rl := NewKeyedRateLimiter(2, time.Minute)
	called := false
	handler := KeyedMiddleware(rl, func(*http.Request) string {
		panic("key failed")
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler must not run when key function panics")
	}
}

func TestWithKeyedClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithKeyedClock(nil)
}

func TestNewKeyedRateLimiter_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	_ = NewKeyedRateLimiter(1, time.Minute, nil)
}

func keyedEntryCount(rl *KeyedRateLimiter) int {
	var count int
	for i := range rl.shards {
		s := &rl.shards[i]
		s.mu.Lock()
		count += s.entries.Len()
		s.mu.Unlock()
	}
	return count
}
