package centrifuge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cfg "github.com/centrifugal/centrifuge"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// Node is the kit-grade centrifuge wrapper. It implements
// [github.com/bds421/rho-kit/runtime/v2/lifecycle.Component] via
// [Node.Start] / [Node.Stop] so it can be wired into the kit's
// app.Builder / lifecycle.Runner alongside the HTTP server.
//
// Concurrency: every public method is safe for concurrent use.
// [Node.Start] may be called at most once; a re-entry returns an
// error rather than racing the centrifuge node's internal state.
type Node struct {
	node                *cfg.Node
	logger              *slog.Logger
	verifier            *jwtutil.Provider
	classifier          ChannelClassifier
	metrics             *Metrics
	subscribeAuthorizer ChannelAuthorizer
	publishAuthorizer   ChannelAuthorizer
	openChannelsUnsafe  bool
	anonymousUnsafe     bool
	wsMessageSizeLimit  int
	wsWriteTimeout      time.Duration

	// lifecycleMu serialises Start/Stop so check-then-act across the
	// started/stopped atomics cannot leak a running node that Stop
	// will never shut down.
	lifecycleMu sync.Mutex
	started     atomic.Bool
	stopped     atomic.Bool
}

// NewNode constructs a kit-wrapped centrifuge node. Options are
// applied in order; later options override earlier ones for scalar
// fields. Returns a non-nil Node on success; on construction error
// (typically caused by centrifuge's internal config validation)
// returns nil + redacted error.
//
// Construction does NOT start the node — call [Node.Start] (or wire
// the Node into a lifecycle.Runner) to begin accepting connections.
func NewNode(opts ...Option) (*Node, error) {
	c := config{
		classifier: defaultChannelClassifier,
		logLevel:   logLevelInfo,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("realtime/centrifuge: NewNode option must not be nil")
		}
		opt(&c)
	}
	logger := c.logger
	if logger == nil {
		logger = slog.Default()
	}
	if c.verifier == nil && !c.anonymousUnsafe {
		return nil, errors.New("realtime/centrifuge: NewNode requires WithJWTAuth or explicit WithAnonymousConnectionsUnsafe (bare nodes are not open pub/sub buses)")
	}

	cnode, err := cfg.New(cfg.Config{
		LogLevel:   toCentrifugeLogLevel(c.logLevel),
		LogHandler: makeLogHandler(logger),
	})
	if err != nil {
		return nil, redact.WrapError("realtime/centrifuge: node construction", err)
	}

	// Metrics are opt-in: building the kit-side metric set only when a
	// caller supplied a registerer via WithMetricsRegisterer mirrors
	// infra/redis.WithMetricsRegisterer. When metrics stays nil, the
	// observe* calls early-return so emission is a no-op.
	metrics := c.metrics
	if metrics == nil && c.registerer != nil {
		metrics = NewMetrics(WithRegisterer(c.registerer))
	}

	n := &Node{
		node:                cnode,
		logger:              logger,
		verifier:            c.verifier,
		classifier:          c.classifier,
		metrics:             metrics,
		subscribeAuthorizer: c.subscribeAuthorizer,
		publishAuthorizer:   c.publishAuthorizer,
		openChannelsUnsafe:  c.openChannelsUnsafe,
		anonymousUnsafe:     c.anonymousUnsafe,
		wsMessageSizeLimit:  c.wsMessageSizeLimit,
		wsWriteTimeout:      c.wsWriteTimeout,
	}

	// Install kit-side callback wiring (connect auth, channel authz,
	// metrics). Channel-level authz is configured via
	// WithSubscribeAuthorizer / WithPublishAuthorizer /
	// WithOpenChannelsUnsafe — not by replacing handlers on the
	// underlying node (centrifuge.Node has no OnSubscribe/OnPublish;
	// those are per-client hooks installed here in OnConnect).
	n.installCallbacks()
	return n, nil
}

// Underlying returns the wrapped [centrifuge.Node] for advanced
// users who need access to surfaces the kit does not (yet) expose
// — server-side subscriptions, RPC handlers, history/presence APIs.
//
// Channel-level authorization MUST be configured via
// [WithSubscribeAuthorizer] / [WithPublishAuthorizer] (or deliberately
// [WithOpenChannelsUnsafe]). Do not call client.OnSubscribe /
// client.OnPublish yourself: those hooks are installed per connection
// inside the kit's OnConnect, and replacing OnConnect wholesale drops
// kit metrics and authz. Callers MAY register additional node-level
// callbacks (OnSurvey, …) but MUST NOT replace OnConnecting with
// logic that bypasses the kit's auth chain.
func (n *Node) Underlying() *cfg.Node {
	return n.node
}

// Start runs the centrifuge node. It starts the node's internal
// goroutines and then blocks until the passed-in ctx is cancelled,
// satisfying the [runtime/lifecycle.Component] "Start blocks" contract.
//
// Cancellation contract: Start returns ONLY when ctx is cancelled.
// [Node.Stop] shuts the centrifuge node down but does NOT unblock a
// concurrent Start — callers MUST cancel the context they passed to
// Start (lifecycle.Runner does this automatically on shutdown) or the
// Start goroutine leaks. Stop alone is not sufficient to return Start.
//
// Safe to call at most once per Node — a second Start returns an
// error rather than racing the centrifuge internals.
func (n *Node) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("realtime/centrifuge: Start requires a non-nil context")
	}
	n.lifecycleMu.Lock()
	if n.stopped.Load() {
		n.lifecycleMu.Unlock()
		return errors.New("realtime/centrifuge: Node.Start after Stop")
	}
	if n.started.Load() {
		n.lifecycleMu.Unlock()
		return errors.New("realtime/centrifuge: Node.Start already invoked")
	}
	if err := n.node.Run(); err != nil {
		n.lifecycleMu.Unlock()
		return redact.WrapError("realtime/centrifuge: node run", err)
	}
	// Mark started only after Run succeeds so a failed Start can be
	// retried and Stop's never-ran guard stays accurate.
	n.started.Store(true)
	n.lifecycleMu.Unlock()
	// Block until ctx cancels — Run() returns immediately after
	// starting internal goroutines; lifecycle.Component contract
	// requires Start to block.
	<-ctx.Done()
	return nil
}

// Stop gracefully shuts down the centrifuge node. Implements
// [runtime/lifecycle.Component].
//
// Idempotent: a second Stop is a no-op. Stop is a safe no-op when
// Start was never reached too — centrifuge's Shutdown nil-derefs an
// unstarted node, so the kit guards against that to keep the
// lifecycle.Runner happy in failure-cleanup paths. A Stop that races
// a concurrent Start waits for Start to finish transitioning so a
// successfully started node is always shut down.
func (n *Node) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("realtime/centrifuge: Stop requires a non-nil context")
	}
	n.lifecycleMu.Lock()
	defer n.lifecycleMu.Unlock()
	if n.stopped.Load() {
		return nil
	}
	n.stopped.Store(true)
	if !n.started.Load() {
		// Never ran — nothing to shut down. stopped is set so a later
		// Start is refused rather than leaving a node with no
		// shutdown path.
		return nil
	}
	if err := n.node.Shutdown(ctx); err != nil {
		return redact.WrapError("realtime/centrifuge: node shutdown", err)
	}
	return nil
}

// WebsocketHandler returns an http.Handler that upgrades incoming
// connections via centrifuge's WebSocket protocol. Mount under
// whatever path your centrifuge client expects (typical:
// `/connection/websocket`).
//
// The handler composes naturally with the kit's httpx middleware
// stack — wrap with auth, rate limiting, request IDs, panic
// recovery exactly like any other http.Handler.
func (n *Node) WebsocketHandler() http.Handler {
	wscfg := cfg.WebsocketConfig{}
	if n.wsMessageSizeLimit > 0 {
		wscfg.MessageSizeLimit = n.wsMessageSizeLimit
	}
	if n.wsWriteTimeout > 0 {
		wscfg.WriteTimeout = n.wsWriteTimeout
	}
	return cfg.NewWebsocketHandler(n.node, wscfg)
}

// installCallbacks wires the kit-side OnConnecting handler (JWT
// auth + connect metrics) and per-client subscribe/publish handlers
// that enforce channel authorization (fail-closed by default) while
// still emitting channel-class metrics.
func (n *Node) installCallbacks() {
	n.node.OnConnecting(func(ctx context.Context, e cfg.ConnectEvent) (cfg.ConnectReply, error) {
		if n.verifier == nil {
			// Only reachable with WithAnonymousConnectionsUnsafe (NewNode
			// refuses bare nodes). Count as accepted: the operator opted in.
			n.metrics.observeConnect(connectOutcomeAccepted)
			return cfg.ConnectReply{
				Credentials: &cfg.Credentials{UserID: ""},
			}, nil
		}
		token := extractBearer(e.Token)
		if token == "" {
			n.metrics.observeConnect(connectOutcomeRejected)
			return cfg.ConnectReply{}, cfg.DisconnectInvalidToken
		}
		claims, err := n.verifier.VerifyContext(ctx, token, time.Now())
		if err != nil {
			disc, outcome := connectVerifyFailure(err)
			n.metrics.observeConnect(outcome)
			if outcome == connectOutcomeError {
				n.logger.ErrorContext(ctx, "centrifuge: connect token verification failed (key set unavailable)",
					redact.Error(err),
				)
			} else {
				n.logger.WarnContext(ctx, "centrifuge: connect token verification failed",
					redact.Error(err),
				)
			}
			return cfg.ConnectReply{}, disc
		}
		if claims.Subject == "" {
			n.metrics.observeConnect(connectOutcomeRejected)
			n.logger.WarnContext(ctx, "centrifuge: connect token missing subject")
			return cfg.ConnectReply{}, cfg.DisconnectInvalidToken
		}
		n.metrics.observeConnect(connectOutcomeAccepted)
		return cfg.ConnectReply{
			Credentials: &cfg.Credentials{
				UserID:   claims.Subject,
				ExpireAt: claims.ExpiresAt,
			},
		}, nil
	})

	n.node.OnConnect(func(client *cfg.Client) {
		client.OnSubscribe(func(e cfg.SubscribeEvent, cb cfg.SubscribeCallback) {
			n.metrics.observeSubscribe(n.classifier(e.Channel))
			if err := n.authorizeChannel(client, e.Channel, n.subscribeAuthorizer); err != nil {
				cb(cfg.SubscribeReply{}, err)
				return
			}
			cb(cfg.SubscribeReply{}, nil)
		})
		client.OnPublish(func(e cfg.PublishEvent, cb cfg.PublishCallback) {
			n.metrics.observePublish(n.classifier(e.Channel))
			if err := n.authorizeChannel(client, e.Channel, n.publishAuthorizer); err != nil {
				cb(cfg.PublishReply{}, err)
				return
			}
			cb(cfg.PublishReply{}, nil)
		})
		client.OnDisconnect(func(e cfg.DisconnectEvent) {
			n.metrics.observeDisconnect(disconnectReason(e.Disconnect))
		})
	})
}

// authorizeChannel applies fail-closed channel authorization.
// Order: open-channels opt-in → explicit authorizer → default deny.
func (n *Node) authorizeChannel(client *cfg.Client, channel string, authorizer ChannelAuthorizer) error {
	if n.openChannelsUnsafe {
		return nil
	}
	if authorizer == nil {
		return cfg.ErrorPermissionDenied
	}
	ctx := context.Background()
	if client != nil {
		if cctx := client.Context(); cctx != nil {
			ctx = cctx
		}
	}
	ev := ChannelAuthEvent{Channel: channel}
	if client != nil {
		ev.ClientID = client.ID()
		ev.UserID = client.UserID()
	}
	if err := authorizer(ctx, ev); err != nil {
		// Map caller errors to permission-denied so clients never see
		// raw internal error text; log the detail for operators.
		n.logger.WarnContext(ctx, "centrifuge: channel authorization denied",
			redact.String("channel", channel),
			redact.String("user_id", ev.UserID),
			redact.Error(err),
		)
		return cfg.ErrorPermissionDenied
	}
	return nil
}

// extractBearer extracts a bearer token from the centrifuge connect
// event. Centrifuge clients send the token via either the dedicated
// Token field (JWT-style auth) or the freeform Data blob (older
// clients). The kit accepts either with the Token field taking
// precedence.

// connectVerifyFailure maps a verifier error to the centrifuge disconnect
// advice and the connects_total outcome label. Infrastructure failures
// (JWKS not ready / stale) are temporary; token failures are terminal.
func connectVerifyFailure(err error) (disconnect cfg.Disconnect, outcome string) {
	if errors.Is(err, jwtutil.ErrKeySetUnavailable) {
		return cfg.DisconnectServerError, connectOutcomeError
	}
	return cfg.DisconnectInvalidToken, connectOutcomeRejected
}

func extractBearer(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	// Accept an optional "Bearer " prefix on the dedicated Token field only.
	// Connect Data is an application payload and is never treated as a token.
	const prefix = "Bearer "
	if len(token) > len(prefix) && strings.EqualFold(token[:len(prefix)], prefix) {
		return strings.TrimSpace(token[len(prefix):])
	}
	return token
}

// disconnectReason classifies a centrifuge disconnect for the
// kit's disconnects_total metric. centrifuge sets the event's
// Disconnect to [centrifuge.DisconnectConnectionClosed] (code 3000)
// when the close was NOT server-initiated — i.e. the client closed
// the connection cleanly. Any other code means the server tore the
// connection down (shutdown, expired credentials, slow consumer,
// kicked, etc.), which the kit reports as "stale".
func disconnectReason(d cfg.Disconnect) string {
	if d.Code == cfg.DisconnectConnectionClosed.Code {
		return disconnectReasonClean
	}
	return disconnectReasonStale
}

// toCentrifugeLogLevel maps the kit's bounded log-level enum to
// centrifuge's. Centrifuge supports Trace/Debug/Info/Warn/Error;
// the kit only exposes Error/Info/Debug because the others rarely
// match operator intent.
func toCentrifugeLogLevel(l logLevel) cfg.LogLevel {
	switch l {
	case logLevelError:
		return cfg.LogLevelError
	case logLevelDebug:
		return cfg.LogLevelDebug
	default:
		return cfg.LogLevelInfo
	}
}

// makeLogHandler bridges centrifuge log lines into the kit's
// [*slog.Logger]. centrifuge emits structured records with
// arbitrary key/value fields; the kit copies them through
// [redact.String] so PII in log fields does not bypass the kit's
// redaction conventions.
func makeLogHandler(logger *slog.Logger) cfg.LogHandler {
	return func(entry cfg.LogEntry) {
		attrs := make([]any, 0, 2*len(entry.Fields))
		for k, v := range entry.Fields {
			attrs = append(attrs, redact.String(k, valueToString(v)))
		}
		switch entry.Level {
		case cfg.LogLevelError:
			logger.Error(entry.Message, attrs...)
		case cfg.LogLevelWarn:
			logger.Warn(entry.Message, attrs...)
		case cfg.LogLevelInfo:
			logger.Info(entry.Message, attrs...)
		case cfg.LogLevelDebug, cfg.LogLevelTrace:
			logger.Debug(entry.Message, attrs...)
		default:
			logger.Info(entry.Message, attrs...)
		}
	}
}

// valueToString renders an arbitrary log-field value for
// [redact.String]. centrifuge LogEntry.Fields commonly carry
// scalars (client/user counts, durations, flags) alongside strings,
// so the common scalar types are handled explicitly and anything
// else falls back to [fmt.Sprint] rather than being dropped to the
// empty string — silently discarding diagnostic context in bridged
// log lines.
func valueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}
