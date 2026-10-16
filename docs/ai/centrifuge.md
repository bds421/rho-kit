# Centrifuge application policy and lifecycle

Use realtime/centrifuge/v2 with the official Centrifuge client SDK. The framework
owns the wire protocol. Configure WithJWTAuth, bounded channel classification and
application channel authorizers. Default subscriptions/publication remain denied.
Mount through a WebSocket-compatible HTTP stack; buffering/ordinary request timeout
wrappers require a qualified bypass.

WithVerifiedConnectPolicy runs after mandatory JWT verification and a nonempty
subject. It receives the ConnectEvent plus a value copy of verified jwtutil.Claims.
Configure jwtutil.WithStringClaims for application claims such as a session ID.
Recheck current session/account/resource authority and bind application connect data
to the verified session; connect data never verifies identity itself.

ConnectionPolicyReply.Context must derive from the incoming context, retaining
request metadata/cancellation. ExpireAt zero retains JWT expiry; a positive value
may tighten but not extend it. Parent/policy context deadlines also cap expiry;
cancelled/expired decisions fail closed. Info may be exposed through presence:
do not place credentials or private session data there.

WithVerifiedRefreshPolicy enables SDK client-side token refresh. Every refresh
verifies a fresh JWT and requires the same subject before application policy.
Recheck session/resource/permission state; reject stale/revoked bindings. Context
is not replaced on refresh. Expiry/cancellation bounds still apply. Without this
option existing expiration behavior is unchanged. Invalid tokens and unavailable
key sets retain distinct disconnect advice; raw callback errors are not returned
to clients.

WithConnectObserver/WithDisconnectObserver run after kit authz/metrics wiring.
Their Connection view exposes ID/UserID/Context/Send/Disconnect, no callback
replacement methods. Callbacks must be concurrency-safe. Register/revoke clients
without replacing OnConnecting/OnConnect/OnSubscribe/OnPublish/OnRefresh. Check
current resource/recipient/feature authority before private sends. Send success
queues a framework push, not proof of browser receipt.

Durable recovery/private projection remain application/backend choices. Hooks do
not create a new broker or durable authorization. Contained native WebSocket tests
cover signed connect, identity preservation, default channel denial, valid refresh,
subject-change rejection, invalid JWT and expiry/context bounds.

`WithSubscriptionReadyObserver` observes a completed native subscribe callback
only when the channel is installed and the connection context is live. A native
ready signal alone is insufficient: upstream also closes it on failures. Denied,
duplicate and channel-limit failures do not start an application stream. The
observer must return promptly; launch application work with connection cancellation.
This is server installation, not acknowledgement that a browser received a reply.

`WithPublicationWritePolicy` gates native publication frames immediately before
transport writing. Its callback is `func(Connection, PublicationWriteEvent) bool`.
The event has `Channel`, `Data`, `FrameType`, `Protocol` and `Unidirectional`.
Native `Data` is exactly one bare encoded `protocol.Reply` for bidirectional
transports or `protocol.Push` for unidirectional transports, without WebSocket
framing or a protobuf length prefix. Protocol/direction come from the transport,
not byte guessing. Decode protobuf with the generated message's `UnmarshalVT`;
the length-prefixed `NewProtobufReplyDecoder` is not suitable for these bytes.

Data is read-only and borrowed for the synchronous callback only. Do not mutate,
retain or pass it to another goroutine; clone for an owned lifetime. Encodings may
be shared across clients. Go does not enforce this immutability contract. The
wrapper does not add metadata to client-visible publication Tags or Info.

The wrapper checks current subscription, connection context and shutdown both
before and after policy. Native close unsubscribes before acquiring its writer
lock for flushing; the subscription guard refuses all remaining publications
without another authority call. Native close releases its channel mutex before
writer flushing, so the guard can read subscription state without a lock
inversion. Refusal never synchronously closes from the writer. A false decision
or failed guard permanently latches
refusal and requests an asynchronous Slow disconnect (the upstream no-flush path).
Later improved rights or token refresh cannot revive queued frames. Control
replies are unchanged. `Connection.Send` and publications embedded in native
history/recovery replies are outside this publication-frame gate; do not enable
those paths for private data without their own current-authority policy.
Callbacks must be concurrency-safe, bounded, and must not synchronously disconnect
or stop the node from inside the writer. Applications remain responsible for
current authority and conservative admission/deadline bounds. There is a gap
between this callback and actual network writing: it does not establish network
completion, browser observation, or a hard guarantee that no bytes cross a deadline.
It cannot implement per-resource expiry without an application-owned decision.

`WithClientQueueMaxSize` bounds native queued bytes, `WithClientChannelLimit`
bounds client subscriptions, and `WithMaxMessagesInFrame` sets native aggregation.
All require positive values. Without a publication policy, omission preserves
upstream defaults. Setting aggregation to one does not bound queued message count.
With a publication policy,
omitted aggregation becomes one and explicit values above one fail construction:
an earlier allowed item must not wait behind later policy calls in a bulk write.
Native close flushing may still use WriteMany regardless of that limit; the
subscription guard above rejects its pending publications. These options neither
replace the writer nor introduce a parallel protocol/queue. Applications must
separately bound their own admission.

`Node.Ready` is true after native Run succeeds through Node.Start, while Start's
context is active and Stop has not begun. Calling Underlying.Run directly does
not set it. It establishes no provider/session authority. Lifecycle tests exercise
actual Start/cancellation; native-client tests exercise subscription failures and
publication encoding and metadata for JSON/protobuf, bidirectional/unidirectional
transports, refusal with queued frames, PermissionDenied/Shutdown close flushing,
and close/unsubscribe/cancellation/Node.Stop while a policy call is pending.
These are real native client/queue fixtures, not a replacement writer or provider
qualification.

Upstream v0.38 retains the queue-item channel only when its namespace metric
callback is configured. Enabling the publication policy supplies a fixed default
namespace label and disables namespace caching, so channel names reach the gate
without becoming metric labels or cached metric keys. Omitted policy retains
upstream metric configuration. No protocol bytes are decoded to recover channels.
