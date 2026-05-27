package readreplica

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Acquirer is the minimal surface RoutingPool needs from each backend
// pool. Both [*pgxpool.Pool] and tests' fake pools satisfy it.
type Acquirer interface {
	Acquire(ctx context.Context) (*pgxpool.Conn, error)
	Ping(ctx context.Context) error
	Close()
}

// Config configures a RoutingPool.
type Config struct {
	// Primary is the write-side pool. Required.
	Primary Acquirer
	// Replicas is the read-side pool fan-out. Zero replicas is legal:
	// the RoutingPool then behaves as a pass-through to Primary, which
	// lets callers code against the same API in single-pool and
	// replica-fanout deployments.
	Replicas []Acquirer
}

// Option configures a RoutingPool.
type Option func(*routingConfig)

type routingConfig struct {
	healthInterval        time.Duration
	maxConsecutiveFails   int
	logger                *slog.Logger
	registerer            prometheus.Registerer
	disableHealthCheck    bool
	probeTimeout          time.Duration
}

// WithHealthInterval overrides the replica health-probe interval
// (default 30s). Lower values surface failover faster at the cost of
// extra Ping traffic to each replica.
func WithHealthInterval(d time.Duration) Option {
	if d <= 0 {
		panic("readreplica: WithHealthInterval requires positive duration")
	}
	return func(c *routingConfig) { c.healthInterval = d }
}

// WithMaxConsecutiveFailures sets the consecutive-Ping-failure count
// at which a replica is removed from rotation (default 3). Mark-unhealthy
// is sticky until the next successful periodic probe.
func WithMaxConsecutiveFailures(n int) Option {
	if n <= 0 {
		panic("readreplica: WithMaxConsecutiveFailures requires positive count")
	}
	return func(c *routingConfig) { c.maxConsecutiveFails = n }
}

// WithProbeTimeout sets the per-Ping deadline used by the health loop
// (default 5s). A replica whose Ping takes longer counts as failed for
// the WithMaxConsecutiveFailures counter.
func WithProbeTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("readreplica: WithProbeTimeout requires positive duration")
	}
	return func(c *routingConfig) { c.probeTimeout = d }
}

// WithLogger overrides the logger used by the routing pool for warnings
// (replica removed from rotation, fallback to primary, etc.).
func WithLogger(l *slog.Logger) Option {
	return func(c *routingConfig) { c.logger = l }
}

// WithMetricsRegisterer pins the Prometheus registerer for routing
// metrics. Defaults to [prometheus.DefaultRegisterer].
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	if reg == nil {
		panic("readreplica: WithMetricsRegisterer requires non-nil registerer")
	}
	return func(c *routingConfig) { c.registerer = reg }
}

// WithoutHealthCheck disables the background health-probe loop.
// Replicas are tried on demand and a failed Acquire on a replica trips
// the consecutive-fail counter (so callers still get failover) but the
// kit will not background-probe them. Use only for tests that don't
// want a goroutine they have to clean up.
func WithoutHealthCheck() Option {
	return func(c *routingConfig) { c.disableHealthCheck = true }
}

// AcquireOption tunes a single Acquire call.
type AcquireOption func(*acquireConfig)

type acquireConfig struct {
	readOnly bool
}

// WithReadOnly hints that the Acquire is for a read workload, so the
// pool may return a healthy replica connection. Without this option
// the pool returns a primary connection.
func WithReadOnly() AcquireOption { return func(c *acquireConfig) { c.readOnly = true } }

// RoutingPool routes Acquire calls to primary or a healthy replica
// based on the AcquireOption set. Concurrent-safe.
type RoutingPool struct {
	primary  Acquirer
	replicas []*replicaState
	cfg      routingConfig
	metrics  *routingMetrics

	rrIdx atomic.Uint64

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type replicaState struct {
	pool             Acquirer
	consecutiveFails atomic.Int32
	healthy          atomic.Bool
}

// New constructs a RoutingPool. The supplied Primary must be non-nil.
// Replicas may be empty: the pool then behaves as a pass-through (every
// Acquire returns a primary connection); callers can promote to a
// fanned-out deployment later without an API change.
func New(cfg Config, opts ...Option) (*RoutingPool, error) {
	if cfg.Primary == nil {
		return nil, errors.New("readreplica: Config.Primary is required")
	}
	rc := routingConfig{
		healthInterval:      30 * time.Second,
		maxConsecutiveFails: 3,
		probeTimeout:        5 * time.Second,
		registerer:          prometheus.DefaultRegisterer,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("readreplica: option must not be nil")
		}
		opt(&rc)
	}
	if rc.logger == nil {
		rc.logger = slog.Default()
	}
	rp := &RoutingPool{
		primary: cfg.Primary,
		cfg:     rc,
		stopCh:  make(chan struct{}),
	}
	for _, r := range cfg.Replicas {
		if r == nil {
			return nil, errors.New("readreplica: Config.Replicas must not contain nil")
		}
		st := &replicaState{pool: r}
		st.healthy.Store(true)
		rp.replicas = append(rp.replicas, st)
	}
	m, err := newRoutingMetrics(rc.registerer)
	if err != nil {
		return nil, redact.WrapError("readreplica: metrics", err)
	}
	rp.metrics = m
	rp.metrics.replicaCount.Set(float64(len(rp.replicas)))
	rp.metrics.healthyReplicas.Set(float64(len(rp.replicas)))

	if !rc.disableHealthCheck && len(rp.replicas) > 0 {
		rp.wg.Add(1)
		go rp.healthLoop()
	}
	return rp, nil
}

// Acquire returns a connection. Default routes to primary; pass
// [WithReadOnly] to route to a healthy replica. When no replicas are
// healthy a read-only Acquire falls back to the primary and increments
// the fallback metric so dashboards surface the degradation.
func (p *RoutingPool) Acquire(ctx context.Context, opts ...AcquireOption) (*pgxpool.Conn, error) {
	cfg := acquireConfig{}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("readreplica: AcquireOption must not be nil")
		}
		opt(&cfg)
	}
	if !cfg.readOnly {
		p.metrics.primaryAcquires.Inc()
		return p.primary.Acquire(ctx)
	}
	if conn, ok := p.acquireReplica(ctx); ok {
		p.metrics.replicaAcquires.Inc()
		return conn, nil
	}
	p.metrics.replicaFallback.Inc()
	p.cfg.logger.Warn("readreplica: no healthy replicas, falling back to primary")
	conn, err := p.primary.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// acquireReplica picks a healthy replica round-robin and acquires a
// connection from it. Returns ok=false when no replica yields a
// connection (all unhealthy or all Acquire calls fail).
func (p *RoutingPool) acquireReplica(ctx context.Context) (*pgxpool.Conn, bool) {
	n := len(p.replicas)
	if n == 0 {
		return nil, false
	}
	// Try each replica at most once per call to avoid pathological loops.
	start := int(p.rrIdx.Add(1)) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		st := p.replicas[idx]
		if !st.healthy.Load() {
			continue
		}
		conn, err := st.pool.Acquire(ctx)
		if err == nil {
			st.consecutiveFails.Store(0)
			return conn, true
		}
		p.recordReplicaFailure(idx, err)
	}
	return nil, false
}

func (p *RoutingPool) recordReplicaFailure(idx int, err error) {
	st := p.replicas[idx]
	fails := st.consecutiveFails.Add(1)
	if fails >= int32(p.cfg.maxConsecutiveFails) && st.healthy.CompareAndSwap(true, false) {
		p.cfg.logger.Warn("readreplica: replica removed from rotation",
			slog.Int("replica_index", idx),
			slog.Int("consecutive_failures", int(fails)),
			slog.String("error", err.Error()),
		)
		p.metrics.healthyReplicas.Dec()
	}
}

// healthLoop probes each replica on a tick and toggles healthy state.
func (p *RoutingPool) healthLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.cfg.healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			for idx, st := range p.replicas {
				p.probe(idx, st)
			}
		}
	}
}

func (p *RoutingPool) probe(idx int, st *replicaState) {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.probeTimeout)
	defer cancel()
	if err := st.pool.Ping(ctx); err != nil {
		p.recordReplicaFailure(idx, err)
		return
	}
	// Successful probe — clear failure counter, mark healthy if it
	// was unhealthy.
	st.consecutiveFails.Store(0)
	if st.healthy.CompareAndSwap(false, true) {
		p.cfg.logger.Info("readreplica: replica re-added to rotation",
			slog.Int("replica_index", idx),
		)
		p.metrics.healthyReplicas.Inc()
	}
}

// Close stops the background health loop and closes every pool the
// RoutingPool owns. Idempotent: subsequent calls are no-ops.
//
// Note: only the primary + replica pools passed in via Config are
// closed. The pgxpool docs require Close to run once per pool, so the
// caller MUST NOT reuse those pools after RoutingPool.Close.
func (p *RoutingPool) Close() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		p.wg.Wait()
		p.primary.Close()
		for _, r := range p.replicas {
			r.pool.Close()
		}
	})
}

// PrimaryHealthy returns true if the primary's Ping succeeded most
// recently (cheap snapshot for health endpoints; does not probe).
// Returns true on a fresh pool until proven otherwise.
func (p *RoutingPool) PrimaryHealthy(ctx context.Context) bool {
	return p.primary.Ping(ctx) == nil
}

// ReplicaHealth returns a snapshot of replica health flags by index.
// Index order matches Config.Replicas. Useful for health endpoints
// that want to expose per-replica status.
func (p *RoutingPool) ReplicaHealth() []bool {
	out := make([]bool, len(p.replicas))
	for i, st := range p.replicas {
		out[i] = st.healthy.Load()
	}
	return out
}
