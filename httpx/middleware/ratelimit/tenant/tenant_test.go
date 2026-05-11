package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
)

// fakeLimiter counts calls per key and denies once a per-key cap is
// reached. Keeps the test independent from the algorithmic details of
// any real Limiter implementation.
type fakeLimiter struct {
	mu      sync.Mutex
	limit   int
	counts  map[string]int
	retry   time.Duration
	wantErr error
}

func newFakeLimiter(limit int) *fakeLimiter {
	return &fakeLimiter{limit: limit, counts: make(map[string]int)}
}

func (f *fakeLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wantErr != nil {
		return false, 0, f.wantErr
	}
	f.counts[key]++
	if f.counts[key] > f.limit {
		return false, f.retry, nil
	}
	return true, 0, nil
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func reqWithTenant(t *testing.T, id string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if id != "" {
		r = r.WithContext(coretenant.WithID(r.Context(), coretenant.ID(id)))
	}
	return r
}

func TestNew_AllowsUnderLimit(t *testing.T) {
	mw := New(newFakeLimiter(3))
	h := mw(okHandler())

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, reqWithTenant(t, "acme"))
		require.Equal(t, http.StatusOK, rec.Code, "request %d should pass", i+1)
	}
}

func TestNew_BlocksWhenTenantBudgetExhausted(t *testing.T) {
	lim := newFakeLimiter(1)
	lim.retry = 5 * time.Second
	mw := New(lim)
	h := mw(okHandler())

	// First request burns the budget.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "acme"))
	require.Equal(t, http.StatusOK, rec.Code)

	// Second request must be 429 with the tenant-scope marker.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "acme"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "tenant", rec.Header().Get("X-RateLimit-Scope"))
	assert.Equal(t, "5", rec.Header().Get("Retry-After"))
	assertJSONError(t, rec, "tenant rate limit exceeded")
}

func TestNew_TenantsAreIsolated(t *testing.T) {
	mw := New(newFakeLimiter(1))
	h := mw(okHandler())

	// Tenant A burns its budget.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "acme"))
	require.Equal(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "acme"))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Tenant B has its own budget and must still pass.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "widgets"))
	assert.Equal(t, http.StatusOK, rec.Code,
		"tenant B should not be affected by tenant A's exhaustion")
}

func TestNew_MissingTenantReturns400(t *testing.T) {
	mw := New(newFakeLimiter(10))
	h := mw(okHandler())

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"missing tenant should return 400; tenant middleware must run upstream")
	assertJSONError(t, rec, "tenant: required tenant ID is missing")
}

func TestNew_LimiterErrorReturns500(t *testing.T) {
	lim := newFakeLimiter(10)
	lim.wantErr = errors.New("backend down")
	mw := New(lim)
	h := mw(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "acme"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"limiter backend error must not silently fail-open")
	assertJSONError(t, rec, "rate limit check failed")
}

func TestNew_PanicsOnNilLimiter(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil limiter")
		}
	}()
	New(nil)
}

func TestNew_NoRetryAfterWhenLimiterDoesntKnow(t *testing.T) {
	lim := newFakeLimiter(0) // every call is denied
	mw := New(lim)
	h := mw(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithTenant(t, "acme"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "tenant", rec.Header().Get("X-RateLimit-Scope"))
	// Limiter returned retryAfter=0 (no opinion); middleware must not
	// fabricate a Retry-After header.
	assert.Empty(t, rec.Header().Get("Retry-After"))
	assertJSONError(t, rec, "tenant rate limit exceeded")
}

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, want, body.Error)
	assert.NotEmpty(t, body.Code)
}
