package amqpbackend

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/resilience/retry"
)

// Connection manages an AMQP connection with automatic reconnection.
type Connection struct {
	url                  string
	tlsConfig            *tls.Config
	conn                 *amqp.Connection
	mu                   sync.RWMutex
	logger               *slog.Logger
	closed               chan struct{}
	closeOnce            sync.Once
	dead                 chan struct{}
	deadOnce             sync.Once
	connected            chan struct{} // closed on first successful connect
	connectedOnce        sync.Once     // ensures connected is closed exactly once
	maxReconnectAttempts int           // 0 = unlimited
	lazyConnect          bool          // defer initial connection to background
	generation           uint64        // incremented on each reconnect; stale watchers self-terminate
	reconnecting         atomic.Bool   // prevents overlapping reconnect goroutines
	reconnectSignal      chan struct{} // buffered(1); queues a reconnect when loop is finishing

	// onReconnect is called after a successful reconnect. Typically used
	// to re-declare topology. Best-effort: failures are logged but do not
	// prevent the connection from being used.
	onReconnect func(Connector) error
}

// DialOption configures a Connection during Dial.
type DialOption func(*Connection)

// WithMaxReconnectAttempts sets the maximum number of reconnection attempts.
// 0 (the default) means unlimited — the connection will retry forever with
// exponential backoff. Use this for services that must stay alive regardless
// of how long RabbitMQ is down.
func WithMaxReconnectAttempts(n int) DialOption {
	return func(c *Connection) {
		c.maxReconnectAttempts = n
	}
}

// OnReconnect registers a callback invoked after each successful reconnect.
// The callback receives a Connector so it can open channels to re-declare
// topology. Errors are logged but do not prevent consumers from retrying.
func OnReconnect(fn func(Connector) error) DialOption {
	return func(c *Connection) {
		c.onReconnect = fn
	}
}

// WithTLS configures mTLS for the AMQP connection. When set, the connection
// uses amqp.DialTLS instead of amqp.Dial, presenting a client certificate
// and verifying the server against the provided CA.
func WithTLS(cfg *tls.Config) DialOption {
	return func(c *Connection) {
		c.tlsConfig = cfg
	}
}

// WithLazyConnect defers the initial AMQP connection to a background goroutine.
// Dial returns immediately with a Connection whose Healthy() is false. The
// reconnect loop starts in the background, connecting when RabbitMQ becomes
// available. This allows services to start serving HTTP traffic while waiting
// for the broker. Use Connected() to be notified when the first connection succeeds.
func WithLazyConnect() DialOption {
	return func(c *Connection) {
		c.lazyConnect = true
	}
}

// Dial establishes a new AMQP connection and starts monitoring for disconnects.
// By default, reconnection retries are unlimited. Use WithMaxReconnectAttempts
// to set a finite limit (Dead() fires when exhausted).
//
// With WithLazyConnect(), Dial returns immediately without connecting. The
// connection is established in the background via the reconnect loop.
func Dial(url string, logger *slog.Logger, opts ...DialOption) (*Connection, error) {
	if url == "" {
		return nil, fmt.Errorf("amqp URL must not be empty")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger must not be nil")
	}

	c := &Connection{
		url:             url,
		logger:          logger,
		closed:          make(chan struct{}),
		dead:            make(chan struct{}),
		connected:       make(chan struct{}),
		reconnectSignal: make(chan struct{}, 1),
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.lazyConnect {
		logger.Info("amqp lazy connect enabled, connecting in background", "url", sanitizeURL(url))
		c.startReconnect()
		return c, nil
	}

	conn, err := c.dial()
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}

	c.conn = conn
	c.generation = 1
	c.connectedOnce.Do(func() { close(c.connected) })

	go c.watchConnection(conn, 1)

	return c, nil
}

// amqpConnection returns the underlying amqp.Connection for use by Watermill.
// Returns nil if not connected. Callers must handle the nil case.
func (c *Connection) amqpConnection() *amqp.Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}

// Channel opens a new AMQP channel on the current connection.
func (c *Connection) Channel() (*amqp.Channel, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.conn == nil || c.conn.IsClosed() {
		return nil, fmt.Errorf("connection is not available")
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}

	return ch, nil
}

// Close terminates the AMQP connection and stops reconnection attempts.
// It is safe to call Close multiple times.
func (c *Connection) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.closed)

		c.mu.Lock()
		defer c.mu.Unlock()

		if c.conn != nil && !c.conn.IsClosed() {
			if err := c.conn.Close(); err != nil {
				closeErr = fmt.Errorf("messaging: close connection: %w", err)
			}
		}
	})
	return closeErr
}

// Healthy reports whether the AMQP connection is alive and has not been
// permanently lost. It is safe for concurrent use.
func (c *Connection) Healthy() bool {
	select {
	case <-c.dead:
		return false
	default:
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.conn != nil && !c.conn.IsClosed()
}

// Dead returns a channel that is closed when the connection is permanently
// lost after exhausting all reconnection attempts. If maxReconnectAttempts
// is 0 (unlimited), this channel is never closed.
func (c *Connection) Dead() <-chan struct{} {
	return c.dead
}

// Connected returns a channel that is closed when the first successful
// connection is established. For non-lazy connections this is already
// closed when Dial returns. For lazy connections, select on this to know
// when the broker becomes available.
func (c *Connection) Connected() <-chan struct{} {
	return c.connected
}

func (c *Connection) watchConnection(conn *amqp.Connection, gen uint64) {
	notifyClose := conn.NotifyClose(make(chan *amqp.Error, 1))

	select {
	case <-c.closed:
		return
	case amqpErr, ok := <-notifyClose:
		// Check generation to prevent stale watchers from triggering reconnect.
		c.mu.RLock()
		currentGen := c.generation
		c.mu.RUnlock()
		if gen != currentGen {
			return
		}

		if !ok {
			// notifyClose closed without an error. If we're not shutting down,
			// treat this as a connection loss (e.g. clean broker restart).
			select {
			case <-c.closed:
				return
			default:
				c.logger.Warn("amqp notifyClose channel closed unexpectedly, triggering reconnect")
				c.startReconnect()
			}
			return
		}
		c.logger.Error("amqp connection lost", "error", amqpErr)
		c.startReconnect()
	}
}

// startReconnect spawns reconnect in a goroutine with an atomic guard
// to prevent overlapping reconnect loops. If a reconnect is already in
// progress, a signal is queued so the loop retries after completing its
// current attempt (preventing lost reconnect signals from R7-46).
func (c *Connection) startReconnect() {
	if !c.reconnecting.CompareAndSwap(false, true) {
		// Reconnect already running — queue a signal so it retries
		// if it was about to exit.
		select {
		case c.reconnectSignal <- struct{}{}:
		default:
		}
		return
	}
	go func() {
		defer c.reconnecting.Store(false)
		c.reconnect()
	}()
}

func (c *Connection) reconnect() {
	bo := retry.WorkerPolicy.NewBackoff()
	attempts := 0

	for {
		if c.maxReconnectAttempts > 0 && attempts >= c.maxReconnectAttempts {
			c.logger.Error("amqp max reconnect attempts reached", "attempts", attempts)
			c.deadOnce.Do(func() { close(c.dead) })
			return
		}

		delay := bo.Next()
		timer := time.NewTimer(delay)
		select {
		case <-c.closed:
			timer.Stop()
			return
		case <-timer.C:
		}

		logAttrs := []any{"attempt", attempts + 1}
		if c.maxReconnectAttempts > 0 {
			logAttrs = append(logAttrs, "max", c.maxReconnectAttempts)
		}
		c.logger.Info("attempting amqp connect", logAttrs...)

		conn, err := c.dial()
		if err != nil {
			c.logger.Error("amqp reconnect failed", "error", err, "attempt", attempts+1, "url", sanitizeURL(c.url))
			attempts++
			continue
		}

		c.mu.Lock()
		old := c.conn
		c.conn = conn
		c.generation++
		gen := c.generation
		// Close old connection inside the lock to prevent a race with
		// Close() which also acquires mu and closes c.conn.
		if old != nil {
			if err := old.Close(); err != nil {
				c.logger.Debug("failed to close old amqp connection", "error", err)
			}
		}
		c.mu.Unlock()

		c.logger.Info("amqp connected successfully", "attempts", attempts+1)
		c.connectedOnce.Do(func() { close(c.connected) })
		// Reset backoff and attempt counter after a successful connection
		// so the full budget is available if the connection drops again.
		bo.Reset()
		attempts = 0

		// Start watching BEFORE onReconnect so connection drops during
		// topology re-declaration (which can be slow) are detected.
		go c.watchConnection(conn, gen)

		if c.onReconnect != nil {
			if err := c.onReconnect(c); err != nil {
				c.logger.Error("onReconnect callback failed, will retry connection", "error", err)
				// Topology declaration failed — the connection is alive but
				// unusable (exchanges/queues may not exist). Close and retry
				// to prevent silent message loss.
				c.mu.Lock()
				if c.conn != nil && !c.conn.IsClosed() {
					_ = c.conn.Close()
				}
				c.mu.Unlock()
				attempts++
				continue
			}
		}

		// Check if the connection survived onReconnect — a topology
		// declaration error can cause the broker to close the connection.
		// If dead, loop back to retry instead of returning with a dead conn.
		c.mu.RLock()
		connAlive := c.conn != nil && !c.conn.IsClosed()
		c.mu.RUnlock()
		if !connAlive {
			c.logger.Warn("amqp connection dropped during onReconnect, retrying")
			// Keep the current backoff instead of resetting. This prevents
			// a tight retry loop when onReconnect consistently fails.
			attempts++
			continue
		}

		// Drain any queued reconnect signal before returning. If
		// watchConnection detected a drop while we were finishing, we
		// must loop back to handle it (prevents lost signals from R7-46).
		select {
		case <-c.reconnectSignal:
			c.logger.Info("reconnect signal received during finalization, retrying")
			bo.Reset()
			continue
		default:
		}
		return
	}
}

// dial opens an AMQP connection, using TLS when configured.
// The TLS config is cloned before each dial to prevent amqp.DialTLS from
// mutating the shared *tls.Config (Go's TLS handshake writes ServerName
// into the config when it is empty).
func (c *Connection) dial() (*amqp.Connection, error) {
	if c.tlsConfig != nil {
		return amqp.DialTLS(c.url, c.tlsConfig.Clone())
	}
	return amqp.Dial(c.url)
}

// sanitizeURL strips credentials from an AMQP URL for safe logging.
// Returns the URL with any userinfo replaced by "***:***".
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "***"
	}
	if u.User != nil {
		u.User = url.UserPassword("***", "***")
	}
	return u.String()
}
