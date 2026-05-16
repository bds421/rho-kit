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
	if cfg.compression {
		acceptOpts.CompressionMode = coderws.CompressionContextTakeover
	}

	maxBytes := cfg.maxMessageSize
	handler := cfg.handler
	metrics := cfg.metrics

	return func(w http.ResponseWriter, r *http.Request) {
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
			inner:   raw,
			ctx:     ctx,
			logger:  logger,
			metrics: metrics,
		}
		metrics.connOpened()

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
				closeCode = coderws.StatusInternalError
				closeReason = "handler error"
				logger.WarnContext(ctx, "websocket: handler returned error",
					redact.Error(err),
				)
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
