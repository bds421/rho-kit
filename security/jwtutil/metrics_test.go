package jwtutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewMetricsCollector_NilProvider rejects nil at construction time
// because the collector reads provider state inside Collect() with no
// further guards.
func TestNewMetricsCollector_NilProvider(t *testing.T) {
	reg := prometheus.NewRegistry()
	c, err := NewMetricsCollector(nil, "primary", WithRegisterer(reg))
	require.Error(t, err)
	assert.Nil(t, c)
}

// TestNewMetricsCollector_EmptyInstance pins the requirement: every
// collector instance must have a label so multi-provider services do not
// silently collapse samples into one time series.
func TestNewMetricsCollector_EmptyInstance(t *testing.T) {
	p := newFixtureProvider(t)
	reg := prometheus.NewRegistry()
	_, err := NewMetricsCollector(p, "", WithRegisterer(reg))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance")
}

// TestMetricsCollector_EmitsZerosOnEmptyProvider proves the collector is
// usable before the Provider has fetched anything: timestamps are 0 and the
// fetch-failures counter is at zero for every reason.
func TestMetricsCollector_EmitsZerosOnEmptyProvider(t *testing.T) {
	p := newFixtureProvider(t)
	reg := prometheus.NewRegistry()
	_, err := NewMetricsCollector(p, "primary", WithRegisterer(reg))
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)
	got := metricFamilies(families)

	require.Contains(t, got, "jwks_last_successful_fetch_timestamp_seconds")
	require.Contains(t, got, "jwks_staleness_seconds")
	require.Contains(t, got, "jwks_fetch_failures_total")

	assert.Equal(t, 0.0, gaugeValue(got["jwks_last_successful_fetch_timestamp_seconds"]))
	assert.Equal(t, 0.0, gaugeValue(got["jwks_staleness_seconds"]))

	reasons := counterByLabel(got["jwks_fetch_failures_total"], "reason")
	assert.Equal(t, 0.0, reasons["http"])
	assert.Equal(t, 0.0, reasons["parse"])
	assert.Equal(t, 0.0, reasons["stale-rejected"])
}

// TestMetricsCollector_TracksSuccessAndStaleness simulates a provider
// state where the last successful fetch is a known time ago, and confirms
// both the timestamp and the staleness gauges reflect it.
func TestMetricsCollector_TracksSuccessAndStaleness(t *testing.T) {
	p := newFixtureProvider(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	last := now.Add(-15 * time.Minute)
	p.mu.Lock()
	p.keyset = &KeySet{}
	p.lastSuccessfulFetch = last
	p.mu.Unlock()

	reg := prometheus.NewRegistry()
	c, err := NewMetricsCollector(p, "primary", WithRegisterer(reg))
	require.NoError(t, err)
	// Pin the clock so the staleness math is deterministic.
	c.clock = func() time.Time { return now }

	families, err := reg.Gather()
	require.NoError(t, err)
	got := metricFamilies(families)

	assert.Equal(t, float64(last.Unix()), gaugeValue(got["jwks_last_successful_fetch_timestamp_seconds"]))
	assert.Equal(t, float64(15*60), gaugeValue(got["jwks_staleness_seconds"]))
}

// TestMetricsCollector_ReportsStaleRejectedFailures wires the end-to-end
// path: a provider whose last fetch is past max-stale returns ErrKeySetStale
// from Verify, the counter increments, and the collector exposes it.
func TestMetricsCollector_ReportsStaleRejectedFailures(t *testing.T) {
	p := newFixtureProvider(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	p.maxStale = 5 * time.Minute
	p.clock = func() time.Time { return now }
	p.mu.Lock()
	p.keyset = &KeySet{}
	p.lastSuccessfulFetch = now.Add(-1 * time.Hour)
	p.mu.Unlock()

	// Two stale rejections increment the counter once (transition, not per-request).
	for i := 0; i < 2; i++ {
		_, err := p.Verify("any.token.here", now)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrKeySetStale), "got %v, want ErrKeySetStale", err)
		require.True(t, errors.Is(err, ErrKeySetUnavailable), "errors.Is(ErrKeySetUnavailable) must still match")
	}

	reg := prometheus.NewRegistry()
	_, err := NewMetricsCollector(p, "primary", WithRegisterer(reg))
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)
	got := metricFamilies(families)
	reasons := counterByLabel(got["jwks_fetch_failures_total"], "reason")
	assert.Equal(t, 1.0, reasons["stale-rejected"])
	assert.Equal(t, 0.0, reasons["http"])
	assert.Equal(t, 0.0, reasons["parse"])
}

// TestMetricsCollector_DuplicateRegistrationReusesCollector covers the
// AlreadyRegisteredError fast path so the Builder's "register on every JWT
// module Init" wiring is idempotent against the default registerer.
func TestMetricsCollector_DuplicateRegistrationReusesCollector(t *testing.T) {
	p := newFixtureProvider(t)
	reg := prometheus.NewRegistry()
	first, err := NewMetricsCollector(p, "primary", WithRegisterer(reg))
	require.NoError(t, err)
	second, err := NewMetricsCollector(p, "primary", WithRegisterer(reg))
	require.NoError(t, err)
	assert.Same(t, first, second)
}

// TestProvider_Verify_ReturnsTypedNotReady locks the typed-error contract:
// before any fetch the verifier returns ErrKeySetNotReady, and legacy
// errors.Is on ErrKeySetUnavailable still matches.
func TestProvider_Verify_ReturnsTypedNotReady(t *testing.T) {
	p := newFixtureProvider(t)
	_, err := p.Verify("any.token.here", time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKeySetNotReady), "got %v", err)
	assert.True(t, errors.Is(err, ErrKeySetUnavailable), "must still match umbrella sentinel")
}

// TestProvider_Fetch_RecordsHTTPFailures simulates an HTTP-side failure by
// pointing the fetcher at an unreachable URL and confirms the http counter
// increments instead of the parse counter.
func TestProvider_Fetch_RecordsHTTPFailures(t *testing.T) {
	p := NewProvider("https://127.0.0.1:1/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
	)

	// 127.0.0.1:1 is unreachable; fetch returns a net error.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := p.fetch(ctx)
	require.Error(t, err)

	assert.GreaterOrEqual(t, p.fetchFailHTTP.Load(), uint64(1))
	assert.Equal(t, uint64(0), p.fetchFailParse.Load())
}

// newFixtureProvider returns a hand-built Provider with a nil keyset and no
// JWKS URL, deliberately bypassing the NewProvider / NewProviderWithKeySet
// constructors (and their issuer/audience guardrails) so tests can exercise
// the "not ready" / fetch-counter paths without dialing a fake server. The
// keyset is left nil, so the initial state is "not ready".
func newFixtureProvider(t *testing.T) *Provider {
	t.Helper()
	p := &Provider{clock: time.Now, maxStale: defaultMaxStale}
	return p
}

// metricFamilies indexes a prometheus.Gather result by family name.
func metricFamilies(families []*dto.MetricFamily) map[string]*dto.MetricFamily {
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, mf := range families {
		out[mf.GetName()] = mf
	}
	return out
}

func gaugeValue(mf *dto.MetricFamily) float64 {
	if mf == nil || len(mf.GetMetric()) == 0 {
		return 0
	}
	return mf.GetMetric()[0].GetGauge().GetValue()
}

func counterByLabel(mf *dto.MetricFamily, label string) map[string]float64 {
	out := make(map[string]float64)
	if mf == nil {
		return out
	}
	for _, m := range mf.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == label {
				out[l.GetValue()] = m.GetCounter().GetValue()
			}
		}
	}
	return out
}

var _ = testutil.ToFloat64
