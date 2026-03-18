package redis

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

const (
	pingTimeout = 5 * time.Second
)

// Connection manages a Redis client with health monitoring and lifecycle channels.
// go-redis handles per-command reconnection internally; this type provides a
// health boolean for readiness probes and an onReconnect callback for
// re-subscriptions or script re-registration.
type Connection struct {
	client redis.UniversalClient

	mu                   sync.RWMutex
	healthy              bool
	consecutiveFailures  int
	reconnecting         bool
	closed               chan struct{}
	closeOnce            sync.Once
	dead                 chan struct{}
	deadOnce             sync.Once
	connected            chan struct{}
	connectedOnce        sync.Once

	logger               *slog.Logger
	instance             string
	maxReconnectAttempts int
	lazyConnect          bool
	healthInterval       time.Duration
	onReconnect          func(context.Context, *Connection) error

	metrics *RedisMetrics
}

// ConnOption configures a Connection.
type ConnOption func(*Connection)

// WithLogger sets a structured logger for connection events.
func WithLogger(l *slog.Logger) ConnOption {
	return func(c *Connection) { c.logger = l }
}

// WithMaxReconnectAttempts limits reconnection attempts. 0 means unlimited.
// Negative values are ignored. When the limit is reached, the Dead() channel
// is closed to signal permanent failure.
func WithMaxReconnectAttempts(n int) ConnOption {
	return func(c *Connection) {
		if n >= 0 {
			c.maxReconnectAttempts = n
		}
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
// load on the Redis server. Values <= 0 are ignored (the default is used).
func WithHealthInterval(d time.Duration) ConnOption {
	return func(c *Connection) {
		if d > 0 {
			c.healthInterval = d
		}
	}
}

// WithInstance sets the Prometheus instance label for this connection.
// Use a small, static name like "cache" or "streams" to distinguish
// multiple connections in the same process. Defaults to "default".
// Panics if the name is invalid (empty, contains null bytes, etc.).
func WithInstance(name string) ConnOption {
	return func(c *Connection) {
		if err := ValidateName(name, "instance"); err != nil {
			panic("redis: " + err.Error())
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
	return func(c *Connection) { c.onReconnect = fn }
}

// WithRegisterer sets the Prometheus registerer for connection metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithRegisterer(reg prometheus.Registerer) ConnOption {
	return func(c *Connection) {
		c.metrics = NewRedisMetrics(reg)
	}
}

// Connect creates a new Redis connection. With WithLazyConnect, it returns
// immediately while connecting in the background. Without it, Connect blocks
// until the first successful ping or returns an error.
// Connect creates a new Redis connection and starts the health monitoring loop.
// By default, it performs an eager ping to verify connectivity before returning.
// Use WithLazyConnect to defer the initial ping to the background.
//
// WARNING: If Close() is called very quickly after Connect (before the health
// loop goroutine starts), a brief spurious reconnect attempt may be logged.
// This is harmless but can cause confusing log output in tests. Use
// WithLazyConnect if rapid connect/close cycles are expected.
func Connect(opts *redis.Options, connOpts ...ConnOption) (*Connection, error) {
	c := &Connection{
		closed:         make(chan struct{}),
		dead:           make(chan struct{}),
		connected:      make(chan struct{}),
		logger:         slog.Default(),
		instance:       "default",
		healthInterval: 5 * time.Second,
		metrics:        defaultMetrics,
	}
	for _, o := range connOpts {
		o(c)
	}

	client := redis.NewClient(opts)
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
	return c.client
}

// Healthy reports whether the connection is currently healthy. This is used
// by health check integrations (health.DependencyCheck).
func (c *Connection) Healthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy
}

// WasConnected reports whether the connection has ever been successfully
// established. Used by health checks to distinguish "still connecting on
// startup" from "was healthy but lost connection".
func (c *Connection) WasConnected() bool {
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
	return c.connected
}

// Dead returns a channel that is closed when reconnection has been
// permanently abandoned (maxReconnectAttempts exceeded).
func (c *Connection) Dead() <-chan struct{} {
	return c.dead
}

// Close shuts down the connection and stops the health monitor loop.
// It is safe to call multiple times; subsequent calls return nil.
func (c *Connection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.client.Close()
		c.mu.Lock()
		c.healthy = false
		c.mu.Unlock()
		c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(0)
		c.logger.Info("redis connection closed")
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

	c.mu.Lock()
	wasHealthy := c.healthy
	c.healthy = err == nil

	if err == nil {
		c.consecutiveFailures = 0
		c.mu.Unlock()

		c.metrics.connectionHealthy.WithLabelValues(c.instance).Set(1)
		c.connectedOnce.Do(func() { close(c.connected) })

		if !wasHealthy {
			c.logger.Info("redis connection established")
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
		c.logger.Error("redis connection lost", "error", err)
	} else {
		c.logger.Warn("redis still unhealthy",
			"error", err,
			"consecutive_failures", failures,
		)
	}

	if c.maxReconnectAttempts > 0 && failures >= c.maxReconnectAttempts {
		c.logger.Error("redis max reconnect attempts reached", "attempts", failures)
		c.deadOnce.Do(func() { close(c.dead) })
	}
}

// onReconnectTimeout bounds how long an onReconnect callback may run.
// Prevents user-supplied callbacks from blocking indefinitely and leaking
// goroutines. 30 seconds is generous for topology declarations or
// re-subscriptions.
const onReconnectTimeout = 30 * time.Second

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
			c.mu.Lock()
			c.reconnecting = false
			c.mu.Unlock()
		}()
		select {
		case <-c.closed:
			return
		default:
		}

		// Create a context bounded by both the timeout and the connection lifetime.
		// This allows callbacks to use ctx.Done() for clean cancellation.
		ctx, cancel := context.WithTimeout(context.Background(), onReconnectTimeout)
		defer cancel()
		go func() {
			select {
			case <-c.closed:
				cancel()
			case <-ctx.Done():
			}
		}()

		done := make(chan error, 1)
		go func() { done <- c.onReconnect(ctx, c) }()
		select {
		case err := <-done:
			if err != nil {
				select {
				case <-c.closed:
					c.logger.Warn("redis connection closed during onReconnect callback")
				default:
					c.logger.Error("redis onReconnect callback failed", "error", err)
				}
			}
		case <-ctx.Done():
			c.logger.Error("redis onReconnect callback timed out or connection closed",
				"timeout", onReconnectTimeout)
			// Wait for the callback goroutine to finish — ctx cancellation
			// propagates to the callback, which should return promptly.
			<-done
		}
	}()
}
