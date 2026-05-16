package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
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
		defer resp.Body.Close()
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
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
