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
		// Allow PASS for everything except the rate-limit probe (this
		// fixture doesn't 429), which legitimately surfaces as UNKNOWN.
		if r.Probe == "ratelimit-emits-retry-after-on-429" {
			assert.Equalf(t, StatusUnknown, r.Status, "probe %s should be UNKNOWN without an active rate limiter: %s", r.Probe, r.Detail)
			continue
		}
		assert.Equalf(t, StatusPass, r.Status, "probe %s status=%s detail=%s", r.Probe, r.Status, r.Detail)
	}
}

func TestRunAll_DetectsMissingHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately omit X-Content-Type-Options to fail that probe.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)

	for _, r := range results {
		if r.Probe == "secheaders-x-content-type-options" {
			assert.Equalf(t, StatusFail, r.Status, "missing X-Content-Type-Options must FAIL: %s", r.Detail)
			return
		}
	}
	t.Fatal("secheaders-x-content-type-options probe not found in results")
}

func TestExitNonZero_FailAlwaysExitsNonZero(t *testing.T) {
	// FR-004: a FAIL result must exit non-zero regardless of -soft
	// or -no-allow-skips. The pre-fix code only treated readiness as
	// hard, so JWT/CSRF/rate-limit FAIL results silently passed CI.
	results := []Result{{Probe: "secheaders-x-frame-options", Status: StatusFail}}
	assert.True(t, exitNonZero(results, false, false), "FAIL must fail by default")
	assert.True(t, exitNonZero(results, true, false), "FAIL must still fail under -soft")
	assert.True(t, exitNonZero(results, true, true), "FAIL must still fail under -soft -no-allow-skips")
}

func TestExitNonZero_UnknownFailsByDefault_PassesUnderSoft(t *testing.T) {
	// FR-005: UNKNOWN was previously encoded as "passing" (route 404
	// → success). It now defaults to non-zero unless -soft.
	results := []Result{{Probe: "jwt-rejects-missing-token", Status: StatusUnknown}}
	assert.True(t, exitNonZero(results, false, false), "UNKNOWN must fail by default")
	assert.False(t, exitNonZero(results, true, false), "UNKNOWN must pass under -soft")
}

func TestExitNonZero_SkippedPassesByDefault_FailsUnderNoAllowSkips(t *testing.T) {
	results := []Result{{Probe: "ratelimit-emits-retry-after-on-429", Status: StatusSkipped}}
	assert.False(t, exitNonZero(results, false, false), "SKIPPED must pass by default")
	assert.True(t, exitNonZero(results, false, true), "SKIPPED must fail under -no-allow-skips")
}

func TestExitNonZero_PassNeverFails(t *testing.T) {
	results := []Result{{Probe: "readiness-200", Status: StatusPass}}
	for _, soft := range []bool{false, true} {
		for _, noAllowSkips := range []bool{false, true} {
			assert.Falsef(t, exitNonZero(results, soft, noAllowSkips),
				"PASS must always succeed (soft=%v, noAllowSkips=%v)", soft, noAllowSkips)
		}
	}
}

func TestContainsFold(t *testing.T) {
	assert.True(t, containsFold("Cache-Control: no-store", "no-store"))
	assert.True(t, containsFold("nosniff", "NOSNIFF"))
	assert.False(t, containsFold("DENY", "ALLOW"))
	assert.True(t, containsFold("anything", ""))
}

func TestJWTProbe_404IsUnknown(t *testing.T) {
	// Route-by-convention: a 404 doesn't tell us whether JWT is wired
	// or this service simply doesn't expose /api/v1/whoami. Pre-fix
	// this returned a soft pass; new behaviour is UNKNOWN so CI sees
	// the inconclusive result and either treats it as failure (default)
	// or warning (-soft).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	hc := &http.Client{Timeout: 2 * time.Second}
	results := runAll(hc, srv.URL)
	var found bool
	for _, r := range results {
		if r.Probe == "jwt-rejects-missing-token" {
			require.Equal(t, StatusUnknown, r.Status, "404 on JWT probe must be UNKNOWN, got %s (%s)", r.Status, r.Detail)
			found = true
			break
		}
	}
	require.True(t, found)
}
