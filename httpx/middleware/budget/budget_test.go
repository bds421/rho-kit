package budget_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/tenant"
	databudget "github.com/bds421/rho-kit/data/v2/budget"
	mw "github.com/bds421/rho-kit/httpx/v2/middleware/budget"
)

// fakeBudget is a hand-rolled stub so the middleware tests don't
// pull in a real backend. Each Consume call records the (key, amount)
// pair and returns the configured response.
type fakeBudget struct {
	allowed   bool
	remaining int64
	retry     time.Duration
	err       error
	calls     []call
}

type call struct {
	key    string
	amount int64
}

func (f *fakeBudget) Consume(_ context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	f.calls = append(f.calls, call{key, amount})
	return f.allowed, f.remaining, f.retry, f.err
}

func (f *fakeBudget) Peek(_ context.Context, _ string) (int64, error) {
	return f.remaining, nil
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_PanicsOnNilBudget(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil budget")
		}
	}()
	mw.Middleware(nil)
}

func TestMiddleware_PanicsOnNegativeAmount(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on negative amount")
		}
	}()
	mw.Middleware(&fakeBudget{allowed: true}, mw.WithAmount(-1))
}

func TestMiddleware_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	mw.Middleware(&fakeBudget{allowed: true}, nil)
}

func TestWithKeyFunc_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil key func")
		}
	}()
	mw.WithKeyFunc(nil)
}

func TestWithScope_PanicsOnInvalidHeaderValue(t *testing.T) {
	assert.PanicsWithValue(t,
		"middleware/budget: WithScope requires a valid HTTP header value",
		func() { mw.WithScope("tenant\r\nX-Injected: secret-token") },
	)
}

func TestMiddleware_AllowsWhenBudgetAdmits(t *testing.T) {
	b := &fakeBudget{allowed: true, remaining: 99}
	h := mw.Middleware(b, mw.WithKeyFunc(staticKey("alice")))(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, b.calls, 1)
	assert.Equal(t, call{"alice", 1}, b.calls[0],
		"default amount is 1 and key comes from KeyFunc")
}

func TestMiddleware_RejectsWith429AndHeaders(t *testing.T) {
	b := &fakeBudget{allowed: false, remaining: 0, retry: 30 * time.Second}
	h := mw.Middleware(b,
		mw.WithKeyFunc(staticKey("alice")),
		mw.WithScope("tokens-per-hour"),
	)(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "tokens-per-hour", rec.Header().Get(mw.HeaderScope))
	assert.Equal(t, "0", rec.Header().Get(mw.HeaderRemaining))
	retry, err := strconv.Atoi(rec.Header().Get(mw.HeaderRetry))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, retry, 1, "Retry-After must be at least 1 second")
	assert.LessOrEqual(t, retry, 30)
}

func TestMiddleware_DefaultScopeIsTenant(t *testing.T) {
	// When WithScope is not called, the rejection still carries a
	// dashboard-friendly scope label so operators don't see anonymous
	// 429s. M-8 in v2 audit: matches httpx/middleware/ratelimit/tenant.
	b := &fakeBudget{allowed: false, remaining: 0, retry: time.Second}
	h := mw.Middleware(b, mw.WithKeyFunc(staticKey("alice")))(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	assert.Equal(t, "tenant", rec.Header().Get(mw.HeaderScope))
}

func TestMiddleware_ScopeOverride(t *testing.T) {
	// Explicit scope overrides the tenant default.
	b := &fakeBudget{allowed: false, remaining: 0, retry: time.Second}
	h := mw.Middleware(b,
		mw.WithKeyFunc(staticKey("alice")),
		mw.WithScope("dollars-per-day"),
	)(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	assert.Equal(t, "dollars-per-day", rec.Header().Get(mw.HeaderScope))
}

func TestMiddleware_PassesThroughOnNoKey(t *testing.T) {
	// KeyFunc returns ok=false => no charge, no headers, no upstream call.
	b := &fakeBudget{}
	keyFn := func(*http.Request) (string, bool) { return "", false }
	h := mw.Middleware(b, mw.WithKeyFunc(keyFn), mw.WithAllowMissingKey())(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, b.calls,
		"middleware must not call Consume when KeyFunc returns false")
}

func TestMiddleware_MissingKeyRejectedByDefault(t *testing.T) {
	b := &fakeBudget{}
	keyFn := func(*http.Request) (string, bool) { return "", false }
	h := mw.Middleware(b, mw.WithKeyFunc(keyFn))(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, b.calls, "invalid/missing keys must be rejected before backend use")
	assertJSONError(t, rec, "budget key required")
}

func TestMiddleware_InvalidKeyRejectedBeforeBackend(t *testing.T) {
	b := &fakeBudget{allowed: true}
	h := mw.Middleware(b, mw.WithKeyFunc(staticKey("bad key")))(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, b.calls, "invalid budget keys must not reach backend implementations")
	assertJSONError(t, rec, "invalid budget key")
}

func TestMiddleware_BackendErrorReturns503(t *testing.T) {
	b := &fakeBudget{err: errors.New("redis down")}
	h := mw.Middleware(b, mw.WithKeyFunc(staticKey("alice")))(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"surface backend errors as 503 rather than fail-open")
	assertJSONError(t, rec, "budget unavailable")
}

func TestMiddleware_KeyFuncPanicReturns503(t *testing.T) {
	b := &fakeBudget{allowed: true}
	called := false
	h := mw.Middleware(b, mw.WithKeyFunc(func(*http.Request) (string, bool) {
		panic("key failed")
	}))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.False(t, called)
	assert.Empty(t, b.calls)
	assertJSONError(t, rec, "budget unavailable")
}

func TestMiddleware_AmountOverride(t *testing.T) {
	b := &fakeBudget{allowed: true, remaining: 100}
	h := mw.Middleware(b,
		mw.WithKeyFunc(staticKey("alice")),
		mw.WithAmount(25),
	)(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	require.Len(t, b.calls, 1)
	assert.Equal(t, int64(25), b.calls[0].amount)
}

func TestTenantKeyFunc_ReadsCtx(t *testing.T) {
	// End-to-end: the default key function honours tenant.WithID.
	b := &fakeBudget{allowed: true, remaining: 99}
	h := mw.Middleware(b)(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(tenant.WithID(req.Context(), tenant.ID("acme")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Len(t, b.calls, 1)
	assert.Equal(t, "acme", b.calls[0].key)
}

func TestTenantKeyFunc_PassesWithoutTenant(t *testing.T) {
	// Missing tenant is fail-closed by default: otherwise a missing
	// upstream tenant resolver silently bypasses budget enforcement.
	b := &fakeBudget{}
	h := mw.Middleware(b)(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, b.calls)
}

func TestTenantKeyFunc_PassesWithoutTenantWhenExplicitlyAllowed(t *testing.T) {
	b := &fakeBudget{}
	h := mw.Middleware(b, mw.WithAllowMissingKey())(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, b.calls)
}

// Sentinels are part of the public API and must propagate.
func TestMiddleware_BackendInvalidKeyError(t *testing.T) {
	b := &fakeBudget{err: databudget.ErrInvalidKey}
	h := mw.Middleware(b, mw.WithKeyFunc(staticKey("alice")))(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func staticKey(s string) mw.KeyFunc {
	return func(*http.Request) (string, bool) { return s, true }
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
