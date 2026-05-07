package idempotency

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMiddleware_PanicsWithoutUserExtractorOrSharedKeysOptIn(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when Middleware is constructed without WithUserExtractor and WithAllowSharedKeys")
		}
	}()
	store := idem.NewMemoryStore()
	Middleware(store) // intentionally missing both — must panic
}

// TestIdempotency_EmptyUserReturns400_NotShared verifies that when an extractor
// is configured but returns "" at runtime (anonymous request, missing auth ctx,
// JWT minted without sub), the middleware fails closed with 400 rather than
// collapsing the cache key to (method, path, rawKey). The cache slot must NOT
// be poisoned: a subsequent request with a real user must execute fresh.
func TestIdempotency_EmptyUserReturns400_NotShared(t *testing.T) {
	store := idem.NewMemoryStore()

	extractor := func(r *http.Request) string {
		// Realistic mis-wiring: extractor reads a header populated by an
		// earlier auth middleware. Anonymous request → "".
		return r.Header.Get("X-User")
	}

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"served":true}`))
	})
	handler := Middleware(store, WithUserExtractor(extractor))(inner)

	// Request 1: anonymous — extractor returns "". Must be rejected with 400
	// and must not touch the cache slot.
	req1 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"v":1}`))
	req1.Header.Set("Idempotency-Key", "shared-key")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusBadRequest {
		t.Fatalf("anonymous request status = %d, want %d", rec1.Code, http.StatusBadRequest)
	}
	if callCount != 0 {
		t.Fatalf("inner handler called %d times for anonymous request, want 0", callCount)
	}

	// Request 2: authenticated — same Idempotency-Key. Must execute fresh
	// (the anonymous request must NOT have poisoned the cache slot).
	req2 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"v":2}`))
	req2.Header.Set("Idempotency-Key", "shared-key")
	req2.Header.Set("X-User", "alice-uuid")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("authenticated request status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if callCount != 1 {
		t.Fatalf("inner handler called %d times after authenticated request, want 1", callCount)
	}
	if rec2.Body.String() != `{"served":true}` {
		t.Fatalf("authenticated body = %q, want fresh response", rec2.Body.String())
	}
}

func TestMiddleware_StripsIdentityHeadersFromCachedResponse(t *testing.T) {
	store := idem.NewMemoryStore()

	// Handler sets identity-bearing response headers that must NOT be replayed.
	identityHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "session=user-A-secret")
		w.Header().Set("Authorization", "Bearer user-A-token")
		w.Header().Set("WWW-Authenticate", "Basic realm=\"x\"")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "kept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	handler := Middleware(store, WithAllowSharedKeys())(identityHandler)

	// First request — populates the cache.
	req1 := httptest.NewRequest(http.MethodPost, "/x", nil)
	req1.Header.Set("Idempotency-Key", "k")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec1.Code)
	}

	// Second request — replayed from cache. Identity headers must be absent.
	req2 := httptest.NewRequest(http.MethodPost, "/x", nil)
	req2.Header.Set("Idempotency-Key", "k")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	for _, h := range []string{"Set-Cookie", "Authorization", "WWW-Authenticate", "Strict-Transport-Security"} {
		if got := rec2.Header().Get(h); got != "" {
			t.Errorf("%s replayed (would leak cross-user identity); got %q", h, got)
		}
	}
	// Non-identity headers must survive.
	if got := rec2.Header().Get("X-Custom"); got != "kept" {
		t.Errorf("X-Custom header lost on replay; got %q", got)
	}
	if got := rec2.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type stripped (must not be); got %q", got)
	}
}

func TestMiddleware_PreserveHeadersOverridesStripList(t *testing.T) {
	store := idem.NewMemoryStore()

	identityHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// Explicit opt-in to replay HSTS — niche but supported.
	handler := Middleware(store, WithAllowSharedKeys(), WithPreserveHeaders("Strict-Transport-Security"))(identityHandler)

	req1 := httptest.NewRequest(http.MethodPost, "/x", nil)
	req1.Header.Set("Idempotency-Key", "k-pres")
	handler.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodPost, "/x", nil)
	req2.Header.Set("Idempotency-Key", "k-pres")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if got := rec2.Header().Get("Strict-Transport-Security"); got != "max-age=63072000" {
		t.Errorf("HSTS dropped despite WithPreserveHeaders; got %q", got)
	}
}

func TestFirstRequestPassesThroughAndCaches(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithAllowSharedKeys())(newTestHandler(`{"ok":true}`, http.StatusCreated))

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
	cached, _, err := store.Get(context.Background(), storeKey, nil)
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
	handler := Middleware(store, WithAllowSharedKeys())(inner)

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
	handler := Middleware(store, WithAllowSharedKeys())(inner)

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
	handler := Middleware(store, WithAllowSharedKeys())(inner)

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
	handler := Middleware(store, WithAllowSharedKeys())(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCustomHeaderName(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithAllowSharedKeys(), WithHeader("X-Request-Token"))(newTestHandler("ok", http.StatusOK))

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
	handler := Middleware(store, WithAllowSharedKeys(), WithRequiredMethods(http.MethodDelete))(
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
	handler := Middleware(store, WithAllowSharedKeys(), WithTTL(5*time.Minute))(newTestHandler("ok", http.StatusOK))

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
	resp, _, err := store.Get(context.Background(), "nonexistent", nil)
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

	token, _, ok, err := store.TryLock(context.Background(), "store-key", nil, time.Hour)
	if err != nil || !ok {
		t.Fatalf("TryLock: ok=%v err=%v", ok, err)
	}
	if err := store.Set(context.Background(), "store-key", token, original, time.Hour); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	got, _, err := store.Get(context.Background(), "store-key", nil)
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
	handler := Middleware(store, WithAllowSharedKeys())(inner)

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

	_, _, locked, err := store.TryLock(context.Background(), "lock-key", nil, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !locked {
		t.Error("expected to acquire lock")
	}

	// Second attempt should fail.
	_, _, locked2, err := store.TryLock(context.Background(), "lock-key", nil, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if locked2 {
		t.Error("expected lock to be already held")
	}
}

func TestUnlock(t *testing.T) {
	store := idem.NewMemoryStore()

	token, _, locked, err := store.TryLock(context.Background(), "unlock-key", nil, time.Minute)
	if err != nil || !locked {
		t.Fatal("expected to acquire lock")
	}

	// Unlock should allow re-acquisition.
	if err := store.Unlock(context.Background(), "unlock-key", token); err != nil {
		t.Fatalf("Unlock error: %v", err)
	}

	_, _, locked2, err := store.TryLock(context.Background(), "unlock-key", nil, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !locked2 {
		t.Error("expected to re-acquire lock after Unlock")
	}
}

func TestLockTTLExpiry(t *testing.T) {
	store := idem.NewMemoryStore()

	_, _, locked, _ := store.TryLock(context.Background(), "ttl-lock", nil, 1*time.Millisecond)
	if !locked {
		t.Fatal("expected to acquire lock")
	}

	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	_, _, locked2, _ := store.TryLock(context.Background(), "ttl-lock", nil, time.Minute)
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
	handler := Middleware(store, WithAllowSharedKeys())(inner)

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
	handler := Middleware(store, WithAllowSharedKeys())(newTestHandler("ok", http.StatusOK))

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
	handler := Middleware(store, WithAllowSharedKeys())(inner)

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
	_, _, locked, err := store.TryLock(context.Background(), storeKey, nil, time.Minute)
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

	token, _, _, _ := store.TryLock(context.Background(), "copy-key", nil, time.Hour)
	_ = store.Set(context.Background(), "copy-key", token, original, time.Hour)

	got1, _, _ := store.Get(context.Background(), "copy-key", nil)
	got1.Body[0] = 'X' // mutate returned copy

	got2, _, _ := store.Get(context.Background(), "copy-key", nil)
	if string(got2.Body) != "hello" {
		t.Error("mutation of returned value affected store; expected defensive copy")
	}
}

// --- New: body-fingerprint plumbing ---

func TestBodyFingerprint_MismatchReturns422(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithAllowSharedKeys(), WithBodyFingerprint())(newTestHandler(`{"ok":true}`, http.StatusCreated))

	// First request with body A.
	req1 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":100}`))
	req1.Header.Set("Idempotency-Key", "fp-key")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first request status = %d, want 201", rec1.Code)
	}

	// Same key, different body → 422.
	req2 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":999}`))
	req2.Header.Set("Idempotency-Key", "fp-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Errorf("second request status = %d, want 422", rec2.Code)
	}
}

func TestBodyFingerprint_SameBodyReplaysCache(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})
	handler := Middleware(store, WithAllowSharedKeys(), WithBodyFingerprint())(inner)

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":100}`))
		req.Header.Set("Idempotency-Key", "fp-same")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", rec.Code)
		}
	}

	if calls != 1 {
		t.Errorf("inner called %d times, want 1 (second request should replay)", calls)
	}
}

func TestBodyFingerprint_DownstreamHandlerCanReadBody(t *testing.T) {
	store := idem.NewMemoryStore()
	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = string(body)
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store, WithAllowSharedKeys(), WithBodyFingerprint())(inner)

	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("Idempotency-Key", "fp-passthrough")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if seen != `{"hello":"world"}` {
		t.Errorf("inner saw body %q, want %q — middleware must restore body after fingerprinting", seen, `{"hello":"world"}`)
	}
}

// TestBodyFingerprint_OversizedRejectedWith413 covers Codex finding #1 + #2.
// A request body larger than maxFingerprintBodySize must NOT reach the
// handler truncated, and must NOT collapse to a constant fingerprint that
// would let two distinct oversized bodies share an idempotency slot.
// The middleware rejects with 413 before any handler or store work runs.
func TestBodyFingerprint_OversizedRejectedWith413(t *testing.T) {
	store := idem.NewMemoryStore()
	handlerCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store, WithAllowSharedKeys(), WithBodyFingerprint())(inner)

	tooBig := bytes.Repeat([]byte("x"), maxFingerprintBodySize+1)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(tooBig))
	req.Header.Set("Idempotency-Key", "big-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if handlerCalled {
		t.Error("handler must not run for oversized fingerprinted body")
	}
	// The cache slot must not have been touched — a follow-up under-cap
	// request with the same key proceeds normally.
	req2 := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("ok"))
	req2.Header.Set("Idempotency-Key", "big-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("subsequent under-cap request status = %d, want 200", rec2.Code)
	}
	if !handlerCalled {
		t.Error("handler must run for under-cap request with the same key")
	}
}

// TestBodyFingerprint_AtCapBoundaryAccepted ensures the boundary case
// (body == maxFingerprintBodySize) still works — only strictly larger
// bodies should be rejected.
func TestBodyFingerprint_AtCapBoundaryAccepted(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithAllowSharedKeys(), WithBodyFingerprint())(
		newTestHandler(`{"ok":true}`, http.StatusOK),
	)

	atCap := bytes.Repeat([]byte("y"), maxFingerprintBodySize)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(atCap))
	req.Header.Set("Idempotency-Key", "boundary-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body == cap should pass)", rec.Code, http.StatusOK)
	}
}

// hangingStore implements idem.Store but blocks Set/Unlock until ctx is
// cancelled. The middleware must bound the post-handler context so a hung
// store cannot pin the request goroutine indefinitely.
type hangingStore struct{}

func (hangingStore) Get(_ context.Context, _ string, _ []byte) (*idem.CachedResponse, bool, error) {
	return nil, false, nil
}

func (hangingStore) TryLock(_ context.Context, _ string, _ []byte, _ time.Duration) (string, bool, bool, error) {
	return "tok", false, true, nil
}

func (hangingStore) Set(ctx context.Context, _, _ string, _ idem.CachedResponse, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func (hangingStore) Unlock(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestPostHandlerTimeout_HungStoreDoesNotPinGoroutine covers Codex
// finding #4. With a short post-handler timeout the middleware must
// return promptly even when Set hangs, instead of waiting forever on
// context.Background().
func TestPostHandlerTimeout_HungStoreDoesNotPinGoroutine(t *testing.T) {
	handler := Middleware(hangingStore{},
		WithAllowSharedKeys(),
		WithPostHandlerTimeout(50*time.Millisecond),
	)(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "hang-key")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
		// expected — middleware returned despite the hung Set
	case <-time.After(500 * time.Millisecond):
		t.Fatal("middleware did not return; post-handler context was not bounded")
	}
}

func TestWithPostHandlerTimeout_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for non-positive timeout")
		}
	}()
	WithPostHandlerTimeout(0)
}

// flushTrackingWriter records whether Flush was forwarded. It also
// implements http.Hijacker / http.Pusher so the wrapper exposes them
// transitively.
type flushTrackingWriter struct {
	http.ResponseWriter
	flushed atomic.Bool
}

func (f *flushTrackingWriter) Flush() { f.flushed.Store(true) }

// TestResponseCapture_ForwardsFlush covers Codex finding #5. The
// responseCapture wrapper must forward Flush() to the underlying writer
// so streaming handlers (SSE, chunked transfer) keep working behind the
// middleware.
func TestResponseCapture_ForwardsFlush(t *testing.T) {
	tracked := &flushTrackingWriter{ResponseWriter: httptest.NewRecorder()}
	rc := &responseCapture{
		ResponseWriter:  tracked,
		capturedHeaders: make(http.Header),
		statusCode:      http.StatusOK,
		body:            &bytes.Buffer{},
	}
	// http.Flusher must be reachable from the wrapper.
	flusher, ok := http.ResponseWriter(rc).(http.Flusher)
	if !ok {
		t.Fatal("responseCapture does not satisfy http.Flusher")
	}
	flusher.Flush()
	if !tracked.flushed.Load() {
		t.Error("Flush was not forwarded to the underlying ResponseWriter")
	}
}

// hijackTrackingWriter implements http.Hijacker for the wrapper test.
type hijackTrackingWriter struct {
	http.ResponseWriter
	called atomic.Bool
}

func (h *hijackTrackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.called.Store(true)
	return nil, nil, nil
}

func TestResponseCapture_ForwardsHijack(t *testing.T) {
	tracked := &hijackTrackingWriter{ResponseWriter: httptest.NewRecorder()}
	rc := &responseCapture{
		ResponseWriter:  tracked,
		capturedHeaders: make(http.Header),
		statusCode:      http.StatusOK,
		body:            &bytes.Buffer{},
	}
	hijacker, ok := http.ResponseWriter(rc).(http.Hijacker)
	if !ok {
		t.Fatal("responseCapture does not satisfy http.Hijacker")
	}
	if _, _, err := hijacker.Hijack(); err != nil {
		t.Fatalf("Hijack returned err: %v", err)
	}
	if !tracked.called.Load() {
		t.Error("Hijack was not forwarded to the underlying ResponseWriter")
	}
}
