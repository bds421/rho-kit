package websocket

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// DefaultMaxMessageBytes mirrors infra/messaging's per-message ceiling
// (1 MiB) so a service that limits broker payloads gets the same
// guarantee on its WebSocket ingress without having to plumb the
// constant manually. Keep these values aligned by hand — infra is not
// imported here to avoid pulling its dependency closure into a
// transport-level module.
const DefaultMaxMessageBytes int64 = 1 << 20

// HandlerFunc is the kit-flavored WebSocket handler.
//
// The context is the per-request context derived from the upgrade
// request and cancelled when the connection closes for any reason
// (peer close, server shutdown, idle timeout, panic). The Conn is a
// kit wrapper that emits metrics, logs panics, and redacts inner
// error text on every read/write/close.
type HandlerFunc func(ctx Context, conn *Conn) error

// Option configures [Handle].
type Option func(*config)

type config struct {
	handler        HandlerFunc
	subprotocols   []string
	compression    bool
	maxMessageSize int64
	logger         *slog.Logger
	metrics        *Metrics
	originPatterns []string
}

func defaultConfig() config {
	return config{
		maxMessageSize: DefaultMaxMessageBytes,
		logger:         slog.Default(),
	}
}

// WithHandler installs the application callback invoked once the
// upgrade completes. Required: [Handle] panics on registration if no
// handler is configured, since serving a WebSocket with no handler is
// always a bug rather than a runtime condition to absorb.
func WithHandler(h HandlerFunc) Option {
	if h == nil {
		panic("httpx/websocket: WithHandler requires a non-nil handler")
	}
	return func(c *config) { c.handler = h }
}

// WithSubprotocols restricts the negotiated subprotocols. When unset,
// the empty subprotocol is the only accepted value (RFC 6455 default).
func WithSubprotocols(subprotocols ...string) Option {
	return func(c *config) {
		c.subprotocols = append([]string(nil), subprotocols...)
	}
}

// WithCompression enables permessage-deflate. The coder/websocket
// default is no compression; opt in only when the deployment has
// validated that the encrypted payload mix is not vulnerable to
// CRIME/BREACH-style oracles.
func WithCompression() Option {
	return func(c *config) { c.compression = true }
}

// WithMaxMessageBytes overrides the per-message read ceiling.
// The default is [DefaultMaxMessageBytes] (1 MiB), mirroring the
// infra/messaging convention. Pass a positive size; non-positive
// values panic so misconfiguration surfaces at startup.
func WithMaxMessageBytes(size int64) Option {
	if size <= 0 {
		panic("httpx/websocket: WithMaxMessageBytes requires a positive size")
	}
	return func(c *config) { c.maxMessageSize = size }
}

// WithMetrics registers the kit metric set on reg. Pass nil to fall
// back to [prometheus.DefaultRegisterer] semantics via
// [NewMetrics]; the kit convention is for callers to supply an
// explicit registerer rather than relying on the global, so this
// option panics on nil to surface miswiring at startup.
func WithMetrics(reg prometheus.Registerer) Option {
	if reg == nil {
		panic("httpx/websocket: WithMetrics requires a non-nil registerer (omit the option for unmetered)")
	}
	return func(c *config) {
		c.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// WithLogger installs a structured logger for connection lifecycle
// events (accept, close, panic). Defaults to [slog.Default]. Nil
// loggers panic so the option is not silently dropped.
func WithLogger(logger *slog.Logger) Option {
	if logger == nil {
		panic("httpx/websocket: WithLogger requires a non-nil logger")
	}
	return func(c *config) { c.logger = logger }
}

// WithOriginPatterns sets the allowed origin host patterns enforced by
// coder/websocket's Accept. Patterns are case-insensitive
// path.Match-style globs. Without this option the request host is
// authorised automatically and cross-origin handshakes are rejected —
// callers who need to allow specific cross-origin browsers must opt in
// explicitly.
func WithOriginPatterns(patterns ...string) Option {
	return func(c *config) {
		c.originPatterns = append([]string(nil), patterns...)
	}
}
