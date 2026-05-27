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

func (f *fakeAcquirer) setFailAcquire(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAcquire = err
}

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

var _ = dto.MetricFamily{} // keep dto import linked for diagnostics
