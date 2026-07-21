# AGENTS.md — `httpx/websocket`

## When to use this package

- The service needs a long-lived bidirectional WebSocket connection where the browser (or other peer) uses the native `new WebSocket(...)` API — i.e. you control the wire protocol and the JSON shape.
- The handler should benefit from the kit's HTTP middleware stack: auth, rate limit, request IDs, panic recovery, structured logging, redacted errors.
- Non-browser peers (Go CLI tools, server-to-server WS) are in scope; centrifuge's SDK would be a heavier client dep.

## When to use something else

- **Browser-facing pub/sub with channels, presence, history:** `realtime/centrifuge` — full framework with batteries; browser uses centrifuge JS SDK.
- **Backend-to-backend async messaging:** `infra/messaging/*` — broker discipline, no WebSocket overhead.
- **One-way server-to-client streaming:** stdlib `http.ResponseWriter` + `http.Flusher` (Server-Sent Events) — simpler and CDN-friendly.
- **Raw TCP / TLS bidirectional bytes:** `net.Listen` / `crypto/tls.Listen` — HTTP upgrade is unnecessary overhead.

## Key APIs

- `Handle(opts...)` — returns `http.HandlerFunc`. Compose with any kit middleware exactly like any other handler.
- `Shutdown(ctx)` — drains every open connection created through package-level `Handle`; call it during process shutdown alongside the HTTP server shutdown.
- `NewHub(opts...)` — creates an isolated connection registry when independent listeners or tests must not participate in package-level `Shutdown`; drain it with `Hub.Shutdown(ctx)`.
- `WithHandler(fn)` — REQUIRED. Application callback signature `func(ctx Context, conn *Conn) error`.
- `WithMaxConnections(n)` — caps concurrent connections per handler. Beyond cap returns `503` + `Retry-After: 1`.
- `WithPingInterval(d)` + `WithPongTimeout(d)` — idle keepalive heartbeat. Defaults: 30s ping / 10s pong. Opt out with `WithNoHeartbeat()`.
- `WithWriteTimeout(d)` — per-write deadline (default 30s). Slow consumer DoS lever — on deadline expiry the connection is dropped. Opt out with `WithNoWriteTimeout()`.
- `WithMaxMessageBytes(n)` — caps inbound message size. Default 1 MiB.
- `WithAnyOriginUnsafe()` — disables same-origin check. The "Unsafe" suffix is deliberate; only safe when every handler independently authenticates the principal.

## Common mistakes

- **`WithNoHeartbeat` in production without another dead-peer path** — production WebSocket services WILL accumulate half-open connections if the default heartbeat is disabled without a substitute. RFC 6455 has no mandatory heartbeat; browsers don't ping.
- **No `WithMaxConnections`** — unbounded concurrency → OOM. Default to a per-handler cap based on your service's connection budget.
- **`WithNoWriteTimeout` on untrusted peers** — a slow consumer wedges a goroutine for minutes (TCP backpressure). Keep the default 30s (or set a value comfortably larger than `largest_message / slowest_realistic_bandwidth`).
- **`WithAnyOriginUnsafe()` without bearer-token auth in the first message** — opens the service to cross-site WebSocket hijacking (CSWSH). Either use explicit `WithOriginPatterns(allowlist)` OR independently auth the principal post-upgrade.
- **`WithCompression()` in high-fanout services** — the default is `NoContextTakeover` (bounded per-conn memory). Avoid `WithCompressionContextTakeover()` unless workload measurements show the memory cost (~32 KiB per direction per conn) is acceptable.
- **`Conn.Close` without `defer`** — handler exits early on an error path, connection orphan until GC. `Close` is idempotent so `defer conn.Close(...)` is always safe.

## Composition with httpx middleware

```go
mux := http.NewServeMux()
wsHandler := websocket.Handle(
    websocket.WithHandler(echoHandler),
    websocket.WithLogger(logger),
    websocket.WithMetrics(reg),
    websocket.WithMaxConnections(10_000),
    // write timeout + heartbeat are on by default (30s / 30s+10s pong)
)
rl := ratelimit.NewLimiter(100, time.Minute)
mux.Handle("/ws", auth.JWT(provider)(ratelimit.Middleware(rl)(wsHandler)))
srv := httpx.NewServer(":8080", stack.Default(mux, logger))
```

## Observability

- Metrics: `httpx_websocket_active` (gauge), `httpx_websocket_messages_total{direction}`, `httpx_websocket_message_bytes{direction}`, `httpx_websocket_close_total{code}`, `httpx_websocket_pings_total{result}` (only when ping interval configured), `httpx_websocket_rejected_total{reason}` (only when max-connections set).
- Close codes pass through `promutil.OpaqueLabelValue` for non-standard codes so per-tenant or attacker-controlled codes cannot inflate cardinality.
