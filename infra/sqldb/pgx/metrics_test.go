package pgx

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPoolStatsCollector_NilPool surfaces the misconfiguration as an error
// rather than panicking on the first Collect() — startup-time errors are
// easier to diagnose than runtime panics inside the metrics scrape goroutine.
func TestNewPoolStatsCollector_NilPool(t *testing.T) {
	reg := prometheus.NewRegistry()
	c, err := NewPoolStatsCollector(nil, "primary", WithRegisterer(reg))
	require.Error(t, err)
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "non-nil pool")
}

// TestNewPoolStatsCollector_EmptyInstance rejects unlabelled collectors so a
// service that wires two pools cannot accidentally collapse their stats into
// a single time series.
func TestNewPoolStatsCollector_EmptyInstance(t *testing.T) {
	pool := newDisconnectedPool(t)
	defer pool.Close()

	reg := prometheus.NewRegistry()
	_, err := NewPoolStatsCollector(pool, "", WithRegisterer(reg))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance")
}

// TestPoolStatsCollector_EmitsAllDescriptors confirms every documented metric
// shows up in the registry after a scrape, with the instance label applied.
func TestPoolStatsCollector_EmitsAllDescriptors(t *testing.T) {
	pool := newDisconnectedPool(t)
	defer pool.Close()

	reg := prometheus.NewRegistry()
	_, err := NewPoolStatsCollector(pool, "primary", WithRegisterer(reg))
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)

	wantFamilies := map[string]bool{
		"pgx_pool_acquired_conns":                false,
		"pgx_pool_total_conns":                   false,
		"pgx_pool_idle_conns":                    false,
		"pgx_pool_max_conns":                     false,
		"pgx_pool_acquire_wait_seconds_total":    false,
		"pgx_pool_acquire_count_total":           false,
		"pgx_pool_canceled_acquire_count_total":  false,
	}
	for _, mf := range families {
		if _, ok := wantFamilies[mf.GetName()]; ok {
			wantFamilies[mf.GetName()] = true
			require.NotEmpty(t, mf.GetMetric(), "no samples for %s", mf.GetName())
			labels := mf.GetMetric()[0].GetLabel()
			require.Len(t, labels, 1)
			assert.Equal(t, "instance", labels[0].GetName())
			assert.Equal(t, "primary", labels[0].GetValue())
		}
	}
	for name, seen := range wantFamilies {
		assert.True(t, seen, "metric family %s never scraped", name)
	}
}

// TestPoolStatsCollector_MaxConnsReportsConfigured pins the max-conns gauge to
// the value pgxpool was constructed with, so capacity-planning dashboards have
// the same denominator the runtime is enforcing.
func TestPoolStatsCollector_MaxConnsReportsConfigured(t *testing.T) {
	pool := newDisconnectedPool(t)
	defer pool.Close()

	reg := prometheus.NewRegistry()
	c, err := NewPoolStatsCollector(pool, "primary", WithRegisterer(reg))
	require.NoError(t, err)

	// pgxpool.Stat() honours MaxConns from the config used at construction.
	got := readGauge(t, reg, c.maxConns)
	assert.Equal(t, float64(pool.Config().MaxConns), got)
}

// TestPoolStatsCollector_DuplicateRegistrationReusesCollector exercises the
// AlreadyRegisteredError branch: the second construction call returns the
// already-registered collector rather than panicking, which is what makes the
// "register on every WithPostgres" wiring in the Builder safe to call from
// tests that share prometheus.DefaultRegisterer.
func TestPoolStatsCollector_DuplicateRegistrationReusesCollector(t *testing.T) {
	pool := newDisconnectedPool(t)
	defer pool.Close()

	reg := prometheus.NewRegistry()
	first, err := NewPoolStatsCollector(pool, "primary", WithRegisterer(reg))
	require.NoError(t, err)
	second, err := NewPoolStatsCollector(pool, "primary", WithRegisterer(reg))
	require.NoError(t, err)
	assert.Same(t, first, second)
}

// TestPoolStatsCollector_NilRegistererUsesDefault confirms passing nil reg
// falls through to prometheus.DefaultRegisterer, matching the Redis-package
// convention so callers do not need to know about DefaultRegisterer.
func TestPoolStatsCollector_NilRegistererUsesDefault(t *testing.T) {
	pool := newDisconnectedPool(t)
	defer pool.Close()

	// Use a unique instance so we don't clash with prior tests sharing
	// the process-wide default registerer.
	const instance = "test-nil-reg"
	c, err := NewPoolStatsCollector(pool, instance)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer prometheus.DefaultRegisterer.Unregister(c)

	// Subsequent registration on the default registerer reuses the
	// collector — proves it was actually registered the first time.
	c2, err := NewPoolStatsCollector(pool, instance)
	require.NoError(t, err)
	assert.Same(t, c, c2)
}

// readGauge extracts a single gauge sample matching desc from reg. Used to
// assert specific values without round-tripping through testutil.ToFloat64
// (the collector is unregistered after a single call, which we want for
// repeated assertions in one test).
func readGauge(t *testing.T, reg prometheus.Gatherer, desc *prometheus.Desc) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	descName := descShortName(desc.String())
	for _, mf := range families {
		if mf.GetName() != descName {
			continue
		}
		require.NotEmpty(t, mf.GetMetric())
		return mf.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatalf("metric %s not found", descName)
	return 0
}

// descShortName extracts the fqName from a Desc.String() value of the form
// `Desc{fqName: "foo", ...}`.
func descShortName(s string) string {
	const prefix = `fqName: "`
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// newDisconnectedPool returns a pgxpool that has not actually established a
// network connection. pgxpool.NewWithConfig with LazyConnect is the closest
// we get without testcontainers; Stat() works on the un-dialed pool and
// returns zero counters with the configured MaxConns.
func newDisconnectedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// Use a DSN that pgxpool.ParseConfig accepts but never dials because we
	// never call Acquire(). The pool returns from NewWithConfig immediately;
	// Stat() does not require a live connection.
	pcfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	require.NoError(t, err)
	pcfg.MaxConns = 7
	pcfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	require.NoError(t, err)
	return pool
}

var _ = testutil.ToFloat64 // silence unused import when read* helpers are dropped
