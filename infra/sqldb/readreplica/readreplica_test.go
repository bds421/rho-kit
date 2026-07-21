package readreplica_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb/readreplica/v2"
)

// fakeAcquirer implements readreplica.Acquirer without a real Postgres
// pool. It records call counts and synthesises Acquire / Ping failures
// on demand so the routing logic can be exercised without testcontainers.
type fakeAcquirer struct {
	mu          sync.Mutex
	name        string
	acquires    int
	pings       int
	failAcquire error // returned by Acquire when non-nil
	failPing    error // returned by Ping when non-nil
}

func (f *fakeAcquirer) Acquire(_ context.Context) (*pgxpool.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	if f.failAcquire != nil {
		return nil, f.failAcquire
	}
	// We return nil; tests don't use the connection. nil here means
	// "Acquire succeeded but the conn is a stub". The RoutingPool
	// never dereferences it.
	return nil, nil //nolint:nilnil // sentinel ok-with-nil-conn in tests
}

func (f *fakeAcquirer) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	return f.failPing
}

func (f *fakeAcquirer) Close() {}

func (f *fakeAcquirer) setFailPing(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPing = err
}

func (f *fakeAcquirer) acquireCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquires
}

func (f *fakeAcquirer) pingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings
}

func TestNew_RequiresPrimary(t *testing.T) {
	_, err := readreplica.New(readreplica.Config{})
	require.Error(t, err)
}

func TestAcquire_DefaultRoutesToPrimary(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	replica := &fakeAcquirer{name: "r"}
	rp, err := readreplica.New(readreplica.Config{
		Primary:  primary,
		Replicas: []readreplica.Acquirer{replica},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	for i := 0; i < 5; i++ {
		_, _ = rp.Acquire(context.Background())
	}
	require.Equal(t, 5, primary.acquireCount())
	require.Equal(t, 0, replica.acquireCount())
}

func TestAcquire_ReadOnlyRoutesToReplicaRoundRobin(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	r1 := &fakeAcquirer{name: "r1"}
	r2 := &fakeAcquirer{name: "r2"}
	rp, err := readreplica.New(readreplica.Config{
		Primary:  primary,
		Replicas: []readreplica.Acquirer{r1, r2},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	for i := 0; i < 6; i++ {
		_, _ = rp.Acquire(context.Background(), readreplica.WithReadOnly())
	}
	// Round-robin: 3 each
	require.Equal(t, 3, r1.acquireCount(), "r1 acquires")
	require.Equal(t, 3, r2.acquireCount(), "r2 acquires")
	require.Equal(t, 0, primary.acquireCount(), "primary acquires (should be zero for read-only)")
}

func TestAcquire_FallbackToPrimaryWhenAllReplicasUnhealthy(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	r1 := &fakeAcquirer{name: "r1", failAcquire: errors.New("down")}
	r2 := &fakeAcquirer{name: "r2", failAcquire: errors.New("down")}
	reg := prometheus.NewRegistry()
	rp, err := readreplica.New(readreplica.Config{
		Primary:  primary,
		Replicas: []readreplica.Acquirer{r1, r2},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMaxConsecutiveFailures(1),
		readreplica.WithMetricsRegisterer(reg),
	)
	require.NoError(t, err)
	defer rp.Close()

	// First read: tries r1 (fail), r2 (fail), falls back to primary.
	_, _ = rp.Acquire(context.Background(), readreplica.WithReadOnly())
	require.Equal(t, 1, primary.acquireCount(), "fallback should hit primary")

	// Second read: all replicas marked unhealthy after the first
	// failure, so we skip them and go straight to primary.
	_, _ = rp.Acquire(context.Background(), readreplica.WithReadOnly())
	require.Equal(t, 2, primary.acquireCount(), "subsequent reads go straight to primary")

	// Both replicas should report unhealthy.
	health := rp.ReplicaHealth()
	require.Equal(t, []bool{false, false}, health)

	// Fallback counter should be at least 1.
	require.GreaterOrEqual(t, counterValue(t, reg, "sqldb_readreplica_replica_fallback_total"), 1.0)

	// primary_acquires_total counts every connection taken from the
	// primary pool, including read-only fallbacks (its help text says so).
	// Both reads above fell back to the primary, so the counter must
	// equal the number of primary.Acquire calls.
	require.Equal(t, 2.0, counterValue(t, reg, "sqldb_readreplica_primary_acquires_total"),
		"primary_acquires_total must include read-only fallbacks to primary")
}

func TestHealthLoop_ReAddsReplicaAfterRecovery(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	bad := &fakeAcquirer{name: "r1", failPing: errors.New("down")}
	rp, err := readreplica.New(readreplica.Config{
		Primary:  primary,
		Replicas: []readreplica.Acquirer{bad},
	},
		readreplica.WithHealthInterval(20*time.Millisecond),
		readreplica.WithMaxConsecutiveFailures(1),
		readreplica.WithProbeTimeout(50*time.Millisecond),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	require.Eventually(t, func() bool {
		return !rp.ReplicaHealth()[0]
	}, 1*time.Second, 10*time.Millisecond, "expected replica to be marked unhealthy")

	// Recovery.
	bad.setFailPing(nil)
	require.Eventually(t, func() bool {
		return rp.ReplicaHealth()[0]
	}, 1*time.Second, 10*time.Millisecond, "expected replica to recover")
}

func TestAcquire_NoReplicasIsPassThrough(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	rp, err := readreplica.New(readreplica.Config{Primary: primary},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	_, _ = rp.Acquire(context.Background(), readreplica.WithReadOnly())
	require.Equal(t, 1, primary.acquireCount(), "no replicas → fallback to primary")
}

func TestClose_IsIdempotent(t *testing.T) {
	rp, err := readreplica.New(readreplica.Config{
		Primary: &fakeAcquirer{},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	rp.Close()
	rp.Close() // must not panic
}

func TestReplicaConcurrency(t *testing.T) {
	primary := &fakeAcquirer{}
	r := &fakeAcquirer{}
	rp, err := readreplica.New(readreplica.Config{
		Primary:  primary,
		Replicas: []readreplica.Acquirer{r},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	var wg sync.WaitGroup
	var ops atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = rp.Acquire(context.Background(), readreplica.WithReadOnly())
				ops.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(2500), ops.Load())
	require.Equal(t, 2500, r.acquireCount(), "all reads on the one healthy replica")
}

func TestNew_SharedRegistererDoesNotClobberGauges(t *testing.T) {
	// Two RoutingPools sharing one registerer (the DefaultRegisterer
	// behaviour) must not corrupt each other's gauges. A shared
	// single-series gauge would let the second pool's Set overwrite the
	// first pool's value; per-pool series keep each pool independent.
	reg := prometheus.NewRegistry()

	rpA, err := readreplica.New(readreplica.Config{
		Primary:  &fakeAcquirer{name: "pa"},
		Replicas: []readreplica.Acquirer{&fakeAcquirer{}, &fakeAcquirer{}},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(reg),
	)
	require.NoError(t, err)
	defer rpA.Close()

	rpB, err := readreplica.New(readreplica.Config{
		Primary:  &fakeAcquirer{name: "pb"},
		Replicas: []readreplica.Acquirer{&fakeAcquirer{}, &fakeAcquirer{}, &fakeAcquirer{}, &fakeAcquirer{}, &fakeAcquirer{}},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(reg),
	)
	require.NoError(t, err)
	defer rpB.Close()

	// Both pools' replicas_total values must survive simultaneously:
	// pool A configured 2, pool B configured 5.
	totals := gaugeValues(t, reg, "sqldb_readreplica_replicas_total")
	require.ElementsMatch(t, []float64{2, 5}, totals,
		"each pool must keep its own replicas_total gauge series")

	healthy := gaugeValues(t, reg, "sqldb_readreplica_replicas_healthy")
	require.ElementsMatch(t, []float64{2, 5}, healthy,
		"each pool must keep its own replicas_healthy gauge series")
}

func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func gaugeValues(t *testing.T, reg *prometheus.Registry, name string) []float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	var out []float64
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			out = append(out, m.GetGauge().GetValue())
		}
	}
	return out
}

var _ = dto.MetricFamily{} // keep dto import linked for diagnostics

func TestAcquire_ZeroReplicaPassThroughDoesNotCountFallback(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	reg := prometheus.NewRegistry()
	rp, err := readreplica.New(readreplica.Config{Primary: primary},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(reg),
	)
	require.NoError(t, err)
	defer rp.Close()

	for i := 0; i < 5; i++ {
		_, err := rp.Acquire(context.Background(), readreplica.WithReadOnly())
		require.NoError(t, err)
	}
	require.Equal(t, 5, primary.acquireCount())
	require.Equal(t, 0.0, counterValue(t, reg, "sqldb_readreplica_replica_fallback_total"),
		"zero-replica pass-through must not inflate the degradation metric")
	require.Equal(t, 5.0, counterValue(t, reg, "sqldb_readreplica_primary_acquires_total"))
}

func TestAcquire_CallerCancelDoesNotEvictReplicas(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	// Acquire respects the caller's context: return ctx.Err() so the
	// pool sees a cancellation-shaped failure rather than a replica fault.
	r1 := &ctxAwareAcquirer{fakeAcquirer: fakeAcquirer{name: "r1"}}
	r2 := &ctxAwareAcquirer{fakeAcquirer: fakeAcquirer{name: "r2"}}
	rp, err := readreplica.New(readreplica.Config{
		Primary:  primary,
		Replicas: []readreplica.Acquirer{r1, r2},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMaxConsecutiveFailures(1),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already canceled
		_, _ = rp.Acquire(ctx, readreplica.WithReadOnly())
	}

	// Replicas must still be healthy — cancellation is the caller's fault.
	require.Equal(t, []bool{true, true}, rp.ReplicaHealth(),
		"caller cancellation must not evict replicas")
}

// ctxAwareAcquirer fails Acquire with ctx.Err() when the context is done.
type ctxAwareAcquirer struct {
	fakeAcquirer
}

func (f *ctxAwareAcquirer) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	f.mu.Lock()
	f.acquires++
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil //nolint:nilnil // test stub
}

func TestClose_DeletesGaugeSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	rp, err := readreplica.New(readreplica.Config{
		Primary:  &fakeAcquirer{name: "p"},
		Replicas: []readreplica.Acquirer{&fakeAcquirer{}, &fakeAcquirer{}},
	},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(reg),
	)
	require.NoError(t, err)

	require.ElementsMatch(t, []float64{2}, gaugeValues(t, reg, "sqldb_readreplica_replicas_total"))
	require.ElementsMatch(t, []float64{2}, gaugeValues(t, reg, "sqldb_readreplica_replicas_healthy"))

	rp.Close()

	require.Empty(t, gaugeValues(t, reg, "sqldb_readreplica_replicas_total"),
		"Close must DeleteLabelValues for replicas_total")
	require.Empty(t, gaugeValues(t, reg, "sqldb_readreplica_replicas_healthy"),
		"Close must DeleteLabelValues for replicas_healthy")
}

func TestPrimaryHealthy_LivePing(t *testing.T) {
	primary := &fakeAcquirer{name: "p"}
	rp, err := readreplica.New(readreplica.Config{Primary: primary},
		readreplica.WithoutHealthCheck(),
		readreplica.WithMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	defer rp.Close()

	require.True(t, rp.PrimaryHealthy(context.Background()))
	require.Equal(t, 1, primary.pingCount(), "PrimaryHealthy must issue a live Ping")

	primary.setFailPing(errors.New("down"))
	require.False(t, rp.PrimaryHealthy(context.Background()))
	require.Equal(t, 2, primary.pingCount())
}
