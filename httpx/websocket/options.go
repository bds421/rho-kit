package websocket

import (
	"log/slog"
	"time"

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

// compressionMode selects the permessage-deflate behaviour applied at
// upgrade. The default (zero value) disables compression so each
// connection starts with a deterministic, bounded memory footprint.
type compressionMode int

const (
	// compressionDisabled is the zero value: no permessage-deflate is
	// negotiated. Named so the switch in [Handle] reads naturally
	// even though the case is implicit (default branch).
	_ compressionMode = iota
	// compressionNoTakeover compresses each message independently —
	// no inter-message LZ77 state. Bounded per-conn memory.
	compressionNoTakeover
	// compressionContextTakeover persists the LZ77 sliding window
	// between messages for better ratios on similar payloads, at a
	// cost of ~32 KiB per direction per connection.
	compressionContextTakeover
)

type config struct {
	handler        HandlerFunc
	subprotocols   []string
	compression    compressionMode
	maxMessageSize int64
	maxConnections int64
	writeTimeout   time.Duration
	pingInterval   time.Duration
	pongTimeout    time.Duration
	readDrain      bool
	logger         *slog.Logger
	metrics        *Metrics
	originPatterns []string
}

// defaultConfig leaves heartbeat and write timeout disabled. Callers
// serving untrusted clients SHOULD set [WithPingInterval] and
// [WithWriteTimeout] (and usually [WithMaxConnections]) so a peer that
// stops reading cannot pin a goroutine/fd indefinitely. Safe non-zero
// defaults are a v3 candidate (see V3_BREAKING_PROPOSALS.md).
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

// WithCompression enables permessage-deflate with bounded per-conn
// memory: each message is compressed independently and no LZ77 state
// persists between messages. Suitable as the default whenever
// compression is wanted.
//
// Opt in only when the deployment has validated that the encrypted
// payload mix is not vulnerable to CRIME/BREACH-style oracles.
//
// See [WithCompressionContextTakeover] for the higher-ratio,
// higher-memory variant.
func WithCompression() Option {
	return func(c *config) { c.compression = compressionNoTakeover }
}

// WithCompressionContextTakeover enables permessage-deflate with
// context takeover — the LZ77 sliding window persists between
// messages, yielding better ratios on repetitive payloads at a cost
// of roughly 32 KiB per direction per connection.
//
// Prefer [WithCompression] unless workload measurements show the
// memory cost is acceptable and the compression-ratio improvement is
// material. CRIME/BREACH-style oracle considerations apply here too.
func WithCompressionContextTakeover() Option {
	return func(c *config) { c.compression = compressionContextTakeover }
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

// WithMaxConnections caps the number of concurrent WebSocket
// connections served by this handler. When the cap is reached the
// handler responds to subsequent upgrade requests with
// `503 Service Unavailable` and a `Retry-After: 1` header, and bumps
// the `httpx_websocket_rejected_total{reason="max_connections"}`
// counter. The rejection short-circuits before [coder/websocket]
// would otherwise complete a hijack and per-conn allocation.
//
// Pass zero (the default) to disable the cap. Non-positive values
// other than zero panic so misconfiguration surfaces at startup.
//
// Note: this is a per-process, per-handler cap. Multi-replica
// deployments should multiply by replica count when sizing peak
// browser fan-out; horizontal limits belong at the load balancer.
func WithMaxConnections(n int) Option {
	if n < 0 {
		panic("httpx/websocket: WithMaxConnections requires a non-negative value (zero disables)")
	}
	return func(c *config) { c.maxConnections = int64(n) }
}

// WithPingInterval enables an idle-keepalive heartbeat. Once per
// interval the handler sends a WebSocket Ping control frame; if the
// peer does not respond with a Pong within [WithPongTimeout] the
// connection is closed with [StatusPolicyViolation].
//
// WebSocket has no mandatory heartbeat — browsers do not ping, and
// idle TCP sockets survive until the kernel keepalive (often 2 h)
// kicks in. A server-driven Ping is the only portable way to detect
// half-open connections and reclaim file descriptors. Typical values
// are 30 s for browser-facing services and 5–10 s when peers are
// other backend processes.
//
// Read requirement: the underlying [coder/websocket] Ping does not
// itself read from the connection — it waits for a concurrent read to
// pump the peer's Pong. A handler that reads in a loop (the request/
// response shape) satisfies this automatically. A push-only handler
// that only writes never reads the Pong, so every ping would time out
// and the heartbeat would close the connection with
// [StatusPolicyViolation]; pair [WithReadDrain] with this option for
// push-only handlers.
//
// Pass zero (the default) to disable heartbeats. Non-positive values
// other than zero panic so misconfiguration surfaces at startup.
func WithPingInterval(d time.Duration) Option {
	if d < 0 {
		panic("httpx/websocket: WithPingInterval requires a non-negative duration (zero disables)")
	}
	return func(c *config) { c.pingInterval = d }
}

// WithPongTimeout bounds how long the heartbeat waits for the peer's
// Pong reply before declaring the connection dead and closing with
// [StatusPolicyViolation]. Non-positive values panic.
//
// The pong timeout is only meaningful alongside a heartbeat, so it must
// be paired with [WithPingInterval]: [Handle] panics if a pong timeout
// is configured without a ping interval, since the setting would
// otherwise be silently dropped.
//
// When unset the heartbeat uses the ping interval as the pong
// deadline, which is a safe default for almost all deployments.
func WithPongTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("httpx/websocket: WithPongTimeout requires a positive duration")
	}
	return func(c *config) { c.pongTimeout = d }
}

// WithReadDrain makes the kit drive an internal reader for the
// lifetime of the connection so control frames — Pong replies in
// particular — are pumped even when the application handler never
// reads. It exists for the server-push pattern, where the handler only
// writes and would otherwise be killed by its own [WithPingInterval]
// heartbeat because nothing reads the peer's Pong (see the read
// requirement noted on [WithPingInterval]).
//
// Semantics when enabled (these mirror coder/websocket's CloseRead,
// which backs this option):
//
//   - The handler MUST NOT call [Conn.ReadMessage] or [Conn.ReadJSON];
//     the read side is owned by the kit and a handler read would race
//     the internal reader. Use this only for write-only handlers.
//   - If the peer sends a data message, the connection is closed with
//     [StatusPolicyViolation] — a push-only endpoint does not expect
//     inbound application data.
//   - Peer disconnect cancels the per-connection [Conn.Context]
//     promptly (the kit watches CloseRead's derived context). A push
//     loop should select on ctx.Done() to exit without waiting for a
//     heartbeat failure.
//
// WithReadDrain is independent of [WithPingInterval]: CloseRead still
// pumps control/close frames and detects peer disconnect without a
// heartbeat. Pairing both is recommended for idle dead-peer detection
// under half-open TCP.
func WithReadDrain() Option {
	return func(c *config) { c.readDrain = true }
}

// WithWriteTimeout bounds the duration of a single [Conn.WriteMessage]
// or [Conn.WriteJSON] call. When zero (the default) writes inherit the
// per-connection context with no per-call deadline.
//
// The WebSocket framing protocol cannot resume a partially-sent
// message, so when the deadline expires the underlying
// [coder/websocket] driver closes the connection — i.e. this option
// is a "drop the slow consumer" lever, not a retry-friendly per-write
// deadline. Set it to a value comfortably larger than your largest
// expected payload divided by the slowest realistic peer bandwidth
// (e.g. 5–30 s for human-interactive UIs over 4G).
//
// Non-positive values panic so misconfiguration surfaces at startup.
func WithWriteTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("httpx/websocket: WithWriteTimeout requires a positive duration (omit the option for no timeout)")
	}
	return func(c *config) { c.writeTimeout = d }
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

// WithAnyOriginUnsafe disables the same-origin check on the upgrade
// handshake — every Origin header is accepted. This is the WebSocket
// equivalent of `Access-Control-Allow-Origin: *` and exposes the
// service to cross-site WebSocket hijacking (CSWSH) unless every
// handler independently authenticates the connecting principal (for
// example via a session cookie scoped to the WS endpoint, or a
// short-lived auth token in the first frame).
//
// The "Unsafe" suffix is deliberate and survives audit greps: prefer
// [WithOriginPatterns] with an explicit allow-list whenever the set of
// browser-side origins is known.
func WithAnyOriginUnsafe() Option {
	return func(c *config) {
		c.originPatterns = []string{"*"}
	}
}
