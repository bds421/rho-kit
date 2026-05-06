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
