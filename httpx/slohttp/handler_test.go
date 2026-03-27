package slohttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bds421/rho-kit/observability/slo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_OK(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(1000)

	c := slo.NewChecker(reg, slo.ErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slo", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var resp StatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "ok", resp.Overall)
}

func TestHandler_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(900)
	total.WithLabelValues("500").Add(100)

	c := slo.NewChecker(reg, slo.ErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slo", nil))

	var resp StatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "breached", resp.Overall)
}

func TestHandler_NaN(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := slo.NewChecker(reg, slo.ErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slo", nil))

	var resp StatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Nil(t, resp.Statuses[0].Current)
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := slo.NewChecker(reg, slo.ErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/slo", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}

func TestHandler_Head(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := slo.NewChecker(reg, slo.ErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/slo", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_Empty(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := slo.NewChecker(reg)
	handler := Handler(c)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slo", nil))

	var resp StatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "ok", resp.Overall)
	assert.Empty(t, resp.Statuses)
}

func TestHandler_PanicsNilChecker(t *testing.T) {
	assert.Panics(t, func() { Handler(nil) })
}
