package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGateway_HappyPath verifies the canonical chain end-to-end:
// a request with a valid bearer + tenant header reaches the
// downstream and returns its result.
func TestGateway_HappyPath(t *testing.T) {
	gw := newGateway("demo-token-1234567890", callRealDownstream)
	handler := gw.buildHandler(slog.Default())
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/orders", nil)
	req.Header.Set("Authorization", "Bearer demo-token-1234567890")
	req.Header.Set("X-Tenant-Id", "acme")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestGateway_UnauthorizedRejectedBeforeDownstream verifies that
// JWT auth runs before the downstream is invoked. If auth ran
// after, a missing token would still incur a downstream call.
func TestGateway_UnauthorizedRejectedBeforeDownstream(t *testing.T) {
	var downstreamCalled atomic.Int32
	gw := newGateway("demo-token-1234567890", func(_ context.Context, _ string) (string, error) {
		downstreamCalled.Add(1)
		return "", nil
	})
	srv := httptest.NewServer(gw.buildHandler(slog.Default()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/orders", nil)
	req.Header.Set("X-Tenant-Id", "acme")
	// no Authorization header

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, int32(0), downstreamCalled.Load(),
		"downstream must not be invoked when auth fails")
}

// TestGateway_RetryRecoversTransientFailure exercises the retry
// policy: a downstream that fails on the first call and succeeds
// on the second must still return 200 to the client.
func TestGateway_RetryRecoversTransientFailure(t *testing.T) {
	f := &failingDownstream{}
	f.failuresRemaining.Store(1)
	gw := newGateway("demo-token-1234567890", f.call)
	srv := httptest.NewServer(gw.buildHandler(slog.Default()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/orders", nil)
	req.Header.Set("Authorization", "Bearer demo-token-1234567890")
	req.Header.Set("X-Tenant-Id", "acme")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"retry must recover from a single transient failure")
}

// TestGateway_BreakerOpensAfterSustainedFailures drives the
// downstream to constant failure and asserts the breaker
// eventually returns 503 with the open-circuit message instead
// of 502 (downstream-error).
func TestGateway_BreakerOpensAfterSustainedFailures(t *testing.T) {
	gw := newGateway("demo-token-1234567890", alwaysFailDownstream)
	srv := httptest.NewServer(gw.buildHandler(slog.Default()))
	defer srv.Close()

	send := func() int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/orders", nil)
		req.Header.Set("Authorization", "Bearer demo-token-1234567890")
		req.Header.Set("X-Tenant-Id", "acme")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	// Hammer the gateway until the breaker trips. Threshold is 5
	// consecutive failures; after that the breaker should reject
	// fast with 503.
	var saw503 bool
	for i := 0; i < 30; i++ {
		status := send()
		if status == http.StatusServiceUnavailable {
			saw503 = true
			break
		}
	}
	assert.True(t, saw503, "breaker must open after sustained downstream failure")
}

// TestGateway_TenantHeaderRequired pins the contract that the
// gateway refuses to dispatch without an X-Tenant-Id header.
func TestGateway_TenantHeaderRequired(t *testing.T) {
	gw := newGateway("demo-token-1234567890", callRealDownstream)
	srv := httptest.NewServer(gw.buildHandler(slog.Default()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/orders", nil)
	req.Header.Set("Authorization", "Bearer demo-token-1234567890")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestGateway_BulkheadFullReturns503 saturates the bulkhead with
// slow in-flight calls and asserts the next caller is rejected
// with 503 ("downstream busy"). The downstream blocks on a release
// channel so the test deterministically observes the full state.
func TestGateway_BulkheadFullReturns503(t *testing.T) {
	release := make(chan struct{})

	var inflight atomic.Int32
	slowDownstream := func(ctx context.Context, _ string) (string, error) {
		inflight.Add(1)
		defer inflight.Add(-1)
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	gw := newGateway("demo-token-1234567890", slowDownstream)
	srv := httptest.NewServer(gw.buildHandler(slog.Default()))
	// Drain blocked handlers before srv.Close() — otherwise close
	// races with the in-flight goroutines.
	var wg sync.WaitGroup
	defer func() {
		close(release)
		wg.Wait()
		srv.Close()
	}()

	// send issues one request and returns its status alongside the
	// transport error. It returns rather than asserting so callers on
	// worker goroutines never invoke require.* — t.FailNow only exits
	// the calling goroutine and would leave the test in a confusing
	// half-failed state. The caller decides how to assert.
	send := func() (int, error) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/orders", nil)
		req.Header.Set("Authorization", "Bearer demo-token-1234567890")
		req.Header.Set("X-Tenant-Id", "acme")
		resp, err := srv.Client().Do(req)
		if err != nil {
			return 0, err
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode, nil
	}

	// Fill the bulkhead with bulkheadMaxInFlight concurrent calls.
	// They block on `release` so the bulkhead stays saturated. Worker
	// goroutines use assert.* (t.Error, goroutine-safe) so a transport
	// failure records the failure without aborting the goroutine.
	for i := 0; i < bulkheadMaxInFlight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := send()
			assert.NoError(t, err, "saturating request must not fail at the transport layer")
		}()
	}

	// Wait until the bulkhead is actually full before firing the
	// overflow caller.
	deadline := time.Now().Add(2 * time.Second)
	for inflight.Load() < int32(bulkheadMaxInFlight) {
		if time.Now().After(deadline) {
			t.Fatalf("bulkhead never saturated; inflight=%d", inflight.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The bulkhead is now full. The next caller will wait up to
	// bulkheadQueueWait (100ms) before being rejected with 503. This
	// runs on the main test goroutine, so require.* is safe here.
	status, err := send()
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, status,
		"caller arriving while bulkhead is full must be rejected with 503")
}

// TestGateway_BudgetExhaustedReturns504 makes the downstream
// slower than the per-call budget so the timeoutbudget context
// cancels mid-call. The gateway must surface 504, not 502.
func TestGateway_BudgetExhaustedReturns504(t *testing.T) {
	slowDownstream := func(ctx context.Context, _ string) (string, error) {
		select {
		case <-time.After(2 * time.Second): // far longer than requestBudget
			return "late", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	gw := newGateway("demo-token-1234567890", slowDownstream)
	srv := httptest.NewServer(gw.buildHandler(slog.Default()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/orders", nil)
	req.Header.Set("Authorization", "Bearer demo-token-1234567890")
	req.Header.Set("X-Tenant-Id", "acme")

	start := time.Now()
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusGatewayTimeout, resp.StatusCode,
		"a downstream slower than the budget must surface 504")
	assert.Less(t, elapsed, 2*time.Second,
		"the gateway must cancel the downstream before its natural completion")
}
