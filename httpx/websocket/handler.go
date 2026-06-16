package websocket

import (
	"context"
	"log/slog"
	"net/http"

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
func Handle(opts ...Option) http.HandlerFunc {
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

		// Idle keepalive — spawned only when WithPingInterval is set.
		// Closes the connection if the peer stops responding to pings,
		// which causes the read loop below to unblock with an error.
		if pingInterval > 0 {
			// WithReadDrain: own the read side so the heartbeat's Pong
			// is pumped for push-only handlers that never read. Driving
			// the internal reader also cancels the per-connection
			// context when the peer goes away, even with no handler
			// read in flight. coder/websocket's CloseRead is idempotent
			// and a no-op for handlers that do read (they simply do not
			// opt in).
			if readDrain {
				raw.CloseRead(ctx)
			}
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

		// Use the inner Close directly so the kit Conn's idempotent
		// Close still works for callers who closed explicitly inside
		// the handler.
		if !conn.closed.Load() {
			conn.closeOnce.Do(func() {
				conn.closed.Store(true)
				conn.closeCode.CompareAndSwap(0, int64(closeCode))
				_ = raw.Close(closeCode, closeReason)
				metrics.connClosed(int(conn.closeCode.Load()))
			})
		}
	}
}
