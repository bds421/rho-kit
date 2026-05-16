package centrifuge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
	node       *cfg.Node
	logger     *slog.Logger
	verifier   *jwtutil.Provider
	classifier ChannelClassifier
	metrics    *Metrics

	started atomic.Bool
	stopped atomic.Bool
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

	cnode, err := cfg.New(cfg.Config{
		LogLevel:   toCentrifugeLogLevel(c.logLevel),
		LogHandler: makeLogHandler(logger),
	})
	if err != nil {
		return nil, redact.WrapError("realtime/centrifuge: node construction", err)
	}

	n := &Node{
		node:       cnode,
		logger:     logger,
		verifier:   c.verifier,
		classifier: c.classifier,
		metrics:    c.metrics,
	}

	// Install kit-side callback wiring. Callers may override or
	// extend by calling node.Underlying().OnSubscribe / OnPublish
	// etc., but the kit's connect-auth and metrics observation must
	// be installed first so they run regardless of caller wiring.
	n.installCallbacks()
	return n, nil
}

// Underlying returns the wrapped [centrifuge.Node] for advanced
// users who need access to surfaces the kit does not (yet) expose
// — channel-level authorization callbacks, server-side
// subscriptions, RPC handlers, history/presence APIs.
//
// The kit's connect-auth and metrics callbacks are already
// installed; callers MAY register additional callbacks but MUST
// NOT replace OnConnecting with logic that bypasses the kit's
// auth chain.
func (n *Node) Underlying() *cfg.Node {
	return n.node
}

// Start runs the centrifuge node. Blocks until Stop is called or
// the node exits with an error. Implements
// [runtime/lifecycle.Component].
//
// Safe to call at most once per Node — a second Start returns an
// error rather than racing the centrifuge internals.
func (n *Node) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("realtime/centrifuge: Start requires a non-nil context")
	}
	if !n.started.CompareAndSwap(false, true) {
		return errors.New("realtime/centrifuge: Node.Start already invoked")
	}
	if err := n.node.Run(); err != nil {
		return redact.WrapError("realtime/centrifuge: node run", err)
	}
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
// lifecycle.Runner happy in failure-cleanup paths.
func (n *Node) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("realtime/centrifuge: Stop requires a non-nil context")
	}
	if !n.stopped.CompareAndSwap(false, true) {
		return nil
	}
	if !n.started.Load() {
		// Never ran — nothing to shut down.
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
	return cfg.NewWebsocketHandler(n.node, cfg.WebsocketConfig{})
}

// installCallbacks wires the kit-side OnConnecting handler (JWT
// auth + connect metrics) and the subscribe/publish observation
// hooks for channel-class metrics.
func (n *Node) installCallbacks() {
	n.node.OnConnecting(func(ctx context.Context, e cfg.ConnectEvent) (cfg.ConnectReply, error) {
		if n.verifier == nil {
			n.metrics.observeConnect(connectOutcomeAccepted)
			return cfg.ConnectReply{}, nil
		}
		token := extractBearer(e.Token, e.Data)
		if token == "" {
			n.metrics.observeConnect(connectOutcomeRejected)
			return cfg.ConnectReply{}, cfg.DisconnectInvalidToken
		}
		claims, err := n.verifier.VerifyContext(ctx, token, time.Now())
		if err != nil {
			n.metrics.observeConnect(connectOutcomeRejected)
			n.logger.WarnContext(ctx, "centrifuge: connect token verification failed",
				redact.Error(err),
			)
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
			cb(cfg.SubscribeReply{}, nil)
		})
		client.OnPublish(func(e cfg.PublishEvent, cb cfg.PublishCallback) {
			n.metrics.observePublish(n.classifier(e.Channel))
			cb(cfg.PublishReply{}, nil)
		})
		client.OnDisconnect(func(_ cfg.DisconnectEvent) {
			n.metrics.observeDisconnect(disconnectReasonClean)
		})
	})
}

// extractBearer extracts a bearer token from the centrifuge connect
// event. Centrifuge clients send the token via either the dedicated
// Token field (JWT-style auth) or the freeform Data blob (older
// clients). The kit accepts either with the Token field taking
// precedence.
func extractBearer(token string, data []byte) string {
	if token != "" {
		return strings.TrimPrefix(token, "Bearer ")
	}
	if len(data) == 0 {
		return ""
	}
	// Older centrifuge clients sometimes pass the token in the
	// Data field as a literal string — accept that path too.
	return strings.TrimPrefix(string(data), "Bearer ")
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
// [redact.String]. centrifuge fields are typically strings,
// integers, or short structured shapes; full formatting via fmt
// would pull in extra cost on every log line.
func valueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	default:
		return ""
	}
}
