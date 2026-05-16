// Package websocket provides a kit-flavored WebSocket adapter on top of
// [github.com/coder/websocket], the modern context-aware fork of
// nhooyr.io/websocket.
//
// # When to use
//
// Reach for this package when a service needs a long-lived,
// bidirectional WebSocket connection that should still benefit from the
// httpx middleware stack — request logging, authentication, rate
// limiting, request IDs, panic recovery, and Prometheus metrics — and
// the kit's logging/redaction conventions for error returns.
//
// # When NOT to use
//
//   - Raw TCP / TLS sockets. Use [net.Listen] or [crypto/tls.Listen]
//     and wire lifecycle by hand; the HTTP upgrade is unnecessary
//     overhead.
//   - One-shot Server-Sent Events. The stdlib [http.ResponseWriter]
//     with [http.Flusher] is simpler and proxies (CDNs, ingress) cope
//     with it better than WebSockets.
//   - Browser pub/sub when an upstream broker (NATS, Redis Streams) is
//     already in the mix. Front a per-connection consumer with an
//     SSE/HTTP endpoint instead of multiplexing on a single WebSocket.
//
// # Composition with httpx middleware
//
// [Handle] returns a stdlib [http.HandlerFunc], so it composes with
// every kit middleware exactly like any other handler:
//
//	mux := http.NewServeMux()
//	wsHandler := websocket.Handle(
//		websocket.WithHandler(echoHandler),
//		websocket.WithLogger(logger),
//		websocket.WithMetrics(reg),
//		websocket.WithMaxMessageBytes(64*1024),
//	)
//	mux.Handle("/ws", auth.JWT(jwks)(ratelimit.IP(100, time.Minute)(wsHandler)))
//	srv := httpx.NewServer(":8080", stack.Default(mux, logger))
//
// The handler receives a request-scoped context that is cancelled when
// the connection closes for any reason, plus a [Conn] wrapper that
// emits Prometheus metrics on read/write/close and returns errors
// wrapped with [redact.WrapError] so backend error text never bleeds
// into logs verbatim.
//
// # Metrics
//
//   - `httpx_websocket_active` — Gauge of currently-open connections.
//   - `httpx_websocket_messages_total{direction}` — Counter of
//     messages by direction (in/out).
//   - `httpx_websocket_message_bytes{direction}` — Histogram of message
//     payload sizes in bytes by direction.
//   - `httpx_websocket_close_total{code}` — Counter of connection
//     closes labelled with normalised close codes. Unknown codes are
//     projected via [promutil.OpaqueLabelValue] so per-tenant or
//     attacker-controlled close codes cannot blow up cardinality.
//
// # Safety
//
//   - The handler recovers panics from the user callback and writes a
//     [StatusInternalError] close frame with a non-leaking reason
//     before tearing down the connection.
//   - [Conn.Close] is idempotent.
//   - Every read/write error wraps the underlying coder/websocket error
//     with [redact.WrapError] so [errors.Is]/[errors.As] still works
//     but [error.Error]() never embeds the inner text.
package websocket
