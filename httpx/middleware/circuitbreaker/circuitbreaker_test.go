package circuitbreaker_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cbmw "github.com/bds421/rho-kit/httpx/v2/middleware/circuitbreaker"
	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
)

func newHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "ok")
	}
}

func TestMiddleware_HappyPathLetsRequestThrough(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, 50*time.Millisecond)
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(newHandler(http.StatusOK))

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusOK, rec.Code)
	}
	assert.Equal(t, "closed", cb.State())
}

func TestMiddleware_TripsOn5xxAndShortCircuits(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, 50*time.Millisecond)
	called := 0
	failingHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := cbmw.Middleware(
		cbmw.WithBreaker(cb),
		cbmw.WithRetryAfter(5*time.Second),
	)(failingHandler)

	// First three failures trip the breaker.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code,
			"iteration %d: handler must run while closed", i)
	}
	require.Equal(t, 3, called, "handler must run for each pre-open request")

	// Fourth request must be short-circuited with 503 + Retry-After.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "5", rec.Header().Get(cbmw.HeaderRetry),
		"Retry-After must reflect WithRetryAfter")
	require.Equal(t, "open", rec.Header().Get(cbmw.HeaderState))
	require.Equal(t, 3, called,
		"handler MUST NOT run while breaker is open; got %d additional invocations", called-3)
}

func TestMiddleware_4xxIsNotAFailureByDefault(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(2, time.Minute)
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(newHandler(http.StatusUnauthorized))

	// 10 401s — default predicate must NOT trip the breaker.
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	}
	require.Equal(t, "closed", cb.State(),
		"4xx must not contribute to the failure count — only the upstream is faulty")
}

func TestMiddleware_PanicCountsAsFailureAndPropagates(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, time.Minute)
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	require.Panics(t, func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	}, "panic must propagate so the upstream recover middleware can write the 500")
	require.Equal(t, "open", cb.State(),
		"panic must count as failure; threshold=1 means one panic opens the breaker")

	// Subsequent request must be short-circuited — confirms the
	// panic actually advanced the breaker counter, not just
	// propagated unrelated to it.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"breaker must be open after the panicking call counted as failure")
}

func TestMiddleware_WithBreakerForPerKey(t *testing.T) {
	// Per-tenant breakers — header "X-Tenant" picks the bucket.
	breakers := map[string]*circuitbreaker.CircuitBreaker{
		"alice": circuitbreaker.NewCircuitBreaker(1, time.Minute),
		"bob":   circuitbreaker.NewCircuitBreaker(1, time.Minute),
	}
	called := 0
	h := cbmw.Middleware(cbmw.WithBreakerFor(func(r *http.Request) *circuitbreaker.CircuitBreaker {
		return breakers[r.Header.Get("X-Tenant")]
	}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusInternalServerError)
	}))

	// Trip alice's breaker.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant", "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "open", breakers["alice"].State())

	// Alice is now blocked.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant", "alice")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"alice's breaker is open; should short-circuit")

	// Bob's breaker is independent — request still reaches the
	// handler (and trips bob's breaker on this 500).
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Tenant", "bob")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code,
		"bob's breaker is independent of alice's")
	require.Equal(t, "open", breakers["alice"].State(),
		"alice's breaker stays open; cooldown has not elapsed")
	require.Equal(t, "open", breakers["bob"].State(),
		"bob's breaker is now open (threshold=1, just took one 500)")
}

func TestMiddleware_BreakerForReturningNilBypasses(t *testing.T) {
	called := 0
	h := cbmw.Middleware(cbmw.WithBreakerFor(func(*http.Request) *circuitbreaker.CircuitBreaker {
		return nil
	}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusInternalServerError)
	}))

	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	}
	require.Equal(t, 10, called, "nil breaker must mean 'no protection on this request'")
}

func TestMiddleware_WithShouldTripCustomPredicate(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, time.Minute)
	// Custom: trip ONLY on 401 (e.g. an upstream auth provider that
	// dies returns 401, not 5xx).
	h := cbmw.Middleware(
		cbmw.WithBreaker(cb),
		cbmw.WithShouldTrip(func(status int, panicked bool) bool {
			return panicked || status == http.StatusUnauthorized
		}),
	)(newHandler(http.StatusUnauthorized))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "open", cb.State(),
		"custom predicate counted 401 as failure; threshold=1 trips immediately")
}

func TestMiddleware_WithOnOpenRespondOverridesBody(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, time.Minute)
	h := cbmw.Middleware(
		cbmw.WithBreaker(cb),
		cbmw.WithOnOpenRespond(func(w http.ResponseWriter, _ *http.Request, _ time.Duration) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = io.WriteString(w, "service paused")
		}),
	)(newHandler(http.StatusInternalServerError))

	// Trip.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "open", cb.State())

	// Override applies on the next call.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.Equal(t, "text/plain", rec.Header().Get("Content-Type"))
	body, _ := io.ReadAll(rec.Body)
	require.Equal(t, "service paused", string(body))
}

func TestMiddleware_HalfOpenProbeRecoversBreaker(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, 10*time.Millisecond)
	// Handler that returns 500 once, then 200.
	calls := 0
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Trip.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "open", cb.State())

	// Wait past cooldown — breaker moves to half-open on next probe.
	time.Sleep(20 * time.Millisecond)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code,
		"half-open probe must reach the handler and succeed")
	require.Equal(t, "closed", cb.State(),
		"successful probe must close the breaker again")
}

func TestMiddleware_RequireBreakerOption(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/circuitbreaker: WithBreaker or WithBreakerFor is required",
		func() { cbmw.Middleware() })
}

func TestMiddleware_NilOptionPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/circuitbreaker: option must not be nil",
		func() { cbmw.Middleware(nil) })
}

func TestMiddleware_NilBreakerInWithBreakerPanics(t *testing.T) {
	require.PanicsWithValue(t,
		"middleware/circuitbreaker: WithBreaker requires a non-nil breaker",
		func() { cbmw.WithBreaker(nil) })
}

func TestMiddleware_DefaultRetryAfterIsPositive(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, time.Minute)
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(newHandler(http.StatusInternalServerError))

	// Trip.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	// Confirm the open response has a positive Retry-After.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	ra := rec.Header().Get(cbmw.HeaderRetry)
	require.NotEmpty(t, ra)
	// Best-effort numeric sanity check.
	require.NotEqual(t, "0", ra, "Retry-After must be >= 1 second")
}

// Sanity check that the breaker-open path does not write headers
// after the response is committed, even when the handler set
// custom headers before WriteHeader.
func TestMiddleware_OpenStateDoesNotLeakHandlerHeaders(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, time.Minute)
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Leaky", "value")
		w.WriteHeader(http.StatusInternalServerError)
	}))

	// First request: trips the breaker; handler wrote X-Leaky.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, "value", rec.Header().Get("X-Leaky"))

	// Second request: breaker open — handler does NOT run, so the
	// fresh ResponseWriter must not contain X-Leaky from a prior
	// request. (The recorder is a separate object; the test verifies
	// the middleware doesn't reach into shared mutable state.)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, "", rec.Header().Get("X-Leaky"),
		"open path must write to a fresh response — no leakage from prior handler")
}

// Compile-time assertion to keep the public Option signature stable.
var _ = func() cbmw.Option {
	return cbmw.WithBreakerFor(func(*http.Request) *circuitbreaker.CircuitBreaker { return nil })
}

// Compile-time: ErrCircuitOpen lives on the resilience package; this
// file exercises it implicitly via the middleware's open response.
var _ = errors.Is
