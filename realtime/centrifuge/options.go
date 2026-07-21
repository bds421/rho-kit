package centrifuge

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// ChannelClassifier maps a centrifuge channel name to a SHORT
// bounded class label used in Prometheus metrics. Operators
// typically classify by prefix:
//
//	func(ch string) string {
//	    switch {
//	    case strings.HasPrefix(ch, "user:"): return "user"
//	    case strings.HasPrefix(ch, "room:"): return "room"
//	    default: return "other"
//	    }
//	}
//
// The classifier MUST return a value from a small bounded set
// (typical: 5–20 distinct strings); the kit validates the returned
// value via [promutil.OpaqueLabelValue] safety net at emit time so
// a misbehaving classifier cannot inflate cardinality, but a
// classifier that returns hundreds of distinct strings will hurt
// Prometheus query performance even if cardinality is technically
// bounded.
type ChannelClassifier func(channel string) string

// defaultChannelClassifier maps every channel to "default" — a
// safe shape that emits a single label value but provides no
// observability per-channel-class. Callers who want richer
// breakdown supply a [WithChannelClassifier] option.
func defaultChannelClassifier(_ string) string { return "default" }

// ChannelAuthEvent carries the identity and target channel for a
// client subscribe or publish authorization decision.
type ChannelAuthEvent struct {
	// ClientID is the centrifuge connection id (opaque, per-connection).
	ClientID string
	// UserID is the authenticated subject from [WithJWTAuth], or empty
	// when the node accepts anonymous connections.
	UserID string
	// Channel is the channel the client wants to subscribe to or publish on.
	Channel string
}

// ChannelAuthorizer decides whether a connected client may subscribe
// to or publish on a channel. Return nil to allow; any non-nil error
// denies the operation (mapped to centrifuge's permission-denied error
// for clients). The authorizer MUST be safe for concurrent use.
//
// Without an authorizer (and without [WithOpenChannelsUnsafe]), the kit
// denies every client subscribe/publish — preserving centrifuge's
// secure-by-default posture while still emitting metrics.
type ChannelAuthorizer func(ctx context.Context, e ChannelAuthEvent) error

// Option configures [NewNode].
type Option func(*config)

type config struct {
	logger              *slog.Logger
	verifier            *jwtutil.Provider
	classifier          ChannelClassifier
	metrics             *Metrics
	registerer          prometheus.Registerer
	logLevel            logLevel
	subscribeAuthorizer ChannelAuthorizer
	publishAuthorizer   ChannelAuthorizer
	openChannelsUnsafe  bool
	anonymousUnsafe     bool
	wsMessageSizeLimit  int
	wsWriteTimeout      time.Duration
}

// logLevel mirrors a subset of centrifuge's LogLevel — the kit
// exposes only the levels operators actually configure (error/info/
// debug) to keep the option surface small.
type logLevel int

const (
	logLevelError logLevel = iota
	logLevelInfo
	logLevelDebug
)

// WithLogger pins the structured logger. Nil panics so a
// miswired-but-configured caller surfaces at startup. Omit the
// option to fall back to [slog.Default].
func WithLogger(logger *slog.Logger) Option {
	if logger == nil {
		panic("realtime/centrifuge: WithLogger requires a non-nil logger (omit the option for slog.Default)")
	}
	return func(c *config) { c.logger = logger }
}

// WithJWTAuth installs a centrifuge `OnConnecting` callback that
// verifies the bearer token sent by the centrifuge client via the
// supplied kit [jwtutil.Provider] and propagates the verified
// subject as the centrifuge connection's user ID.
//
// Per-channel authorization is separate: pass [WithSubscribeAuthorizer]
// / [WithPublishAuthorizer] (or deliberately [WithOpenChannelsUnsafe]).
// WithJWTAuth only handles connection-level identity, not channel-level
// authz. Do NOT replace OnConnect/OnSubscribe/OnPublish via
// [Node.Underlying] — that drops kit metrics and authz wiring; use the
// kit authorizer options instead.
//
// Passing nil panics so an "auth enabled but unwired" miswire
// surfaces at startup rather than degrading into an anonymous
// connection.
func WithJWTAuth(p *jwtutil.Provider) Option {
	if p == nil {
		panic("realtime/centrifuge: WithJWTAuth requires a non-nil jwtutil.Provider (omit the option for an unauthenticated node)")
	}
	return func(c *config) { c.verifier = p }
}

// WithAnonymousConnectionsUnsafe allows connections when no [WithJWTAuth]
// verifier is configured. The name is intentionally grep-able (mirrors
// httpx/websocket.WithAnyOriginUnsafe). Without this option or WithJWTAuth,
// [NewNode] panics so a bare node is never an open pub/sub bus by accident.
func WithAnonymousConnectionsUnsafe() Option {
	return func(c *config) { c.anonymousUnsafe = true }
}

// WithWebsocketMessageSizeLimit sets centrifuge WebsocketConfig.MessageSizeLimit
// used by [Node.WebsocketHandler]. Non-positive values panic.
func WithWebsocketMessageSizeLimit(n int) Option {
	if n <= 0 {
		panic("realtime/centrifuge: WithWebsocketMessageSizeLimit requires n > 0")
	}
	return func(c *config) { c.wsMessageSizeLimit = n }
}

// WithWebsocketWriteTimeout sets centrifuge WebsocketConfig.WriteTimeout
// used by [Node.WebsocketHandler]. Non-positive values panic.
func WithWebsocketWriteTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("realtime/centrifuge: WithWebsocketWriteTimeout requires d > 0")
	}
	return func(c *config) { c.wsWriteTimeout = d }
}

// WithSubscribeAuthorizer installs the channel-subscribe authorization
// callback. When set, subscribe is allowed only when the authorizer
// returns nil. When neither this nor [WithOpenChannelsUnsafe] is set,
// every client subscribe is denied (fail-closed).
//
// Passing nil panics so a miswire surfaces at startup.
func WithSubscribeAuthorizer(fn ChannelAuthorizer) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithSubscribeAuthorizer requires a non-nil authorizer (omit the option for default-deny, or use WithOpenChannelsUnsafe)")
	}
	return func(c *config) { c.subscribeAuthorizer = fn }
}

// WithPublishAuthorizer installs the channel-publish authorization
// callback. Same fail-closed defaults as [WithSubscribeAuthorizer].
//
// Passing nil panics so a miswire surfaces at startup.
func WithPublishAuthorizer(fn ChannelAuthorizer) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithPublishAuthorizer requires a non-nil authorizer (omit the option for default-deny, or use WithOpenChannelsUnsafe)")
	}
	return func(c *config) { c.publishAuthorizer = fn }
}

// WithOpenChannelsUnsafe allows every connected client to subscribe
// to and publish on every channel, bypassing authorizer checks.
// The name is intentionally grep-able (mirrors
// httpx/websocket.WithAnyOriginUnsafe). Prefer explicit
// [WithSubscribeAuthorizer] / [WithPublishAuthorizer] in production.
//
// Metrics for subscribe/publish still fire so open-channel demos keep
// observability.
func WithOpenChannelsUnsafe() Option {
	return func(c *config) { c.openChannelsUnsafe = true }
}

// WithChannelClassifier installs the channel-class mapping function
// used for bounded-cardinality Prometheus labels. See
// [ChannelClassifier] for the contract.
//
// Passing nil panics so misconfiguration surfaces at startup. Omit
// the option to fall back to a single-label classifier that maps
// every channel to "default".
func WithChannelClassifier(fn ChannelClassifier) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithChannelClassifier requires a non-nil classifier (omit the option for the default no-op classifier)")
	}
	return func(c *config) { c.classifier = fn }
}

// WithMetricsRegisterer pins the Prometheus registerer the node
// will thread through to its kit-side metric set. Defaults to
// [prometheus.DefaultRegisterer]. Passing nil panics so a
// miswired-but-configured caller surfaces at startup.
//
// Naming: per the root AGENTS.md "Metrics" convention,
// WithMetricsRegisterer is the OUTER module-Option spelling
// (`func(...) Option`); WithRegisterer (in metrics.go) is the INNER
// MetricsOption spelling. Two earlier versions of this package had
// the names swapped; the convention is now consistent.
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	if reg == nil {
		panic("realtime/centrifuge: WithMetricsRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *config) { c.registerer = reg }
}

// WithLogLevelDebug raises the centrifuge node's log verbosity to
// debug. Default is info-level so the kit-side warn/error/info
// emit but high-volume frame traces stay off.
func WithLogLevelDebug() Option {
	return func(c *config) { c.logLevel = logLevelDebug }
}

// WithLogLevelError lowers the centrifuge node's log verbosity to
// error-only — useful in load-test environments where info-level
// noise drowns relevant signal.
func WithLogLevelError() Option {
	return func(c *config) { c.logLevel = logLevelError }
}
