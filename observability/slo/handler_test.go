package slo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_OK(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(1000)

	c := NewChecker(reg, HTTPErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slo", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp StatusResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "ok", resp.Overall)
	require.Len(t, resp.Statuses, 1)
	assert.False(t, resp.Statuses[0].Breached)
}

func TestHandler_Breached(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"code"})
	reg.MustRegister(total)
	total.WithLabelValues("200").Add(900)
	total.WithLabelValues("500").Add(100)

	c := NewChecker(reg, HTTPErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slo", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp StatusResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "breached", resp.Overall)
	require.Len(t, resp.Statuses, 1)
	assert.True(t, resp.Statuses[0].Breached)
}

func TestHandler_MultiSLO(t *testing.T) {
	reg := prometheus.NewRegistry()
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total requests.",
	}, []string{"code"})
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Request duration.",
		Buckets: []float64{0.1, 0.25, 0.5, 1.0},
	})
	reg.MustRegister(total)
	reg.MustRegister(hist)

	total.WithLabelValues("200").Add(1000)
	for i := 0; i < 100; i++ {
		hist.Observe(0.05)
	}

	c := NewChecker(reg,
		HTTPErrorRateSLO("err", 0.01, time.Hour),
		HTTPLatencySLO("lat", 0.99, 0.5, time.Hour),
	)
	handler := Handler(c)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slo", nil)
	handler.ServeHTTP(rec, req)

	var resp StatusResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "ok", resp.Overall)
	assert.Len(t, resp.Statuses, 2)
}

func TestHandler_EmptyChecker(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := NewChecker(reg)
	handler := Handler(c)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slo", nil)
	handler.ServeHTTP(rec, req)

	var resp StatusResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "ok", resp.Overall)
	assert.Empty(t, resp.Statuses)
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := NewChecker(reg, HTTPErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/slo", nil)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "method %s should be rejected", method)
		assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
	}
}

func TestHandler_HeadMethod(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := NewChecker(reg, HTTPErrorRateSLO("err", 0.01, time.Hour))
	handler := Handler(c)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/slo", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_PanicsOnNilChecker(t *testing.T) {
	assert.PanicsWithValue(t, "slo: checker must not be nil", func() {
		Handler(nil)
	})
}

func TestToJSON(t *testing.T) {
	s := SLOStatus{
		Name:      "test",
		Type:      TypeErrorRate,
		Threshold: 0.01,
		Current:   0.005,
		Breached:  false,
		BurnRate:  0.5,
		Window:    24 * time.Hour,
	}

	j := toJSON(s)
	assert.Equal(t, "test", j.Name)
	assert.Equal(t, TypeErrorRate, j.Type)
	assert.Equal(t, "24h0m0s", j.Window)
}

func TestBuildResponse_MixedStatuses(t *testing.T) {
	statuses := []SLOStatus{
		{Name: "ok-slo", Breached: false},
		{Name: "bad-slo", Breached: true},
	}

	resp := buildResponse(statuses)
	assert.Equal(t, "breached", resp.Overall)
	assert.Len(t, resp.Statuses, 2)
}

func TestBuildResponse_AllOK(t *testing.T) {
	statuses := []SLOStatus{
		{Name: "slo-a", Breached: false},
		{Name: "slo-b", Breached: false},
	}

	resp := buildResponse(statuses)
	assert.Equal(t, "ok", resp.Overall)
}
