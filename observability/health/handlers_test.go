package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveness_Always200(t *testing.T) {
	t.Parallel()

	h := Liveness("v1.2.3")
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live", nil)

	h.ServeHTTP(rr, r)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))

	var body LivenessResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, StatusHealthy, body.Status)
	assert.Equal(t, "v1.2.3", body.Version)
}

func TestWriteJSONError(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	writeJSONError(rr, http.StatusInternalServerError)

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"error":"internal error","code":"INTERNAL"}`, rr.Body.String())
}

func TestReadiness_HealthyReturns200(t *testing.T) {
	t.Parallel()

	checker := &Checker{
		Version: "v1",
		Checks: []DependencyCheck{
			{
				Name:     "db",
				Critical: true,
				Check:    func(_ context.Context) string { return StatusHealthy },
			},
		},
	}
	h := Readiness(checker)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ready", nil)

	h.ServeHTTP(rr, r)

	require.Equal(t, http.StatusOK, rr.Code)

	var body Response
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, StatusHealthy, body.Status)
	assert.Equal(t, StatusHealthy, body.Services["db"])
}

func TestReadiness_DegradedReturns200(t *testing.T) {
	t.Parallel()

	checker := &Checker{
		Version: "v1",
		Checks: []DependencyCheck{
			{
				Name:     "cache",
				Critical: false, // non-critical degradation must still be ready
				Check:    func(_ context.Context) string { return StatusUnhealthy },
			},
		},
	}
	h := Readiness(checker)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ready", nil)

	h.ServeHTTP(rr, r)

	require.Equal(t, http.StatusOK, rr.Code)

	var body Response
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, StatusDegraded, body.Status)
}

// TestReadiness_PanicsOnNilChecker pins the MEDIUM finding: a miswired
// readiness route used to nil-deref on every probe. The fix fails fast at
// handler construction so the route never registers in the first place.
func TestReadiness_PanicsOnNilChecker(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { Readiness(nil) })
}

func TestReadiness_PanicsOnInvalidChecker(t *testing.T) {
	t.Parallel()
	checker := &Checker{
		Version: "v1",
		Checks: []DependencyCheck{
			{Name: "secret-token"},
		},
	}
	assert.PanicsWithValue(t, "health: Readiness requires a valid *Checker", func() { Readiness(checker) })
}

func TestReadiness_UnhealthyReturns503(t *testing.T) {
	t.Parallel()

	checker := &Checker{
		Version: "v1",
		Checks: []DependencyCheck{
			{
				Name:     "db",
				Critical: true,
				Check:    func(_ context.Context) string { return StatusUnhealthy },
			},
		},
	}
	h := Readiness(checker)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ready", nil)

	h.ServeHTTP(rr, r)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body Response
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, StatusUnhealthy, body.Status)
}
