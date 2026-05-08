package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAll_AllPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		// Simulate request-id + correlation-id middleware:
		// generate an X-Request-Id, echo X-Correlation-Id.
		w.Header().Set("X-Request-Id", "test-rid-1")
		if cid := r.Header.Get("X-Correlation-Id"); cid != "" {
			w.Header().Set("X-Correlation-Id", cid)
		}
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/whoami":
			// JWT-gated route returns 401 without auth header.
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/v1/state":
			// CSRF-gated route returns 403 without token.
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)
	for _, r := range results {
		assert.Truef(t, r.Passed, "probe %s failed: %s", r.Probe, r.Detail)
	}
}

func TestRunAll_DetectsMissingHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately omit X-Content-Type-Options to fail that probe.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)

	failed := 0
	for _, r := range results {
		if !r.Passed && r.Probe == "secheaders-x-content-type-options" {
			failed++
		}
	}
	require.Equal(t, 1, failed, "missing X-Content-Type-Options must fail exactly that probe")
}

func TestExitNonZero_StrictModeFailsAny(t *testing.T) {
	results := []Result{
		{Probe: "secheaders-x-frame-options", Passed: false},
	}
	assert.True(t, exitNonZero(results, true), "strict mode must fail on any failed probe")
	assert.False(t, exitNonZero(results, false), "non-strict must NOT fail on a soft failure")
}

func TestExitNonZero_HardFailureAlwaysFails(t *testing.T) {
	results := []Result{
		{Probe: "readiness-200", Passed: false},
	}
	assert.True(t, exitNonZero(results, false),
		"readiness failure must fail CI even without -strict")
}

func TestContainsFold(t *testing.T) {
	assert.True(t, containsFold("Cache-Control: no-store", "no-store"))
	assert.True(t, containsFold("nosniff", "NOSNIFF"))
	assert.False(t, containsFold("DENY", "ALLOW"))
	assert.True(t, containsFold("anything", ""))
}
