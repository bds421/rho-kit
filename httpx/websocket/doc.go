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
// # Origin policy
//
// The default rejects all cross-origin upgrade handshakes — only
// browsers whose `Origin` matches the request `Host` are accepted.
// Use [WithOriginPatterns] to allow specific cross-origin browsers
// (preferred), or [WithAnyOriginUnsafe] to accept any origin (only
// safe when every handler independently authenticates the connecting
// principal; the "Unsafe" suffix is deliberately grep-able).
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
//   - `httpx_websocket_pings_total{result}` — Counter of heartbeat
//     pings by result (`ok` = pong received within deadline,
//     `timeout` = deadline expired and the connection was closed).
//     Emitted only when [WithPingInterval] is configured.
//   - `httpx_websocket_rejected_total{reason}` — Counter of upgrade
//     requests rejected before reaching the WebSocket protocol.
//     The `reason` label is a bounded enum (currently
//     `max_connections`) so no caller-controlled value can blow up
//     cardinality.
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
//   - Write timeout defaults to [DefaultWriteTimeout] (30s) so a slow
//     peer cannot pin a write goroutine indefinitely. Override with
//     [WithWriteTimeout] or opt out via [WithNoWriteTimeout]. When the
//     deadline expires the underlying connection is closed because the
//     WebSocket framing protocol cannot resume a partial frame.
//   - Idle heartbeat defaults to [DefaultPingInterval] (30s) with
//     [DefaultPongTimeout] (10s). RFC 6455 specifies no mandatory
//     heartbeat and browser clients do not ping; without a heartbeat
//     half-open connections can survive until the kernel TCP keepalive
//     (often 2 h) reclaims them. Override with [WithPingInterval] /
//     [WithPongTimeout] or opt out via [WithNoHeartbeat].
//   - [WithMaxConnections] caps in-flight connections to bound memory
//     and file-descriptor pressure. Rejections respond with
//     `503 Service Unavailable` + `Retry-After: 1` before any
//     WebSocket-level allocation.
//
// # Handshake bounds
//
// The kit deliberately does not expose a `WithUpgradeTimeout` option.
// The upgrade phase (read request headers, write `101 Switching
// Protocols`) is fully bounded by the surrounding [http.Server]
// timeouts — when the kit's [httpx.NewServer] is used these are set
// to `ReadHeaderTimeout: 5s` and `WriteTimeout: 35s` by default.
// After Accept hijacks the connection, the default (or configured)
// heartbeat and write timeout take over.
//
// If you wire a raw [http.Server] yourself, set `ReadHeaderTimeout`
// and `WriteTimeout` explicitly — without them slow-loris-style
// upgrade abuse is unbounded.
package websocket
