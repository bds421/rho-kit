// Package centrifuge wraps github.com/centrifugal/centrifuge with
// kit-flavored lifecycle, observability, and authentication.
//
// # The niche
//
// The kit ships three real-time surfaces; pick the one that
// matches your trust model:
//
//   - `httpx/websocket` — raw bidirectional WebSocket. Browser
//     clients use the native `new WebSocket(...)` API and parse
//     whatever frames you write. Reach for it when you control
//     both endpoints and want full protocol freedom.
//   - `infra/messaging/*` — backend-to-backend pub/sub (AMQP,
//     Kafka, NATS, Redis). Browsers never participate in this
//     layer.
//   - `realtime/centrifuge` (this package) — browser-facing
//     real-time framework with channels, presence, history, and
//     server-driven publish. Use this when you want centrifuge's
//     batteries (token auth, channel subscriptions, message
//     history, presence) without writing the protocol layer
//     yourself.
//
// The trade-off is that centrifuge owns the wire protocol: browser
// clients MUST use a centrifuge SDK (centrifuge-js, mobile
// equivalents). The kit's role is to give the server side
// kit-shaped ergonomics — lifecycle, structured logging, redacted
// errors, bounded-cardinality Prometheus labels, and JWT auth via
// [github.com/bds421/rho-kit/security/v2/jwtutil].
//
// # Quick start
//
//	provider := jwtutil.NewProvider("https://issuer/.well-known/jwks.json", nil, 0)
//
//	node, err := centrifuge.NewNode(
//		centrifuge.WithLogger(slog.Default()),
//		centrifuge.WithJWTAuth(provider),
//		centrifuge.WithChannelClassifier(func(channel string) string {
//			if strings.HasPrefix(channel, "user:") { return "user" }
//			if strings.HasPrefix(channel, "room:") { return "room" }
//			return "other"
//		}),
//	)
//	if err != nil { ... }
//
//	mux.Handle("/connection/websocket", node.WebsocketHandler())
//
//	// Wire into lifecycle.Runner — Start() runs the centrifuge node,
//	// Stop() shuts it down gracefully.
//	runner := lifecycle.NewRunner(slog.Default())
//	runner.Add("centrifuge", node)
//	runner.Add("http", lifecycle.NewHTTPServer(srv))
//	runner.Run(ctx)
//
// # Channel classifiers and cardinality
//
// centrifuge channel names are operator- AND end-user-influenced —
// `user:42`, `room:abc-123`, `tenant:acme/orders` are all common
// shapes. Emitting raw channel names as Prometheus labels would
// explode cardinality, so the kit requires a classifier function
// that maps every channel to a SHORT, BOUNDED set of class strings
// ("user", "room", "system", …) before any metric is emitted. Pass
// the classifier via [WithChannelClassifier]; the default
// classifier maps every channel to "default" so a misconfigured
// caller cannot accidentally blow up cardinality.
//
// # Authentication
//
// [WithJWTAuth] integrates a kit [jwtutil.Provider] with centrifuge's
// `OnConnecting` callback: the bearer token sent by the centrifuge
// client is verified, and the verified subject is propagated to the
// centrifuge connection as the user identifier.
//
// Channel-level authorization is fail-closed by default: without
// [WithSubscribeAuthorizer] / [WithPublishAuthorizer] (or a deliberate
// [WithOpenChannelsUnsafe]), every client subscribe and publish is
// rejected with permission denied. Metrics still fire so denials are
// observable. Do not replace per-client OnSubscribe/OnPublish via
// [Node.Underlying] — centrifuge installs those hooks only through
// OnConnect, and replacing OnConnect drops kit metrics and authz.
//
// # When centrifuge is NOT the right answer
//
// Per [httpx/websocket]'s doc, raw WebSocket is preferable when:
//
//   - You ship a tiny browser app that opens `new WebSocket(...)`
//     and sends JSON. Centrifuge would force the centrifuge SDK in,
//     which is a non-trivial dep on the client.
//   - Non-browser peers (Go CLI tools, server-to-server WS) talk to
//     the same endpoint. The centrifuge SDK does exist for Go but
//     the dependency surface is heavier than `coder/websocket`.
//   - You need a custom binary protocol. Centrifuge frames are
//     fixed (JSON or Protobuf variants).
package centrifuge
