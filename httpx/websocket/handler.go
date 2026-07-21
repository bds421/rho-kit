package websocket

import (
	"context"
	"log/slog"
	"net/http"
	"path"

	coderws "github.com/coder/websocket"

	"github.com/bds421/rho-kit/core/v2/redact"
)

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
// Handle does not participate in process-level graceful shutdown of
// open sockets — use [NewHub] + [Hub.Shutdown] when connection
// contexts must cancel on service stop (review-09).
func Handle(opts ...Option) http.HandlerFunc {
	return handleWithHooks(opts, nil, nil)
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

	maxBytes := cfg.maxMessageSize
	handler := cfg.handler
	metrics := cfg.metrics
	writeTimeout := cfg.writeTimeout
	pingInterval := cfg.pingInterval
	pongTimeout := cfg.pongTimeout
	readDrain := cfg.readDrain
	limiter := newConnLimiter(cfg.maxConnections)

	return func(w http.ResponseWriter, r *http.Request) {
		// Reject before Accept so a saturated server does not waste
		// the per-conn allocation just to immediately close. The
		// limiter is a no-op when no cap is configured.
		if !limiter.tryAcquire() {
			metrics.observeRejected(rejectReasonMaxConnections)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "websocket: server at capacity", http.StatusServiceUnavailable)
			return
		}
		defer limiter.release()

		raw, err := coderws.Accept(w, r, acceptOpts)
		if err != nil {
			// coder/websocket has already written an HTTP error
			// response by this point; we only need to surface the
			// failure in logs without leaking driver text.
			logger.WarnContext(r.Context(), "websocket: upgrade failed",
				redact.Error(err),
			)
			return
		}
		raw.SetReadLimit(maxBytes)

		// Derive a per-connection context that closes when the
		// request body closes or when the connection is torn down,
		// whichever comes first. Use [context.WithoutCancel] on the
		// request context so the connection lifetime is not bounded
		// by the upgrade handler's stdlib timeout (the handshake is
		// done — further reads happen on the hijacked TCP connection).
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()

		conn := &Conn{
			inner:        raw,
			ctx:          ctx,
			cancel:       cancel,
			logger:       logger,
			metrics:      metrics,
			writeTimeout: writeTimeout,
		}
		metrics.connOpened()
		if onOpen != nil {
			onOpen(conn)
		}
		defer func() {
			if onClose != nil {
				onClose(conn)
			}
		}()

		// WithReadDrain: own the read side so push-only handlers that
		// never Read still detect peer disconnect. CloseRead is
		// independent of the heartbeat; it is not silently dropped when
		// WithPingInterval is unset.
		//
		// coder/websocket's CloseRead cancels only the *derived* context
		// it returns — not the kit's per-connection parent. Watch that
		// derived context and cancel the kit Conn so push handlers parked
		// on conn.Context().Done() observe peer disconnect promptly.
		if readDrain {
			drainCtx := raw.CloseRead(ctx)
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
		if pingInterval > 0 {
			go runHeartbeat(ctx, conn, pingInterval, pongTimeout, logger, metrics)
		}

		closeCode := coderws.StatusNormalClosure
		closeReason := ""

		// Recover panics from the user handler so a misbehaving
		// callback cannot crash the entire HTTP server. The recovery
		// writes a [StatusInternalError] close frame with a
		// non-leaking reason and lets the deferred Close clean up.
		func() {
			defer func() {
				if rv := recover(); rv != nil {
					closeCode = coderws.StatusInternalError
					closeReason = "internal error"
					logger.ErrorContext(ctx, "websocket: handler panicked",
						redact.Panic(rv),
					)
				}
			}()
			if err := handler(ctx, conn); err != nil {
				// A handler that surfaces the read error on a routine
				// peer disconnect (the natural read-loop pattern) must
				// not have that normal close escalated into a
				// StatusInternalError / WARN. Treat a normal (1000) or
				// going-away (1001) close as the graceful shutdown it
				// is; everything else is a genuine handler error.
				if IsNormalClosure(err) {
					closeCode = coderws.CloseStatus(err)
				} else {
					closeCode = coderws.StatusInternalError
					closeReason = "handler error"
					logger.WarnContext(ctx, "websocket: handler returned error",
						redact.Error(err),
					)
				}
			}
		}()

		// Delegate to Conn.Close so cancel-before-handshake ordering is
		// preserved (heartbeat re-check classifies teardown as graceful)
		// and the close path cannot drift from the exported API.
		_ = conn.Close(StatusCode(closeCode), closeReason)
	}
}
