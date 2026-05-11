package idempotency

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	idem "github.com/bds421/rho-kit/data/v2/idempotency"
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

// TestMiddleware_PanicsOnNilStore verifies the constructor fails fast
// instead of waiting for the first request to dereference nil.
func TestMiddleware_PanicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when Middleware is constructed with a nil Store")
		}
	}()
	Middleware(nil, WithAllowSharedKeys())
}

// TestWithHeader_PanicsOnEmpty verifies that an invalid header field name
// is rejected at construction rather than producing a confusing
// "missing empty header" error on every request.
func TestWithHeader_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty header name")
		}
	}()
	WithHeader("")
}

// TestWithHeader_PanicsOnInvalid rejects names with spaces or control
// characters, which would not be a valid HTTP field name.
func TestWithHeader_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid header name")
		}
	}()
	WithHeader("bad header") // space is not allowed in field names
}

// TestWithLogger_NilNormalizesToDefault ensures error paths can run
// without panicking when callers pass a nil logger.
func TestWithLogger_NilNormalizesToDefault(t *testing.T) {
	store := idem.NewMemoryStore()
	// If WithLogger(nil) failed to normalize, the post-handler error
	// path on the hung-store test would dereference nil and panic. Build
	// a middleware with a nil logger and a hanging store, then drive a
	// request through and confirm the request goroutine returns.
	handler := Middleware(hangingStore{},
		WithAllowSharedKeys(),
		WithLogger(nil),
		WithPostHandlerTimeout(50*time.Millisecond),
	)(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "nil-logger")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("middleware did not return; nil logger likely caused panic on error path")
	}
	_ = store
}

func TestMiddleware_ResponseOverflowLogRedactsRawKey(t *testing.T) {
	store := idem.NewMemoryStore()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	handler := Middleware(store,
		WithAllowSharedKeys(),
		WithLogger(logger),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxCapturedBodySize+1))
	}))

	req := httptest.NewRequest(http.MethodPost, "/pay", nil)
	req.Header.Set("Idempotency-Key", "tenant-secret-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := buf.String()
	if !strings.Contains(out, `"key"`) {
		t.Fatalf("expected key attribute in log, got %q", out)
	}
	if strings.Contains(out, "tenant-secret-key") {
		t.Fatalf("idempotency log leaked raw key: %q", out)
	}
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

func TestIdempotency_UserExtractorPanicReturns400_NotShared(t *testing.T) {
	store := idem.NewMemoryStore()
	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store, WithUserExtractor(func(*http.Request) string {
		panic("extract failed")
	}))(inner)

	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"v":1}`))
	req.Header.Set("Idempotency-Key", "shared-key")
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("extractor panic escaped: %v", r)
			}
		}()
		handler.ServeHTTP(rec, req)
	}()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if callCount != 0 {
		t.Fatalf("inner handler called %d times, want 0", callCount)
	}
}

func TestIdempotency_InvalidUserIdentityReturns400_NotShared(t *testing.T) {
	for name, userID := range map[string]string{
		"edge whitespace": " user-a",
		"internal space":  "user a",
		"control":         "user-a\n",
		"invalid utf8":    string([]byte{'u', 0xff}),
		"too long":        strings.Repeat("a", idem.MaxKeyLen+1),
	} {
		t.Run(name, func(t *testing.T) {
			store := idem.NewMemoryStore()
			callCount := 0
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				callCount++
				w.WriteHeader(http.StatusOK)
			})
			handler := Middleware(store, WithUserExtractor(func(*http.Request) string {
				return userID
			}))(inner)

			req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"v":1}`))
			req.Header.Set("Idempotency-Key", "valid-key")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if callCount != 0 {
				t.Fatalf("inner handler called %d times, want 0", callCount)
			}
		})
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

	// The store key is fingerprinted with method+path+query+key, so look up the hashed key.
	keyReq := httptest.NewRequest(http.MethodPost, "/orders", nil)
	storeKey := mustFingerprintKey(t, keyReq, "key-1", "", nil)
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

func TestPOSTWithBlankHeaderReturns400(t *testing.T) {
	store := idem.NewMemoryStore()
	called := false
	handler := Middleware(store, WithAllowSharedKeys())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req.Header.Set("Idempotency-Key", " \t ")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if called {
		t.Fatal("handler should not be called for blank Idempotency-Key")
	}
}

func TestPOSTWithDuplicateHeaderReturns400(t *testing.T) {
	store := idem.NewMemoryStore()
	called := false
	handler := Middleware(store, WithAllowSharedKeys())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req.Header.Add("Idempotency-Key", "key-a")
	req.Header.Add("Idempotency-Key", "key-b")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if called {
		t.Fatal("handler should not be called for duplicate Idempotency-Key headers")
	}
}

func TestPOSTWithAmbiguousHeaderReturns400(t *testing.T) {
	for name, value := range map[string]string{
		"edge whitespace": " key-a",
		"internal space":  "key a",
		"comma":           "key-a,key-b",
		"control":         "key-a\n",
		"invalid utf8":    string([]byte{'k', 0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			store := idem.NewMemoryStore()
			called := false
			handler := Middleware(store, WithAllowSharedKeys())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/orders", nil)
			req.Header.Set("Idempotency-Key", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if called {
				t.Fatal("handler should not be called for ambiguous Idempotency-Key")
			}
		})
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
	if strings.Contains(rec.Body.String(), "X-Request-Token") {
		t.Fatalf("response leaked custom header name: %q", rec.Body.String())
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
	keyReq := httptest.NewRequest(http.MethodPost, "/", nil)
	storeKey := mustFingerprintKey(t, keyReq, "panic-key", "", nil)
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

func TestBodyFingerprint_ContentHeadersAffectFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		header      string
		firstValue  string
		secondValue string
	}{
		{
			name:        "content type",
			header:      "Content-Type",
			firstValue:  "application/x-www-form-urlencoded",
			secondValue: "text/plain",
		},
		{
			name:        "content encoding",
			header:      "Content-Encoding",
			firstValue:  "gzip",
			secondValue: "identity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := idem.NewMemoryStore()
			calls := 0
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("created"))
			})
			handler := Middleware(store, WithAllowSharedKeys())(inner)

			req1 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`a=b`))
			req1.Header.Set("Idempotency-Key", "content-header-key")
			req1.Header.Set(tc.header, tc.firstValue)
			rec1 := httptest.NewRecorder()
			handler.ServeHTTP(rec1, req1)
			if rec1.Code != http.StatusCreated {
				t.Fatalf("first request status = %d, want 201", rec1.Code)
			}

			req2 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`a=b`))
			req2.Header.Set("Idempotency-Key", "content-header-key")
			req2.Header.Set(tc.header, tc.secondValue)
			rec2 := httptest.NewRecorder()
			handler.ServeHTTP(rec2, req2)
			if rec2.Code != http.StatusUnprocessableEntity {
				t.Fatalf("second request status = %d, want 422", rec2.Code)
			}
			if calls != 1 {
				t.Fatalf("handler called %d times, want first request only", calls)
			}
		})
	}
}

func TestBodyFingerprint_RejectsAmbiguousContentHeaders(t *testing.T) {
	for _, header := range []string{"Content-Type", "Content-Encoding"} {
		t.Run(header, func(t *testing.T) {
			store := idem.NewMemoryStore()
			called := false
			handler := Middleware(store, WithAllowSharedKeys())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`a=b`))
			req.Header.Set("Idempotency-Key", "ambiguous-content-header")
			req.Header.Add(header, "one")
			req.Header.Add(header, "two")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if called {
				t.Fatal("handler must not run when fingerprint headers are ambiguous")
			}
		})
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

// TestBodyFingerprint_DefaultOn_RejectsMutatedRetry verifies that body
// fingerprinting is enabled by default for unsafe methods so a client that
// reuses an Idempotency-Key with a mutated body gets 422 instead of
// silently colliding with the previous slot. This is the main corruption
// case the middleware exists to prevent; off-by-default would let a retry
// with a different amount silently hit the original response.
func TestBodyFingerprint_DefaultOn_RejectsMutatedRetry(t *testing.T) {
	store := idem.NewMemoryStore()
	handler := Middleware(store, WithAllowSharedKeys())(
		newTestHandler(`{"ok":true}`, http.StatusCreated),
	)

	req1 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":100}`))
	req1.Header.Set("Idempotency-Key", "default-fp-key")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first request status = %d, want 201", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":999}`))
	req2.Header.Set("Idempotency-Key", "default-fp-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Errorf("mutated retry status = %d, want 422 (default-on body fingerprint)", rec2.Code)
	}
}

// TestBodyFingerprint_DefaultOn_SameBodyReplaysCache verifies that the
// happy path still works under the new default: a same-key, same-body
// retry hits the cached response and skips the handler.
func TestBodyFingerprint_DefaultOn_SameBodyReplaysCache(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})
	handler := Middleware(store, WithAllowSharedKeys())(inner)

	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":100}`))
		req.Header.Set("Idempotency-Key", "default-fp-same")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", rec.Code)
		}
	}
	if calls != 1 {
		t.Errorf("inner called %d times, want 1 (second request must replay cache)", calls)
	}
}

// TestWithoutBodyFingerprint_OptsOut verifies the explicit opt-out: with
// fingerprinting disabled, a same-key, mutated-body retry hits the cached
// response (the historical pre-fix behaviour) instead of returning 422.
// Use only on large-body routes that have an out-of-band guarantee callers
// will not reuse a key with a different body.
func TestWithoutBodyFingerprint_OptsOut(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})
	handler := Middleware(store, WithAllowSharedKeys(), WithoutBodyFingerprint())(inner)

	req1 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":100}`))
	req1.Header.Set("Idempotency-Key", "no-fp-key")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first request status = %d, want 201", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{"amount":999}`))
	req2.Header.Set("Idempotency-Key", "no-fp-key")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Errorf("mutated retry status = %d, want 201 (opt-out replays cache)", rec2.Code)
	}
	if calls != 1 {
		t.Errorf("inner called %d times, want 1 (replay must skip handler)", calls)
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

type postHandlerContextKey struct{}

type contextRecordingStore struct {
	inner  idem.Store
	value  any
	ctxErr error
}

func newContextRecordingStore() *contextRecordingStore {
	return &contextRecordingStore{inner: idem.NewMemoryStore()}
}

func (s *contextRecordingStore) Get(ctx context.Context, key string, fingerprint []byte) (*idem.CachedResponse, bool, error) {
	return s.inner.Get(ctx, key, fingerprint)
}

func (s *contextRecordingStore) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	return s.inner.TryLock(ctx, key, fingerprint, ttl)
}

func (s *contextRecordingStore) Set(ctx context.Context, key, token string, resp idem.CachedResponse, ttl time.Duration) error {
	s.value = ctx.Value(postHandlerContextKey{})
	s.ctxErr = ctx.Err()
	return s.inner.Set(ctx, key, token, resp, ttl)
}

func (s *contextRecordingStore) Unlock(ctx context.Context, key, token string) error {
	return s.inner.Unlock(ctx, key, token)
}

func TestPostHandlerContextPreservesRequestValuesAfterCancellation(t *testing.T) {
	store := newContextRecordingStore()
	handler := Middleware(store, WithAllowSharedKeys())(newTestHandler("ok", http.StatusOK))

	parent := context.WithValue(context.Background(), postHandlerContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	req.Header.Set("Idempotency-Key", "context-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.value != "trace-123" {
		t.Fatalf("post-handler context value = %v, want trace-123", store.value)
	}
	if store.ctxErr != nil {
		t.Fatalf("post-handler context inherited cancellation: %v", store.ctxErr)
	}
}

func TestWithPostHandlerTimeout_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for non-positive timeout")
		}
		if strings.Contains(r.(string), "0s") {
			t.Fatalf("panic leaked invalid duration: %q", r)
		}
	}()
	WithPostHandlerTimeout(0)
}

func TestOptions_PanicOnInvalidInput(t *testing.T) {
	store := idem.NewMemoryStore()
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "nil middleware option", fn: func() { Middleware(store, nil) }},
		{name: "nil metrics", fn: func() { WithMetrics(nil) }},
		{name: "invalid method", fn: func() { WithRequiredMethods("bad method") }},
		{name: "nil user extractor", fn: func() { WithUserExtractor(nil) }},
		{name: "empty semantic header", fn: func() { WithSemanticHeaders("") }},
		{name: "empty preserve header", fn: func() { WithPreserveHeaders("") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			tt.fn()
		})
	}
}

func TestWithPreserveHeaders_ClonesInput(t *testing.T) {
	names := []string{"X-Replayable"}
	opt := WithPreserveHeaders(names...)
	names[0] = "X-Mutated"

	cfg := defaultConfig()
	opt(&cfg)

	if !cfg.preserveHeaders["X-Replayable"] {
		t.Fatalf("preserveHeaders missing original header: %v", cfg.preserveHeaders)
	}
	if cfg.preserveHeaders["X-Mutated"] {
		t.Fatalf("preserveHeaders retained mutated header: %v", cfg.preserveHeaders)
	}
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

func mustFingerprintKey(t *testing.T, r *http.Request, rawKey, userID string, semanticHeaders []string) string {
	t.Helper()
	key, err := fingerprintKey(r, rawKey, userID, semanticHeaders)
	if err != nil {
		t.Fatalf("fingerprintKey: %v", err)
	}
	return key
}

// FR-029 [HIGH]: Two requests sharing the same Idempotency-Key and
// body but differing in query string MUST NOT collide on the same
// cache slot. Pre-fix, only method+path+rawKey participated, so
// /pay?dry_run=true and /pay?dry_run=false would replay each other.
func TestFingerprintKey_QueryStringDistinguishesRequests(t *testing.T) {
	r1 := httptest.NewRequest(http.MethodPost, "/pay?dry_run=true", nil)
	r2 := httptest.NewRequest(http.MethodPost, "/pay?dry_run=false", nil)

	k1 := mustFingerprintKey(t, r1, "key-1", "user-a", nil)
	k2 := mustFingerprintKey(t, r2, "key-1", "user-a", nil)
	if k1 == k2 {
		t.Fatal("expected distinct fingerprints for differing query strings")
	}
}

// FR-029 [HIGH]: query parameter ORDER must not change the
// fingerprint — ?b=1&a=2 and ?a=2&b=1 are semantically identical
// and must hash to the same slot, otherwise legitimate retries from
// clients that re-serialise their query strings would be treated
// as new operations.
func TestFingerprintKey_QueryStringOrderingEquivalent(t *testing.T) {
	r1 := httptest.NewRequest(http.MethodPost, "/orders?b=1&a=2", nil)
	r2 := httptest.NewRequest(http.MethodPost, "/orders?a=2&b=1", nil)

	k1 := mustFingerprintKey(t, r1, "key-1", "user-a", nil)
	k2 := mustFingerprintKey(t, r2, "key-1", "user-a", nil)
	if k1 != k2 {
		t.Fatalf("expected identical fingerprints for query reorderings, got %s vs %s", k1, k2)
	}
}

func TestFingerprintKey_EscapedPathDistinguishesEncodedDelimiters(t *testing.T) {
	r1 := httptest.NewRequest(http.MethodPost, "/objects/a%2Fb", nil)
	r2 := httptest.NewRequest(http.MethodPost, "/objects/a/b", nil)
	if r1.URL.EscapedPath() == r2.URL.EscapedPath() {
		t.Fatalf("test setup expected distinct escaped paths, got %q", r1.URL.EscapedPath())
	}

	k1 := mustFingerprintKey(t, r1, "key-1", "user-a", nil)
	k2 := mustFingerprintKey(t, r2, "key-1", "user-a", nil)
	if k1 == k2 {
		t.Fatal("expected distinct fingerprints for encoded slash vs literal path separator")
	}
}

// FR-029 [HIGH]: when the operator opts a header into the
// fingerprint, two requests differing only in that header MUST hash
// to distinct slots. Reverse direction: the same header value (and
// header omitted entirely) must not invent collisions on its own.
func TestFingerprintKey_SemanticHeadersDistinguishRequests(t *testing.T) {
	r1 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	r1.Header.Set("X-Tenant-Id", "tenant-a")
	r2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	r2.Header.Set("X-Tenant-Id", "tenant-b")

	headers := []string{"X-Tenant-Id"}
	k1 := mustFingerprintKey(t, r1, "key-1", "", headers)
	k2 := mustFingerprintKey(t, r2, "key-1", "", headers)
	if k1 == k2 {
		t.Fatal("expected distinct fingerprints for differing X-Tenant-Id values")
	}

	// Same value across two requests: same fingerprint.
	r3 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	r3.Header.Set("X-Tenant-Id", "tenant-a")
	k3 := mustFingerprintKey(t, r3, "key-1", "", headers)
	if k1 != k3 {
		t.Fatalf("expected identical fingerprints for same X-Tenant-Id, got %s vs %s", k1, k3)
	}
}

// Header name matching is case-insensitive on the wire — confirm
// that the configured "X-Tenant-Id" matches a request that sent
// "x-tenant-id".
func TestFingerprintKey_SemanticHeadersAreCaseInsensitive(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/orders", nil)
	r.Header.Set("x-tenant-id", "tenant-a") // lowercased
	headers := []string{"X-Tenant-Id"}      // mixed
	k := mustFingerprintKey(t, r, "key-1", "", headers)

	rRef := httptest.NewRequest(http.MethodPost, "/orders", nil)
	rRef.Header.Set("X-Tenant-Id", "tenant-a")
	kRef := mustFingerprintKey(t, rRef, "key-1", "", headers)

	if k != kRef {
		t.Fatalf("expected case-insensitive header match, got %s vs %s", k, kRef)
	}
}

func TestFingerprintKey_RejectsAmbiguousSemanticHeaders(t *testing.T) {
	headers := []string{"X-Tenant-Id"}
	cases := []struct {
		name  string
		setup func(*http.Request)
	}{
		{
			name:  "missing",
			setup: func(*http.Request) {},
		},
		{
			name: "blank",
			setup: func(r *http.Request) {
				r.Header.Set("X-Tenant-Id", " \t ")
			},
		},
		{
			name: "duplicate",
			setup: func(r *http.Request) {
				r.Header.Add("X-Tenant-Id", "tenant-a")
				r.Header.Add("X-Tenant-Id", "tenant-b")
			},
		},
		{
			name: "comma alias source",
			setup: func(r *http.Request) {
				r.Header.Add("X-Tenant-Id", "tenant-a,tenant-b")
				r.Header.Add("X-Tenant-Id", "tenant-c")
			},
		},
		{
			name: "edge whitespace",
			setup: func(r *http.Request) {
				r.Header.Set("X-Tenant-Id", " tenant-a")
			},
		},
		{
			name: "control",
			setup: func(r *http.Request) {
				r.Header.Set("X-Tenant-Id", "tenant-a\n")
			},
		},
		{
			name: "invalid utf8",
			setup: func(r *http.Request) {
				r.Header.Set("X-Tenant-Id", string([]byte{'t', 0xff}))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/orders", nil)
			tc.setup(r)
			key, err := fingerprintKey(r, "key-1", "", headers)
			if err == nil {
				t.Fatalf("expected semantic header error, got key %q", key)
			}
			if strings.Contains(err.Error(), "X-Tenant-Id") {
				t.Fatalf("semantic header error leaked header name: %q", err.Error())
			}
		})
	}
}

func TestOptionalSingletonHeaderValueDoesNotEchoHeaderName(t *testing.T) {
	headerName := "X-Secret-Token-Header"

	t.Run("duplicate", func(t *testing.T) {
		h := http.Header{}
		h.Add(headerName, "one")
		h.Add(headerName, "two")

		_, err := optionalSingletonHeaderValue(h, headerName)
		if err == nil {
			t.Fatal("expected duplicate header error")
		}
		if strings.Contains(err.Error(), headerName) || strings.Contains(err.Error(), "Secret-Token") {
			t.Fatalf("error leaked header name: %q", err.Error())
		}
	})

	t.Run("invalid value", func(t *testing.T) {
		h := http.Header{}
		h.Set(headerName, "bad\nvalue")

		_, err := optionalSingletonHeaderValue(h, headerName)
		if err == nil {
			t.Fatal("expected invalid header value error")
		}
		if strings.Contains(err.Error(), headerName) || strings.Contains(err.Error(), "Secret-Token") {
			t.Fatalf("error leaked header name: %q", err.Error())
		}
	})
}

// End-to-end: when WithSemanticHeaders is configured, a second
// request with a different tenant header MUST NOT be served from
// the first request's cache. This is the practical attack the
// audit flagged: a tenant-A reply leaking into a tenant-B request.
func TestMiddleware_SemanticHeaders_PreventsCrossTenantReplay(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tenant":"` + r.Header.Get("X-Tenant-Id") + `"}`))
	})
	handler := Middleware(store,
		WithAllowSharedKeys(),
		WithoutBodyFingerprint(),
		WithSemanticHeaders("X-Tenant-Id"),
	)(inner)

	// Tenant A request.
	r1 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	r1.Header.Set("Idempotency-Key", "shared-key")
	r1.Header.Set("X-Tenant-Id", "tenant-a")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)
	if got := w1.Body.String(); got != `{"tenant":"tenant-a"}` {
		t.Fatalf("first response body = %q, want tenant-a payload", got)
	}

	// Tenant B request, same idempotency key — must NOT replay tenant A.
	r2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	r2.Header.Set("Idempotency-Key", "shared-key")
	r2.Header.Set("X-Tenant-Id", "tenant-b")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)
	if got := w2.Body.String(); got != `{"tenant":"tenant-b"}` {
		t.Fatalf("second response body = %q, want tenant-b payload (cross-tenant replay regression)", got)
	}
	if calls != 2 {
		t.Errorf("inner handler called %d times, expected 2 (one per tenant)", calls)
	}
}

func TestMiddleware_SemanticHeaders_RejectsMissingBlankOrDuplicate(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(store,
		WithAllowSharedKeys(),
		WithoutBodyFingerprint(),
		WithSemanticHeaders("X-Tenant-Id"),
	)(inner)

	cases := []struct {
		name  string
		setup func(*http.Request)
	}{
		{
			name:  "missing",
			setup: func(*http.Request) {},
		},
		{
			name: "blank",
			setup: func(r *http.Request) {
				r.Header.Set("X-Tenant-Id", " \t ")
			},
		},
		{
			name: "duplicate",
			setup: func(r *http.Request) {
				r.Header.Add("X-Tenant-Id", "tenant-a")
				r.Header.Add("X-Tenant-Id", "tenant-b")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/orders", nil)
			r.Header.Set("Idempotency-Key", "shared-key-"+tc.name)
			tc.setup(r)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("inner handler called %d times, want 0", calls)
	}
}

// Without WithSemanticHeaders, the kit's default is conservative:
// only method/path/query/raw-key/user participate. This is the
// behaviour the audit flagged as unsafe for multi-tenant routing
// headers — the regression test exists so we don't accidentally
// ALSO regress the opt-in default to "everything goes in the key".
func TestMiddleware_NoSemanticHeaders_DoesNotAccidentallyDifferentiate(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	handler := Middleware(store, WithAllowSharedKeys(), WithoutBodyFingerprint())(inner)

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "/orders", nil)
		r.Header.Set("Idempotency-Key", "shared-key")
		// Header that is NOT configured as semantic — must not
		// participate in fingerprint, second request should replay.
		r.Header.Set("X-Whatever", "different-value")
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	if calls != 1 {
		t.Errorf("inner handler called %d times, expected 1 (cache should replay)", calls)
	}
}

func TestWithSemanticHeaders_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on invalid header name")
		}
	}()
	_ = WithSemanticHeaders("not a valid header name with spaces")
}

func TestWithSemanticHeaders_PanicsOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty header name")
		}
	}()
	_ = WithSemanticHeaders("")
}

func TestOptionPanicsDoNotReflectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "header", fn: func() { WithHeader("bad header secret-token") }},
		{name: "method", fn: func() { WithRequiredMethods("bad method secret-token") }},
		{name: "semantic", fn: func() { WithSemanticHeaders("bad header secret-token") }},
		{name: "preserve", fn: func() { WithPreserveHeaders("bad header secret-token") }},
		{name: "ttl", fn: func() { WithTTL(-123 * time.Second) }},
		{name: "post handler timeout", fn: func() { WithPostHandlerTimeout(-456 * time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				rec := recover()
				if rec == nil {
					t.Fatal("expected panic")
				}
				msg, ok := rec.(string)
				if !ok {
					t.Fatalf("panic = %T, want string", rec)
				}
				if strings.Contains(msg, "secret-token") || strings.Contains(msg, "123") || strings.Contains(msg, "456") {
					t.Fatalf("panic leaked invalid value: %q", msg)
				}
			}()
			tt.fn()
		})
	}
}

func TestWithSemanticHeaders_CanonicalizesNames(t *testing.T) {
	cfg := defaultConfig()
	WithSemanticHeaders("X-Tenant-Id")(&cfg)
	if len(cfg.semanticHeaders) != 1 || cfg.semanticHeaders[0] != "X-Tenant-Id" {
		t.Fatalf("expected single canonicalised header, got %v", cfg.semanticHeaders)
	}
}
