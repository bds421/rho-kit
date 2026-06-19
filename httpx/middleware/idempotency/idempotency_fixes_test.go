package idempotency

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	idem "github.com/bds421/rho-kit/data/v2/idempotency"
)

// TestHeadersSetButNotWritten_FirstCallerAndCacheAgree covers the finding that
// a handler which sets headers via w.Header() but returns WITHOUT calling
// Write/WriteHeader sends an implicit 200 with no custom headers to the first
// caller, yet the post-handler snapshot caches those headers — so a replay
// would carry headers the original response never emitted. The first caller's
// observed headers and the replayed headers MUST agree.
func TestHeadersSetButNotWritten_FirstCallerAndCacheAgree(t *testing.T) {
	store := idem.NewMemoryStore()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Set a custom header but return without Write/WriteHeader.
		w.Header().Set("X-Custom", "value")
	})
	handler := Middleware(store, WithAllowSharedKeys())(inner)

	req1 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req1.Header.Set("Idempotency-Key", "header-implicit")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	// The first caller must actually receive the header the handler set.
	if got := rec1.Header().Get("X-Custom"); got != "value" {
		t.Fatalf("first caller X-Custom = %q, want %q", got, "value")
	}

	// Replay must carry exactly the same header the first caller saw.
	req2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req2.Header.Set("Idempotency-Key", "header-implicit")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if got := rec2.Header().Get("X-Custom"); got != rec1.Header().Get("X-Custom") {
		t.Fatalf("replay X-Custom = %q, first caller = %q; cache diverged from sent response",
			got, rec1.Header().Get("X-Custom"))
	}
}

// failingGetStore returns a sentinel error from Get so we can assert the
// middleware logs the underlying cause per the WithLogger contract.
type failingGetStore struct{ err error }

func (s failingGetStore) Get(_ context.Context, _ string, _ []byte) (*idem.CachedResponse, bool, error) {
	return nil, false, s.err
}

func (s failingGetStore) TryLock(_ context.Context, _ string, _ []byte, _ time.Duration) (string, bool, bool, error) {
	return "tok", false, true, nil
}

func (s failingGetStore) Set(_ context.Context, _, _ string, _ idem.CachedResponse, _ time.Duration) error {
	return nil
}

func (s failingGetStore) Unlock(_ context.Context, _, _ string) error { return nil }

// failingTryLockStore returns a sentinel error from TryLock.
type failingTryLockStore struct{ err error }

func (s failingTryLockStore) Get(_ context.Context, _ string, _ []byte) (*idem.CachedResponse, bool, error) {
	return nil, false, nil
}

func (s failingTryLockStore) TryLock(_ context.Context, _ string, _ []byte, _ time.Duration) (string, bool, bool, error) {
	return "", false, false, s.err
}

func (s failingTryLockStore) Set(_ context.Context, _, _ string, _ idem.CachedResponse, _ time.Duration) error {
	return nil
}

func (s failingTryLockStore) Unlock(_ context.Context, _, _ string) error { return nil }

// TestStoreErrorsAreLogged verifies that Get and TryLock backend errors are
// surfaced through the configured logger (the WithLogger contract states it
// "sets the logger for idempotency store errors"). Pre-fix these paths
// returned a bare 500 and discarded the underlying error entirely.
func TestStoreErrorsAreLogged(t *testing.T) {
	sentinel := errors.New("backend exploded")

	tests := []struct {
		name  string
		store idem.Store
	}{
		{name: "Get error logged", store: failingGetStore{err: sentinel}},
		{name: "TryLock error logged", store: failingTryLockStore{err: sentinel}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			handler := Middleware(tc.store, WithAllowSharedKeys(), WithLogger(logger))(
				newTestHandler("ok", http.StatusOK))

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set("Idempotency-Key", "logged-key")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
			}
			out := buf.String()
			if out == "" {
				t.Fatal("expected store error to be logged, got no log output")
			}
			if !strings.Contains(out, "ERROR") {
				t.Errorf("expected an ERROR-level log, got %q", out)
			}
		})
	}
}

// TestStoreErrorLogDoesNotLeakRawKey ensures the store-error log path follows
// the package's redaction convention and never emits the raw idempotency key.
func TestStoreErrorLogDoesNotLeakRawKey(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := Middleware(failingGetStore{err: errors.New("boom")},
		WithAllowSharedKeys(), WithLogger(logger))(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "tenant-secret-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if strings.Contains(buf.String(), "tenant-secret-key") {
		t.Fatalf("store-error log leaked raw key: %q", buf.String())
	}
}

// TestMaxBytesError_Returns413 covers the finding that an *http.MaxBytesError
// from an upstream maxbody (http.MaxBytesReader) cap below the fingerprint
// limit must surface as 413 Payload Too Large, not a generic 400, and must not
// be miscounted as a store error.
func TestMaxBytesError_Returns413(t *testing.T) {
	store := idem.NewMemoryStore()
	metrics := NewMetrics(WithRegisterer(prometheus.NewRegistry()))
	handler := Middleware(store, WithAllowSharedKeys(), WithMetrics(metrics))(
		newTestHandler("ok", http.StatusOK))

	// Wrap the body with a small MaxBytesReader, exactly as the maxbody
	// middleware does upstream.
	body := strings.NewReader(strings.Repeat("x", 64))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Idempotency-Key", "too-large")
	rec := httptest.NewRecorder()
	req.Body = http.MaxBytesReader(rec, req.Body, 8)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (413)", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if got := testutil.ToFloat64(metrics.errors); got != 0 {
		t.Errorf("store errors counter = %v, want 0 (client read failure must not count as a store error)", got)
	}
}

// ttlRecordingStore records the TTLs passed to TryLock and Set so tests can
// assert the middleware uses distinct lock/cache TTLs.
type ttlRecordingStore struct {
	inner   idem.Store
	lockTTL time.Duration
	setTTL  time.Duration
}

func (s *ttlRecordingStore) Get(ctx context.Context, key string, fp []byte) (*idem.CachedResponse, bool, error) {
	return s.inner.Get(ctx, key, fp)
}

func (s *ttlRecordingStore) TryLock(ctx context.Context, key string, fp []byte, ttl time.Duration) (string, bool, bool, error) {
	s.lockTTL = ttl
	return s.inner.TryLock(ctx, key, fp, ttl)
}

func (s *ttlRecordingStore) Set(ctx context.Context, key, token string, resp idem.CachedResponse, ttl time.Duration) error {
	s.setTTL = ttl
	return s.inner.Set(ctx, key, token, resp, ttl)
}

func (s *ttlRecordingStore) Unlock(ctx context.Context, key, token string) error {
	return s.inner.Unlock(ctx, key, token)
}

// TestWithLockTTL_SeparateFromCacheTTL verifies the additive WithLockTTL option
// passes a shorter TTL to TryLock than the response-cache TTL passed to Set, so
// a crash mid-handler does not lock the key out for the full cache TTL.
func TestWithLockTTL_SeparateFromCacheTTL(t *testing.T) {
	rec := &ttlRecordingStore{inner: idem.NewMemoryStore()}
	handler := Middleware(rec,
		WithAllowSharedKeys(),
		WithTTL(24*time.Hour),
		WithLockTTL(30*time.Second),
	)(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "lock-ttl")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if rec.lockTTL != 30*time.Second {
		t.Errorf("TryLock ttl = %v, want 30s (the lock TTL)", rec.lockTTL)
	}
	if rec.setTTL != 24*time.Hour {
		t.Errorf("Set ttl = %v, want 24h (the cache TTL)", rec.setTTL)
	}
}

// TestLockTTL_DefaultsToCacheTTL verifies that without WithLockTTL the
// behaviour is unchanged: the lock TTL equals the cache TTL.
func TestLockTTL_DefaultsToCacheTTL(t *testing.T) {
	rec := &ttlRecordingStore{inner: idem.NewMemoryStore()}
	handler := Middleware(rec,
		WithAllowSharedKeys(),
		WithTTL(2*time.Hour),
	)(newTestHandler("ok", http.StatusOK))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Idempotency-Key", "lock-ttl-default")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if rec.lockTTL != 2*time.Hour {
		t.Errorf("TryLock ttl = %v, want 2h (default = cache TTL)", rec.lockTTL)
	}
}

// TestWithLockTTL_PanicsOnNonPositive matches the package convention of
// failing fast on invalid durations.
func TestWithLockTTL_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for non-positive lock TTL")
		}
	}()
	WithLockTTL(0)
}

// TestWithUncachedStatuses_UnlocksInsteadOfCaching verifies the additive
// WithUncachedStatuses option releases the lock (so a retry can recover)
// instead of caching a transient 5xx response for the full TTL.
func TestWithUncachedStatuses_UnlocksInsteadOfCaching(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway) // transient backend failure
			_, _ = w.Write([]byte("upstream down"))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("recovered"))
	})
	handler := Middleware(store,
		WithAllowSharedKeys(),
		WithUncachedStatuses(http.StatusBadGateway, http.StatusServiceUnavailable),
	)(inner)

	// First request: 502, must NOT be cached and the lock must be released.
	req1 := httptest.NewRequest(http.MethodPost, "/pay", nil)
	req1.Header.Set("Idempotency-Key", "transient")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusBadGateway {
		t.Fatalf("first status = %d, want 502", rec1.Code)
	}

	// Second request with same key must re-run the handler and recover.
	req2 := httptest.NewRequest(http.MethodPost, "/pay", nil)
	req2.Header.Set("Idempotency-Key", "transient")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second status = %d, want 201 (must recover from transient 502, not replay it)", rec2.Code)
	}
	if calls != 2 {
		t.Errorf("handler called %d times, want 2", calls)
	}
}

// TestWithUncachedStatuses_CachesNonMatchingStatuses confirms statuses not in
// the uncached set are still cached and replayed normally.
func TestWithUncachedStatuses_CachesNonMatchingStatuses(t *testing.T) {
	store := idem.NewMemoryStore()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	})
	handler := Middleware(store,
		WithAllowSharedKeys(),
		WithUncachedStatuses(http.StatusBadGateway),
	)(inner)

	req1 := httptest.NewRequest(http.MethodPost, "/pay", nil)
	req1.Header.Set("Idempotency-Key", "cacheable")
	handler.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodPost, "/pay", nil)
	req2.Header.Set("Idempotency-Key", "cacheable")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if calls != 1 {
		t.Errorf("handler called %d times, want 1 (201 should be cached/replayed)", calls)
	}
	if rec2.Code != http.StatusCreated {
		t.Errorf("replayed status = %d, want 201", rec2.Code)
	}
}

// TestWithUncachedStatuses_PanicsOnInvalid matches the fail-fast convention.
func TestWithUncachedStatuses_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid status code")
		}
	}()
	WithUncachedStatuses(42) // not a valid HTTP status
}
