# AGENTS.md — `realtime/centrifuge`

## When to use this package

- The service needs browser-facing real-time pub/sub with channels, presence, and message history.
- Clients can adopt the centrifuge JS / mobile SDK (centrifuge owns the wire protocol; native `WebSocket` is not enough).
- The service is OK with the centrifuge framework's opinions (channel naming, subscription model, history/presence schemas).

## When to use something else

- **Raw bidirectional WebSocket where the browser uses native `new WebSocket(...)`:** `httpx/websocket` — centrifuge would force the JS SDK on the client side.
- **Backend-to-backend pub/sub (no browser):** `infra/messaging/*` (AMQP / Kafka / NATS / Redis) — heavier on broker discipline, lighter on real-time semantics.
- **Server-sent events (one-way streaming):** stdlib `http.Flusher` with `httpx/middleware` is simpler; centrifuge is overkill.
- **Non-browser peers (Go CLI tools, server-to-server WS):** `httpx/websocket` — the Go centrifuge client exists but pulls a heavier dep closure.

## Key APIs

- `NewNode(opts...)` — constructs the kit-wrapped node. Implements `lifecycle.Component` so it composes with `lifecycle.Runner` alongside the HTTP server.
- `WithJWTAuth(*jwtutil.Provider)` — installs the connect-time bearer-token verification callback. Per-channel authz is the caller's responsibility via `Node.Underlying().OnSubscribe`.
- `WithChannelClassifier(fn)` — REQUIRED for production. Maps each channel to a short bounded set of class strings ("user", "room", "system"). Without it, the default classifier returns "default" for everything — no observability.
- `Node.WebsocketHandler()` — returns `http.Handler` to mount at the centrifuge client's expected path (`/connection/websocket`).
- `Node.Underlying()` — escape hatch to the wrapped `*centrifuge.Node` for advanced features (RPC handlers, server-side subscriptions, history queries). DO NOT replace `OnConnecting`; the kit's auth chain is installed first.

## Common mistakes

- **Skipping `WithJWTAuth` on a public-facing node** — connections will be anonymous. The kit allows it (some services genuinely want unauthenticated channels) but the omission is a security decision that needs to be deliberate.
- **`ChannelClassifier` that returns the raw channel name** — explodes Prometheus cardinality. The kit's `safeClass` helper validates against `promutil.ValidateStaticLabelValue` and falls back to `OpaqueLabelValue` if the classifier misbehaves, but a classifier returning 1000+ distinct values still hurts query performance.
- **Calling `Stop` without prior `Start`** — previous bug, fixed: `Stop` is now a safe no-op when `Start` was never reached. `lifecycle.Runner` cleanup paths can call it freely.
- **Replacing `Node.Underlying().OnConnecting` with custom logic** — bypasses the kit's JWT auth chain. Always extend (chain) rather than replace.

## Observability

- Metrics: `realtime_centrifuge_connects_total{outcome}`, `realtime_centrifuge_disconnects_total{reason}`, `realtime_centrifuge_subscribes_total{class}`, `realtime_centrifuge_publishes_total{class}`.
- centrifuge's internal metrics are NOT auto-registered; register them via `node.Underlying().GetMetrics(...)` if needed.

## Wire-protocol caveat

Centrifuge owns the on-wire protocol. Browser clients MUST use a centrifuge SDK. If your app ships a small browser bundle that opens `new WebSocket(...)` and sends raw JSON, use `httpx/websocket` instead — the centrifuge JS SDK is a non-trivial dep on the client.
