package ratelimit

import (
	"net/http"
	"net/http/httptest"
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

func TestKeyedRateLimitMiddleware(t *testing.T) {
	rl := NewKeyedRateLimiter(2, time.Minute)

	handler := KeyedRateLimitMiddleware(rl, func(r *http.Request) string {
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
