package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	idem "github.com/bds421/rho-kit/data/idempotency"
)

func newTestHandler(body string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestFirstRequestPassesThroughAndCaches(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store)(newTestHandler(`{"ok":true}`, http.StatusCreated))

	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req.Header.Set("Idempotency-Key", "key-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("body = %q, want %q", rec.Body.String(), `{"ok":true}`)
	}

	// The store key is fingerprinted with method+path, so look up the hashed key.
	storeKey := fingerprintKey(http.MethodPost, "/orders", "key-1", "")
	cached, err := store.Get(context.Background(), storeKey)
	if err != nil {
		t.Fatalf("store.Get error: %v", err)
	}
	if cached == nil {
		t.Fatal("expected cached response, got nil")
	}
	if cached.StatusCode != http.StatusCreated {
		t.Errorf("cached status = %d, want %d", cached.StatusCode, http.StatusCreated)
	}
}

func TestSecondRequestReplaysCachedResponse(t *testing.T) {
	store := idem.NewMemoryStore()
	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"n":1}`))
	})
	handler := Middleware(store)(inner)

	// First request
	req1 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req1.Header.Set("Idempotency-Key", "key-dup")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	// Second request with same key
	req2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req2.Header.Set("Idempotency-Key", "key-dup")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if callCount != 1 {
		t.Errorf("handler called %d times, want 1", callCount)
	}
	if rec2.Code != http.StatusCreated {
		t.Errorf("replayed status = %d, want %d", rec2.Code, http.StatusCreated)
	}
	if rec2.Body.String() != `{"n":1}` {
		t.Errorf("replayed body = %q, want %q", rec2.Body.String(), `{"n":1}`)
	}
}

func TestDifferentKeysAreIndependent(t *testing.T) {
	store := idem.NewMemoryStore()
	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store)(inner)

	for _, key := range []string{"a", "b", "c"} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Idempotency-Key", key)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	if callCount != 3 {
		t.Errorf("handler called %d times, want 3", callCount)
	}
}

func TestGETPassesThroughWithoutHeader(t *testing.T) {
	store := idem.NewMemoryStore()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store)(inner)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("GET handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestPOSTWithoutHeaderReturns400(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store)(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCustomHeaderName(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithHeader("X-Request-Token"))(newTestHandler("ok", http.StatusOK))

	// Missing custom header returns 400
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	// With custom header succeeds
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.Header.Set("X-Request-Token", "tok-1")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec2.Code, http.StatusOK)
	}
}

func TestWithRequiredMethods(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithRequiredMethods(http.MethodDelete))(
		newTestHandler("deleted", http.StatusOK),
	)

	// POST without header should pass through (not required)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("POST status = %d, want %d", rec.Code, http.StatusOK)
	}

	// DELETE without header returns 400
	req2 := httptest.NewRequest(http.MethodDelete, "/items/1", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Errorf("DELETE status = %d, want %d", rec2.Code, http.StatusBadRequest)
	}
}

func TestWithTTLOption(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithTTL(5*time.Minute))(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "ttl-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestMemoryStoreGetMiss(t *testing.T) {
	store := idem.NewMemoryStore()
	resp, err := store.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil for missing key, got %+v", resp)
	}
}

func TestMemoryStoreSetAndGet(t *testing.T) {
	store := idem.NewMemoryStore()
	original := idem.CachedResponse{
		StatusCode: http.StatusCreated,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"id":42}`),
	}

	err := store.Set(context.Background(), "store-key", original, time.Hour)
	if err != nil {
		t.Fatalf("Set error: %v", err)
	}

	got, err := store.Get(context.Background(), "store-key")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got == nil {
		t.Fatal("expected cached response, got nil")
	}
	if got.StatusCode != original.StatusCode {
		t.Errorf("StatusCode = %d, want %d", got.StatusCode, original.StatusCode)
	}
	ct := got.Headers["Content-Type"]
	if len(ct) == 0 || ct[0] != "application/json" {
		t.Errorf("Content-Type = %v, want [application/json]", ct)
	}
	if string(got.Body) != `{"id":42}` {
		t.Errorf("Body = %q, want %q", string(got.Body), `{"id":42}`)
	}
}

func TestConcurrentRequestsSameKey(t *testing.T) {
	store := idem.NewMemoryStore()
	var handlerCalls atomic.Int32

	// Handler that takes time to process, simulating a slow operation.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	handler := Middleware(store)(inner)

	var wg sync.WaitGroup
	results := make([]int, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/orders", nil)
			req.Header.Set("Idempotency-Key", "race-key")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			results[idx] = rec.Code
		}(i)
	}
	wg.Wait()

	// Only one goroutine should have executed the handler.
	if handlerCalls.Load() != 1 {
		t.Errorf("handler called %d times, want 1", handlerCalls.Load())
	}

	// One should get 201 (processed), the other 409 (conflict).
	got201, got409 := 0, 0
	for _, code := range results {
		switch code {
		case http.StatusCreated:
			got201++
		case http.StatusConflict:
			got409++
		}
	}
	if got201 != 1 || got409 != 1 {
		t.Errorf("expected one 201 and one 409, got results: %v", results)
	}
}

func TestTryLock(t *testing.T) {
	store := idem.NewMemoryStore()

	locked, err := store.TryLock(context.Background(), "lock-key", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !locked {
		t.Error("expected to acquire lock")
	}

	// Second attempt should fail.
	locked2, err := store.TryLock(context.Background(), "lock-key", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if locked2 {
		t.Error("expected lock to be already held")
	}
}

func TestUnlock(t *testing.T) {
	store := idem.NewMemoryStore()

	locked, err := store.TryLock(context.Background(), "unlock-key", time.Minute)
	if err != nil || !locked {
		t.Fatal("expected to acquire lock")
	}

	// Unlock should allow re-acquisition.
	if err := store.Unlock(context.Background(), "unlock-key"); err != nil {
		t.Fatalf("Unlock error: %v", err)
	}

	locked2, err := store.TryLock(context.Background(), "unlock-key", time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !locked2 {
		t.Error("expected to re-acquire lock after Unlock")
	}
}

func TestLockTTLExpiry(t *testing.T) {
	store := idem.NewMemoryStore()

	locked, _ := store.TryLock(context.Background(), "ttl-lock", 1*time.Millisecond)
	if !locked {
		t.Fatal("expected to acquire lock")
	}

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	locked2, _ := store.TryLock(context.Background(), "ttl-lock", time.Minute)
	if !locked2 {
		t.Error("expected expired lock to be reclaimable")
	}
}

func TestAllResponseHeadersCached(t *testing.T) {
	store := idem.NewMemoryStore()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/orders/42")
		w.Header().Set("X-Custom", "value")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42}`))
	})
	handler := Middleware(store)(inner)

	// First request
	req1 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req1.Header.Set("Idempotency-Key", "header-key")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	// Second request (replay)
	req2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req2.Header.Set("Idempotency-Key", "header-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Header().Get("Location") != "/orders/42" {
		t.Errorf("replayed Location = %q, want %q", rec2.Header().Get("Location"), "/orders/42")
	}
	if rec2.Header().Get("X-Custom") != "value" {
		t.Errorf("replayed X-Custom = %q, want %q", rec2.Header().Get("X-Custom"), "value")
	}
}

func TestErrorResponsesAreJSON(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store)(newTestHandler("ok", http.StatusOK))

	// POST without idempotency key should return JSON 400.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestPanicInHandlerReleasesLock(t *testing.T) {
	store := idem.NewMemoryStore()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("handler blew up")
	})
	handler := Middleware(store)(inner)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "panic-key")
	rec := httptest.NewRecorder()

	// Wrap in recover since the panic propagates after lock release.
	func() {
		defer func() { _ = recover() }()
		handler.ServeHTTP(rec, req)
	}()

	// The lock should have been released, allowing a new TryLock.
	// Use the fingerprinted key that the middleware actually stores.
	storeKey := fingerprintKey(http.MethodPost, "/", "panic-key", "")
	locked, err := store.TryLock(context.Background(), storeKey, time.Minute)
	if err != nil {
		t.Fatalf("TryLock error: %v", err)
	}
	if !locked {
		t.Error("expected lock to be released after handler panic")
	}
}

func TestMemoryStoreReturnsCopy(t *testing.T) {
	store := idem.NewMemoryStore()
	original := idem.CachedResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string][]string{"Content-Type": {"text/plain"}},
		Body:       []byte("hello"),
	}

	_ = store.Set(context.Background(), "copy-key", original, time.Hour)

	got1, _ := store.Get(context.Background(), "copy-key")
	got1.Body[0] = 'X' // mutate returned copy

	got2, _ := store.Get(context.Background(), "copy-key")
	if string(got2.Body) != "hello" {
		t.Error("mutation of returned value affected store; expected defensive copy")
	}
}
