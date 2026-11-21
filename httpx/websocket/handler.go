package websocket

import (
	"context"
	"log/slog"
	"net/http"
	"path"

	coderws "github.com/coder/websocket"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// defaultHub tracks connections opened via package-level [Handle] so
// [Shutdown] can drain them on process stop without forcing callers to
// construct a [Hub]. Isolated servers that must not share the global
// set still use [NewHub].
var defaultHub = &Hub{conns: make(map[*Conn]struct{})}

// Handle constructs an [http.HandlerFunc] that upgrades the request to
// a WebSocket connection and dispatches to the configured handler.
//
// The returned handler is a plain stdlib http.HandlerFunc, so it
// composes with the existing httpx middleware stack — auth, rate
// limiting, request IDs, panic recovery — exactly like any other
// handler.
//
// Handle panics if no [WithHandler] option is supplied. Serving a
// WebSocket endpoint with no handler is always a wiring bug rather
// than a runtime condition to absorb.
//
// Connections opened through Handle are registered on a package-level
// registry. Call [Shutdown] from lifecycle/app stop to cancel them and
// send StatusGoingAway. For multi-server isolation (tests, multiple
// independent listeners that must not share drain), use [NewHub]
// instead — Hub registries are independent of package [Shutdown].
func Handle(opts ...Option) http.HandlerFunc {
	return handleWithHooks(opts, defaultHub.track, defaultHub.untrack)
}

// Shutdown cancels every connection opened via package-level [Handle]
// and closes those sockets with StatusGoingAway. It returns when all
// such handlers have untracked, or when ctx is done.
//
// Wire this from lifecycle/app stop (alongside http.Server.Shutdown).
// Connections created through a [Hub] are not affected — drain those
// with [Hub.Shutdown].
func Shutdown(ctx context.Context) error {
	return defaultHub.Shutdown(ctx)
}

// buildConfig applies options and validates the resulting config.
// Shared by [Handle] and [NewHub] so both fail fast identically.
func buildConfig(opts ...Option) config {
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt == nil {
			panic("httpx/websocket: Handle option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.handler == nil {
		panic("httpx/websocket: Handle requires WithHandler")
	}
	// WithPongTimeout only takes effect when a heartbeat is running
	// (pingInterval > 0; default is DefaultPingInterval unless
	// WithNoHeartbeat / WithPingInterval(0)). Configuring a pong timeout
	// without a heartbeat is therefore inert — reject it at startup.
	if cfg.pongTimeout > 0 && cfg.pingInterval <= 0 {
		panic("httpx/websocket: WithPongTimeout requires a non-zero ping interval (the pong timeout is inert without a heartbeat)")
	}
	// WithReadDrain is independent of the heartbeat, but historically
	// operators paired them; keep the option always honoured (no silent drop).
	// Invalid origin globs must fail at registration, not as per-request 403s.
	for _, pat := range cfg.originPatterns {
		if _, err := path.Match(pat, ""); err != nil {
			panic("httpx/websocket: WithOriginPatterns: invalid pattern " + pat + ": " + err.Error())
		}
	}
	return cfg
}

// handleWithHooks is the shared upgrade path for [Handle] and [Hub.Handler].
// onOpen/onClose are optional lifecycle hooks (Hub uses them to track conns).
func handleWithHooks(opts []Option, onOpen, onClose func(*Conn)) http.HandlerFunc {
	cfg := buildConfig(opts...)
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}

	acceptOpts := &coderws.AcceptOptions{
		Subprotocols:   append([]string(nil), cfg.subprotocols...),
		OriginPatterns: append([]string(nil), cfg.originPatterns...),
	}
	switch cfg.compression {
	case compressionNoTakeover:
		acceptOpts.CompressionMode = coderws.CompressionNoContextTakeover
	case compressionContextTakeover:
		acceptOpts.CompressionMode = coderws.CompressionContextTakeover
	}

	u := &upgrader{
		cfg:        cfg,
		logger:     logger,
		acceptOpts: acceptOpts,
		limiter:    newConnLimiter(cfg.maxConnections),
		onOpen:     onOpen,
		onClose:    onClose,
	}
	return u.serve
}

// upgrader is the configured upgrade path of one handler.
type upgrader struct {
	cfg             config
	logger          *slog.Logger
	acceptOpts      *coderws.AcceptOptions
	limiter         *connLimiter
	onOpen, onClose func(*Conn)
}

func (u *upgrader) serve(w http.ResponseWriter, r *http.Request) {
	// Reject before Accept so a saturated server does not waste
	// the per-conn allocation just to immediately close. The
	// limiter is a no-op when no cap is configured.
	if !u.limiter.tryAcquire() {
		u.cfg.metrics.observeRejected(rejectReasonMaxConnections)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "websocket: server at capacity", http.StatusServiceUnavailable)
		return
	}
	defer u.limiter.release()
	hold, ok := acquireKeys(u.cfg.keyLimits, r)
	if !ok {
		u.cfg.metrics.observeRejected(rejectReasonMaxConnectionsPerKey)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "websocket: too many connections for this client", http.StatusTooManyRequests)
		return
	}
	defer hold.release()

	raw, err := coderws.Accept(w, r, u.acceptOpts)
	if err != nil {
		// coder/websocket has already written an HTTP error
		// response by this point; we only need to surface the
		// failure in logs without leaking driver text.
		u.logger.WarnContext(r.Context(), "websocket: upgrade failed",
			redact.Error(err),
		)
		return
	}
	raw.SetReadLimit(u.cfg.maxMessageSize)

	// Derive a per-connection context that closes when the
	// request body closes or when the connection is torn down,
	// whichever comes first. Use [context.WithoutCancel] on the
	// request context so the connection lifetime is not bounded
	// by the upgrade handler's stdlib timeout (the handshake is
	// done — further reads happen on the hijacked TCP connection).
	//
	// Graceful process shutdown is not linked through the request
	// context: call package [Shutdown] (for [Handle]) or
	// [Hub.Shutdown] (for isolated hubs) to cancel open conns.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()

	conn := &Conn{
		inner:        raw,
		ctx:          ctx,
		cancel:       cancel,
		logger:       u.logger,
		metrics:      u.cfg.metrics,
		writeTimeout: u.cfg.writeTimeout,
	}
	u.cfg.metrics.connOpened()
	if u.onOpen != nil {
		u.onOpen(conn)
	}
	defer func() {
		if u.onClose != nil {
			u.onClose(conn)
		}
	}()
	u.watch(ctx, raw, conn)

	closeCode, closeReason := u.runHandler(ctx, conn)
	// Delegate to Conn.Close so cancel-before-handshake ordering is
	// preserved (heartbeat re-check classifies teardown as graceful)
	// and the close path cannot drift from the exported API.
	_ = conn.Close(StatusCode(closeCode), closeReason)
}

// watch starts the read drain and the heartbeat the configuration asks for.
func (u *upgrader) watch(ctx context.Context, raw *coderws.Conn, conn *Conn) {
	// WithReadDrain: own the read side so push-only handlers that
	// never Read still detect peer disconnect. CloseRead is
	// independent of the heartbeat; it is not silently dropped when
	// WithPingInterval is unset.
	//
	// coder/websocket's CloseRead closes the raw socket when its input
	// context is cancelled. Do not bind that input to conn.ctx: Conn.Close
	// intentionally cancels conn.ctx before sending its close frame, and a
	// bound CloseRead could otherwise win that race and turn graceful 1001
	// shutdown into an abnormal EOF. The drain still exits when the raw
	// connection closes; its derived context then cancels conn.ctx for peer
	// disconnects.
	if u.cfg.readDrain {
		drainCtx := raw.CloseRead(context.WithoutCancel(ctx))
		go func() {
			<-drainCtx.Done()
			// Cancel promptly so push handlers parked on
			// conn.Context().Done() exit without waiting for
			// the next heartbeat failure.
			conn.cancelCtx()
		}()
	}
	// Idle keepalive — default on (DefaultPingInterval); opt out via
	// WithNoHeartbeat. Closes the connection if the peer stops
	// responding to pings, unblocking in-flight reads/writes.
	if u.cfg.pingInterval > 0 {
		go runHeartbeat(ctx, conn, u.cfg.pingInterval, u.cfg.pongTimeout, u.logger, u.cfg.metrics)
	}
}

// runHandler runs the user handler and returns the close frame to send.
// A panic is recovered so a misbehaving callback cannot crash the entire
// HTTP server; it closes with [StatusInternalError] and a non-leaking
// reason.
func (u *upgrader) runHandler(ctx context.Context, conn *Conn) (closeCode coderws.StatusCode, closeReason string) {
	closeCode = coderws.StatusNormalClosure
	defer func() {
		if rv := recover(); rv != nil {
			closeCode = coderws.StatusInternalError
			closeReason = "internal error"
			u.logger.ErrorContext(ctx, "websocket: handler panicked",
				redact.Panic(rv),
			)
		}
	}()
	err := u.cfg.handler(ctx, conn)
	if err == nil {
		return closeCode, ""
	}
	// A handler that surfaces the read error on a routine peer
	// disconnect (the natural read-loop pattern) must not have that
	// normal close escalated into a StatusInternalError / WARN. Treat a
	// normal (1000) or going-away (1001) close as the graceful shutdown
	// it is; everything else is a genuine handler error.
	if IsNormalClosure(err) {
		return coderws.CloseStatus(err), ""
	}
	u.logger.WarnContext(ctx, "websocket: handler returned error",
		redact.Error(err),
	)
	return coderws.StatusInternalError, "handler error"
}
