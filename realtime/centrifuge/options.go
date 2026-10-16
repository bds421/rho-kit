package centrifuge

import (
	"context"
	"log/slog"
	"time"

	cfg "github.com/centrifugal/centrifuge"
	"github.com/centrifugal/protocol"
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

// VerifiedConnectPolicy runs after mandatory JWT verification. Claims are a
// value copy; the authenticated subject cannot be replaced by the policy.
// Derive Reply.Context from ctx to preserve cancellation and request metadata.
// All policy callbacks must be safe for concurrent use.
type VerifiedConnectPolicy func(ctx context.Context, event cfg.ConnectEvent, claims jwtutil.Claims) (ConnectionPolicyReply, error)

// VerifiedRefreshPolicy checks current application/session authority after
// mandatory verification of a fresh token for the same authenticated subject.
// Refresh uses the client-side SDK flow, not an unverified server renewal.
type VerifiedRefreshPolicy func(ctx context.Context, client Connection, event cfg.RefreshEvent, claims jwtutil.Claims) (ConnectionPolicyReply, error)

// ConnectionPolicyReply can add server-owned context/info and tighten expiry.
// ExpireAt zero preserves JWT expiry; a nonzero expiry may not extend it.
// Context is used only on connect. Info may be exposed by presence features,
// so do not place credentials or private session data there.
type ConnectionPolicyReply struct {
	Context  context.Context
	Info     []byte
	ExpireAt int64
}

// Option configures [NewNode].
type Option func(*config)

type config struct {
	logger                    *slog.Logger
	verifier                  *jwtutil.Provider
	classifier                ChannelClassifier
	metrics                   *Metrics
	registerer                prometheus.Registerer
	logLevel                  logLevel
	subscribeAuthorizer       ChannelAuthorizer
	publishAuthorizer         ChannelAuthorizer
	openChannelsUnsafe        bool
	anonymousUnsafe           bool
	wsMessageSizeLimit        int
	wsWriteTimeout            time.Duration
	connectPolicy             VerifiedConnectPolicy
	refreshPolicy             VerifiedRefreshPolicy
	connectObserver           func(Connection)
	disconnectObserver        func(Connection, cfg.DisconnectEvent)
	subscriptionReadyObserver func(Connection, string)
	publicationWritePolicy    func(Connection, PublicationWriteEvent) bool
	clientQueueMaxSize        int
	clientChannelLimit        int
	maxMessagesInFrame        int
}

// WithSubscriptionReadyObserver observes a successful native subscription after
// its callback completed and the channel is installed. This is not proof that a
// browser observed the reply. The observer must return promptly and respect the
// connection context; it must not replace authentication or channel handlers.
func WithSubscriptionReadyObserver(fn func(Connection, string)) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithSubscriptionReadyObserver requires a non-nil observer")
	}
	return func(c *config) { c.subscriptionReadyObserver = fn }
}

// PublicationWriteEvent describes one native publication about to be written.
// Data is one bare encoded protocol.Reply, or protocol.Push when Unidirectional
// is true. It has no WebSocket framing or protobuf length prefix. Protocol and
// Unidirectional come from the native transport, never from inspecting Data.
//
// Data is borrowed and READ-ONLY for the synchronous policy call only. Do not
// mutate it, retain it or hand it to another goroutine; clone it for an owned
// lifetime. Native encodings may be shared across clients. This is a caller
// contract, not immutability enforced by Go. Other fields are value snapshots.
type PublicationWriteEvent struct {
	Channel        string
	Data           []byte
	FrameType      protocol.FrameType
	Protocol       cfg.ProtocolType
	Unidirectional bool
}

// WithPublicationWritePolicy runs immediately before the native transport writer
// for publication frames only, while the channel remains subscribed. A false
// result permanently refuses this connection's publications, including remaining
// close-flush items, and asynchronously disconnects it without a queue flush.
// Enabling it defaults MaxMessagesInFrame to one and rejects a larger value.
// Callbacks must be bounded/concurrency-safe, follow PublicationWriteEvent's byte
// lifetime contract, and must not synchronously Stop or Disconnect.
// This gate is not a network completion or browser-observation guarantee.
func WithPublicationWritePolicy(fn func(Connection, PublicationWriteEvent) bool) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithPublicationWritePolicy requires a non-nil policy")
	}
	return func(c *config) { c.publicationWritePolicy = fn }
}

// WithClientQueueMaxSize selects the upstream maximum queued bytes per client.
// Omission preserves the native default. This does not bound queued frame count.
func WithClientQueueMaxSize(n int) Option {
	if n <= 0 {
		panic("realtime/centrifuge: WithClientQueueMaxSize requires n > 0")
	}
	return func(c *config) { c.clientQueueMaxSize = n }
}

// WithClientChannelLimit selects the upstream client-side subscription limit.
func WithClientChannelLimit(n int) Option {
	if n <= 0 {
		panic("realtime/centrifuge: WithClientChannelLimit requires n > 0")
	}
	return func(c *config) { c.clientChannelLimit = n }
}

// WithMaxMessagesInFrame selects the native writer's message aggregation bound.
// A value of one disables aggregation; it is not a queued-message count limit.
func WithMaxMessagesInFrame(n int) Option {
	if n <= 0 {
		panic("realtime/centrifuge: WithMaxMessagesInFrame requires n > 0")
	}
	return func(c *config) { c.maxMessagesInFrame = n }
}

// WithVerifiedConnectPolicy adds application binding policy without replacing
// mandatory JWT identity verification or the kit's authorization/metrics wiring.
// Requires WithJWTAuth; an anonymous node cannot use this option.
func WithVerifiedConnectPolicy(fn VerifiedConnectPolicy) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithVerifiedConnectPolicy requires a non-nil policy")
	}
	return func(c *config) { c.connectPolicy = fn }
}

// WithVerifiedRefreshPolicy enables SDK client-side token refresh. Every refresh
// re-verifies JWT and subject before invoking current session/resource policy.
// Requires WithJWTAuth. Omission preserves the existing expiration behavior.
func WithVerifiedRefreshPolicy(fn VerifiedRefreshPolicy) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithVerifiedRefreshPolicy requires a non-nil policy")
	}
	return func(c *config) { c.refreshPolicy = fn }
}

// WithConnectObserver observes accepted clients after kit handlers are installed.
// Connection exposes client control but not callback replacement methods.
func WithConnectObserver(fn func(Connection)) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithConnectObserver requires a non-nil observer")
	}
	return func(c *config) { c.connectObserver = fn }
}

// WithDisconnectObserver adds lifecycle cleanup after kit disconnect metrics.
func WithDisconnectObserver(fn func(Connection, cfg.DisconnectEvent)) Option {
	if fn == nil {
		panic("realtime/centrifuge: WithDisconnectObserver requires a non-nil observer")
	}
	return func(c *config) { c.disconnectObserver = fn }
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
