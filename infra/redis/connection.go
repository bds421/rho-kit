package redis

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
)

const (
	pingTimeout               = 5 * time.Second
	defaultOnReconnectTimeout = 30 * time.Second
)

// Connection manages a Redis client with health monitoring and lifecycle channels.
// go-redis handles per-command reconnection internally; this type provides a
// health boolean for readiness probes and an onReconnect callback for
// re-subscriptions or script re-registration.
//
// Safe for concurrent use — the embedded UniversalClient is goroutine-safe;
// the health bool and reconnect state are RWMutex-guarded.
type Connection struct {
	client redis.UniversalClient

	mu                  sync.RWMutex
	healthy             bool
	readOnly            bool
	consecutiveFailures int
	reconnecting        bool
	closed              chan struct{}
	closeOnce           sync.Once
	dead                chan struct{}
	deadOnce            sync.Once
	connected           chan struct{}
	connectedOnce       sync.Once

	logger               *slog.Logger
	instance             string
	maxReconnectAttempts int
	lazyConnect          bool
	healthInterval       time.Duration
	onReconnectTimeout   time.Duration
	onReconnect          func(context.Context, *Connection) error

	metrics *Metrics
}

// ConnOption configures a Connection.
type ConnOption func(*Connection)

// WithLogger sets a structured logger for connection events. A nil logger is
// normalized to [slog.Default] so test wiring stays ergonomic.
func WithLogger(l *slog.Logger) ConnOption {
	return func(c *Connection) {
		if l == nil {
			c.logger = slog.Default()
			return
		}
		c.logger = l
	}
}

// WithMaxReconnectAttempts limits reconnection attempts. 0 means unlimited.
// Negative values panic. When the limit is reached, the Dead() channel
// is closed to signal permanent failure.
func WithMaxReconnectAttempts(n int) ConnOption {
	if n < 0 {
		panic("redis: WithMaxReconnectAttempts requires n >= 0")
	}
	return func(c *Connection) {
		c.maxReconnectAttempts = n
	}
}

// WithLazyConnect defers the initial connection to the background. Connect
// returns immediately and the health probe loop starts asynchronously.
// Services can start accepting requests while Redis is still connecting.
func WithLazyConnect() ConnOption {
	return func(c *Connection) { c.lazyConnect = true }
}

// WithHealthInterval sets how frequently the health loop pings Redis.
// Defaults to 5 seconds. Lower values detect outages faster but increase
// load on the Redis server. The duration must be positive.
func WithHealthInterval(d time.Duration) ConnOption {
	if d <= 0 {
		panic("redis: WithHealthInterval requires a positive duration")
	}
	return func(c *Connection) {
		c.healthInterval = d
	}
}

// WithInstance sets the Prometheus instance label for this connection.
// Use a small, static name like "cache" or "streams" to distinguish
// multiple connections in the same process. Defaults to "default".
// Panics if the name is invalid (empty, contains null bytes, etc.).
func WithInstance(name string) ConnOption {
	return func(c *Connection) {
		if err := ValidateName(name, "instance"); err != nil {
			panic("redis: WithInstance: invalid instance name")
		}
		c.instance = name
	}
}

// WithOnReconnect registers a callback invoked after each successful
// reconnection (transition from unhealthy → healthy). Use it to
// re-subscribe to channels or re-register scripts.
//
// The callback runs asynchronously in a separate goroutine to avoid blocking
// health monitoring. The context is cancelled when the connection is closed
// or the callback timeout (30s) elapses — use it for clean cancellation of
// long-running operations. Errors are logged but do not prevent the
// connection from being used.
func WithOnReconnect(fn func(ctx context.Context, conn *Connection) error) ConnOption {
	if fn == nil {
		panic("redis: WithOnReconnect requires a non-nil callback")
	}
	return func(c *Connection) { c.onReconnect = fn }
}

// WithOnReconnectTimeout bounds each onReconnect callback. The timeout
// context is cancelled when d elapses or the connection closes.
func WithOnReconnectTimeout(d time.Duration) ConnOption {
	if d <= 0 {
		panic("redis: WithOnReconnectTimeout requires a positive duration")
	}
	return func(c *Connection) {
		c.onReconnectTimeout = d
	}
}

// WithMetricsRegisterer sets the Prometheus registerer for connection
// metrics. Kit-canonical name on a top-level [ConnOption] threading
// the registerer through to the metrics builder (mirrors
// `infra/storage/{azure,gcs,s3,sftp}backend.WithMetricsRegisterer`,
// `infra/leaderelection/{pgadvisory,redislock}.WithMetricsRegisterer`,
// `grpcx.WithMetricsRegisterer`). The MetricsOption-typed
// [WithRegisterer] in the same package is the inner builder option.
// Defaults to [prometheus.DefaultRegisterer].
func WithMetricsRegisterer(reg prometheus.Registerer) ConnOption {
	return func(c *Connection) {
		if reg == nil {
			c.metrics = NewMetrics()
			return
		}
		c.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// Connect creates a new Redis connection. With WithLazyConnect, it returns
// immediately while connecting in the background. Without it, Connect blocks
// until the first successful ping or returns an error.
// Connect creates a new Redis connection and starts the health monitoring loop.
// By default, it performs an eager ping to verify connectivity before returning.
// Use WithLazyConnect to defer the initial ping to the background.
//
// Single-node Redis only. For Sentinel or Cluster topologies use
// [ConnectUniversal], which accepts redis.UniversalOptions.
//
// WARNING: If Close() is called very quickly after Connect (before the health
// loop goroutine starts), a brief spurious reconnect attempt may be logged.
// This is harmless but can cause confusing log output in tests. Use
// WithLazyConnect if rapid connect/close cycles are expected.
func Connect(opts *redis.Options, connOpts ...ConnOption) (*Connection, error) {
	if opts == nil {
		panic("redis: Connect requires non-nil options")
	}
	copied, err := cloneOptions(opts)
	if err != nil {
		return nil, err
	}
	return connectInternal(redis.NewClient(copied), connOpts...)
}

// ConnectUniversal is the Sentinel/Cluster-aware constructor. opts is the
// goredis UniversalOptions struct: when MasterName is set it picks
// Sentinel; when len(Addrs) > 1 it picks Cluster; otherwise single-node.
// The returned Connection is otherwise identical to one returned by
// [Connect] — Client() returns the same UniversalClient interface.
func ConnectUniversal(opts *redis.UniversalOptions, connOpts ...ConnOption) (*Connection, error) {
	if opts == nil {
		panic("redis: ConnectUniversal requires non-nil options")
	}
	copied, err := cloneUniversalOptions(opts)
	if err != nil {
		return nil, err
	}
	return connectInternal(redis.NewUniversalClient(copied), connOpts...)
}

func cloneOptions(opts *redis.Options) (*redis.Options, error) {
	copied := *opts
	var err error
	if copied.TLSConfig != nil {
		copied.TLSConfig, err = cloneTLSConfigWithFloor(copied.TLSConfig)
		if err != nil {
			return nil, err
		}
	}
	if copied.MaintNotificationsConfig != nil {
		cfg := *copied.MaintNotificationsConfig
		copied.MaintNotificationsConfig = &cfg
	}
	return &copied, nil
}

func cloneUniversalOptions(opts *redis.UniversalOptions) (*redis.UniversalOptions, error) {
	copied := *opts
	copied.Addrs = append([]string(nil), opts.Addrs...)
	var err error
	if copied.TLSConfig != nil {
		copied.TLSConfig, err = cloneTLSConfigWithFloor(copied.TLSConfig)
		if err != nil {
			return nil, err
		}
	}
	if copied.MaintNotificationsConfig != nil {
		cfg := *copied.MaintNotificationsConfig
		copied.MaintNotificationsConfig = &cfg
	}
	return &copied, nil
}

func connectInternal(client redis.UniversalClient, connOpts ...ConnOption) (*Connection, error) {
	if client == nil {
		panic("redis: connectInternal requires a non-nil client")
	}
	c := &Connection{
		closed:             make(chan struct{}),
		dead:               make(chan struct{}),
		connected:          make(chan struct{}),
		logger:             slog.Default(),
		instance:           "default",
		healthInterval:     5 * time.Second,
		onReconnectTimeout: defaultOnReconnectTimeout,
		metrics:            defaultMetrics(),
	}
	for _, o := range connOpts {
		if o == nil {
			panic("redis: connection option must not be nil")
		}
		o(c)
	}

	client.AddHook(&metricsHook{instance: c.instance, metrics: c.metrics})
	c.client = client

	if c.lazyConnect {
		go c.healthLoop()
		return c, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis connect: %w", err)
	}

	c.mu.Lock()
	c.healthy = true
	c.mu.Unlock()
	c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(1)
	c.connectedOnce.Do(func() { close(c.connected) })

	go c.healthLoop()
	return c, nil
}

// Client returns the underlying Redis client. The returned client is safe
// for concurrent use and handles connection pooling internally.
func (c *Connection) Client() redis.UniversalClient {
	if c == nil {
		return nil
	}
	return c.client
}

// Metrics returns the [Metrics] instance backing this connection's
// observability hooks. Use it to pass [WithPoolMetrics] into
// [StartPoolMetricsCollector] so pool gauges land on the same
// Prometheus registry as connection / command metrics — without
// this hop, callers that built the connection with [WithRegisterer]
// would see pool gauges silently routed to the default registry.
//
// Returns nil on a nil receiver; otherwise the receiver always has
// a non-nil metrics instance.
func (c *Connection) Metrics() *Metrics {
	if c == nil {
		return nil
	}
	return c.metrics
}

// Healthy reports whether the connection is currently healthy. This is used
// by health check integrations (health.DependencyCheck).
//
// A connection that is reachable but has flipped to READONLY (failover in
// progress, primary demoted to replica) is reported as unhealthy: writes
// will fail until a replica is promoted. Read-only callers can still call
// [Connection.Client] directly when [Connection.ReadOnly] is true.
func (c *Connection) Healthy() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy && !c.readOnly
}

// ReadOnly reports whether the connection has observed a READONLY reply from
// the server since the last successful write probe. When true, write commands
// are expected to fail with [ErrPrimaryReadOnly] until a Sentinel/Cluster
// failover completes (or an operator promotes a replica).
func (c *Connection) ReadOnly() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.readOnly
}

// MarkReadOnly records that a command surfaced a READONLY reply. This is
// called by command hooks and may be invoked by callers that detect a
// READONLY response on their own pipeline. The flag is cleared on the next
// successful health probe.
func (c *Connection) MarkReadOnly() {
	if c == nil {
		return
	}
	c.mu.Lock()
	wasReadOnly := c.readOnly
	c.readOnly = true
	c.healthy = false
	c.mu.Unlock()
	if !wasReadOnly {
		if c.metrics != nil {
			c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(0)
		}
		if c.logger != nil {
			c.logger.Warn("redis primary is read-only — failover in progress", "instance", c.instance)
		}
	}
}

// WasConnected reports whether the connection has ever been successfully
// established. Used by health checks to distinguish "still connecting on
// startup" from "was healthy but lost connection".
func (c *Connection) WasConnected() bool {
	if c == nil || c.connected == nil {
		return false
	}
	select {
	case <-c.connected:
		return true
	default:
		return false
	}
}

// Connected returns a channel that is closed when the first successful
// connection is established. Useful for services that need Redis before
// starting their main loop.
func (c *Connection) Connected() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.connected
}

// Dead returns a channel that is closed when reconnection has been
// permanently abandoned (maxReconnectAttempts exceeded).
func (c *Connection) Dead() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.dead
}

// Close shuts down the connection and stops the health monitor loop.
// It is safe to call multiple times; subsequent calls return nil.
func (c *Connection) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		if c.closed != nil {
			close(c.closed)
		}
		if c.client != nil {
			err = c.client.Close()
		}
		c.mu.Lock()
		c.healthy = false
		c.mu.Unlock()
		if c.metrics != nil {
			c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(0)
		}
		if c.logger != nil {
			c.logger.Info("redis connection closed")
		}
	})
	return err
}

// healthLoop periodically pings Redis and manages health state transitions.
// A single ticker replaces the previous 3-goroutine state machine. go-redis
// handles per-command reconnection; we only track health for readiness probes
// and fire onReconnect on false→true transitions.
func (c *Connection) healthLoop() {
	ticker := time.NewTicker(c.healthInterval)
	defer ticker.Stop()

	// Initial check — needed for lazy connect.
	c.checkHealth()

	for {
		select {
		case <-c.closed:
			return
		case <-c.dead:
			// Permanent failure declared — stop pinging. Earlier
			// versions kept ticking forever, wasting Redis cycles on
			// a server the kit had given up on.
			return
		case <-ticker.C:
			c.checkHealth()
		}
	}
}

// checkHealth pings Redis and updates the health state. On a false→true
// transition, it fires the onReconnect callback. On consecutive failures
// exceeding maxReconnectAttempts, it signals permanent failure.
func (c *Connection) checkHealth() {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	err := c.client.Ping(ctx).Err()

	// A READONLY reply on ping is unusual but possible when a node has
	// just been demoted: treat it as the master being unavailable for
	// writes. The dedicated readOnly bit lets callers branch on it.
	if err != nil && IsReadOnlyError(err) {
		c.MarkReadOnly()
		// Fall through: still report this probe as failed so health
		// metrics and consecutiveFailures advance.
	}

	c.mu.Lock()
	wasHealthy := c.healthy
	c.healthy = err == nil

	if err == nil {
		c.consecutiveFailures = 0
		// A clean PING also clears any sticky READONLY flag — the
		// node is responding normally to commands again. Sentinel
		// will have routed us to a fresh primary at this point.
		wasReadOnly := c.readOnly
		c.readOnly = false
		c.mu.Unlock()

		c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(1)
		c.connectedOnce.Do(func() { close(c.connected) })

		if !wasHealthy || wasReadOnly {
			if wasReadOnly {
				c.logger.Info("redis primary writable again", "instance", c.instance)
			} else {
				c.logger.Info("redis connection established")
			}
			c.metrics.reconnectSuccesses.WithLabelValues(c.instance).Inc()
			c.fireOnReconnect()
		}
		return
	}

	c.consecutiveFailures++
	failures := c.consecutiveFailures
	c.mu.Unlock()

	c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(0)
	c.metrics.reconnectAttempts.WithLabelValues(c.instance).Inc()

	if wasHealthy {
		c.logger.Error("redis connection lost", redact.Error(err))
	} else {
		c.logger.Warn("redis still unhealthy",
			redact.Error(err),
			"consecutive_failures", failures,
		)
	}

	if c.maxReconnectAttempts > 0 && failures >= c.maxReconnectAttempts {
		c.logger.Error("redis max reconnect attempts reached", "attempts", failures)
		c.deadOnce.Do(func() { close(c.dead) })
	}
}

func (c *Connection) fireOnReconnect() {
	if c.onReconnect == nil {
		return
	}
	// Serialize reconnect callbacks — if one is already in progress,
	// skip this trigger. This prevents goroutine leaks when Redis
	// flaps rapidly (healthy→unhealthy→healthy).
	c.mu.Lock()
	if c.reconnecting {
		c.mu.Unlock()
		c.logger.Debug("redis onReconnect already in progress, skipping")
		return
	}
	c.reconnecting = true
	c.mu.Unlock()

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				c.logger.Error("redis onReconnect callback panicked",
					redact.Panic(rec),
					"stack", string(debug.Stack()),
				)
			}
			c.mu.Lock()
			c.reconnecting = false
			c.mu.Unlock()
		}()
		select {
		case <-c.closed:
			return
		default:
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		defer close(done)

		timer := time.NewTimer(c.onReconnectTimeout)
		defer timer.Stop()
		go func() {
			select {
			case <-c.closed:
				cancel()
			case <-timer.C:
				c.logger.Error("redis onReconnect callback timed out",
					"timeout", c.onReconnectTimeout)
				cancel()
			case <-done:
			}
		}()

		if err := c.onReconnect(ctx, c); err != nil {
			select {
			case <-c.closed:
				c.logger.Warn("redis connection closed during onReconnect callback")
			default:
				c.logger.Error("redis onReconnect callback failed", redact.Error(err))
			}
		}
	}()
}
