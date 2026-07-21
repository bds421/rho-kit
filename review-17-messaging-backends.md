# Code review: Messaging backends (stage 1 — unverified findings)

## Scope

- **Directories**: infra/messaging/amqpbackend, infra/messaging/kafkabackend, infra/messaging/natsbackend
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 11 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 8 |
| **Total (deduplicated)** | **8** |

**Reviewer impressions:**

> This is unusually security-conscious infrastructure code: TLS 1.2 floors with anti-downgrade cloning everywhere, credential redaction via slog.LogValuer, bounded header/body parsing against hostile peers, explicit fail-fast gates (FR-073) against unauthenticated brokers, and careful, well-documented reconnect/ack-ordering logic with prior audit findings tracked inline. The remaining issues are second-order: the FR-073 gates in the Kafka and NATS backends treat password-style auth as a substitute for TLS (permitting cleartext credential transport), and both non-AMQP consumers trust publisher-asserted X-Exchange/X-Routing-Key headers over broker-authorized topics/subjects, which undermines topic-level ACLs at the application layer. Everything else found is edge-case (shared DLQ failure counter across bindings) or polish-level.

> This scope is unusually mature, defensively written code: every backend validates inbound messages, bounds header/body allocations against hostile peers, recovers handler panics, distinguishes permanent vs transient errors with sound poison-pill handling, enforces TLS/SASL floors (FR-073), and nil-safe metrics throughout — clearly the product of multiple prior audit waves. The surviving issues are concentrated in lifecycle edges rather than the data path: the AMQP reconnect state machine has two consistency gaps (Stop fast-path leak, post-Dead resurrection) and the NATS consumer can silently stall on terminal subscription errors. Ack/commit ordering and at-least-once semantics in all three backends check out correct.

> This is an unusually well-hardened messaging layer: consistent TLS 1.2 floors with anti-downgrade cloning, credential redaction via slog.LogValuer, bounded header/body parsing against hostile peers, weak-credential and plaintext-connection startup gates, and visible evidence of many prior audit waves (FR-070..074, N-5, R7-46) with fixes annotated in comments. Reconnect/ack-ordering logic in amqpbackend and the Kafka offset-rewind design are carefully reasoned and documented. The remaining security-lens issues are trust-boundary decisions rather than implementation bugs: SASL/user-password auth accepted as a substitute for TLS (cleartext credential transmission), peer-controlled envelope headers trusted over broker-authoritative metadata, an unvalidated AMQP ReplyTo publish path, and a guest/guest config default that bypasses the otherwise-strict credential validation.

> This scope is unusually mature for infrastructure glue code: consistent nil-safe metrics, panic recovery around every user callback, bounded header/body parsing against hostile peers, TLS floors with explicit hot-rotation escape hatches, and extensive audit-trail comments explaining past fixes (lost reconnect signals, Stop/dial races). The AMQP connection lifecycle is the most carefully engineered piece, though its reconnect loop has a genuine logic slip (backoff/attempt reset defeating the onReconnect failure handling). The main systemic weakness is redelivery pacing on the non-AMQP backends: NATS Naks with zero delay against a 5-delivery budget and Kafka rebalances the whole group per transient handler error, both of which will bite under ordinary transient downstream outages.

> This is unusually mature infrastructure code: extensively documented invariants (with audit-finding references), defensive bounds on attacker-controllable inputs (header depth/byte budgets, delivery size caps), consistent TLS-floor/redaction/metrics patterns across the three backends, and careful ack-ordering and reconnect-race reasoning in the AMQP connection. The dominant weakness is semantic drift between sibling backends — handler timeouts, shutdown grace, Consume return conventions, and retry/redelivery behavior differ in ways that will surprise services switching transports — plus a few genuine gaps: NATS's immediate-Nak redelivery contradicts its own docs and effectively drops messages after rapid MaxDeliver exhaustion, Kafka's convenience constructors can never succeed, and the AMQP DLQ failure cap is shared across bindings.

> The messaging backends are unusually well-hardened for their security lens: consistent TLS-floor cloning with a guarded InsecureSkipVerify opt-in, credentials kept out of logs via LogValue redaction and redact.Error/WrapError, bounded header materialization against hostile publishers, and NATS subject-encoding that prevents wildcard-widening cross-tenant leakage. The main weakness is a design inconsistency in the FR-073 auth guards (Kafka and NATS), which treat "authentication configured" as equivalent to "transport is safe" and therefore permit cleartext-secret auth mechanisms (SASL/PLAIN, NATS username/password/token) over unencrypted connections. No injection, weak-crypto, or fail-open bypass issues were found in the reconnect/ack/lifecycle paths.

> This is high-quality, heavily-audited infrastructure code: nil-safe metrics, consistent credential/URL redaction, bounded header allocations against hostile peers, panic recovery around handlers/hooks/providers, and carefully-reasoned ack/redelivery and TLS-floor semantics, with comments tracing prior audit findings. The defects found are edge-case lifecycle/concurrency refinements (reconnect signal handling, DLQ context propagation, header-drop nondeterminism) rather than mainline logic bugs; the ack ordering, per-publish channel model, and Kafka at-least-once reset logic are sound.

> This is mature, carefully-reasoned infrastructure code: reconnect/redelivery invariants, TLS floors, credential rotation, header-size bounds, and poison-pill handling are all thoughtfully implemented and heavily documented, with panics guarding misuse at construction. The main weaknesses are cross-backend inconsistencies (header stripping, shutdown context grace) and one real performance concern — the Kafka reader-reset-on-transient-error pattern that rebalances the whole consumer group — plus minor dead code. No data-loss or security defects were found in scope.

> This scope is unusually well-hardened for a security review: TLS floors are enforced through tlsclone with an explicit anti-downgrade guardrail, InsecureSkipVerify is only permitted alongside a caller-supplied VerifyConnection, connection/config LogValue implementations emit only booleans (no credential/URL leakage), errors are routed through a redact package, and inbound headers/bodies are size- and depth-bounded against hostile peers. The residual findings are mostly about the FR-073 auth contract treating any SASL/auth as sufficient even without transport encryption (allowing cleartext credential transmission, most concretely with Kafka SASL/PLAIN) plus a minor header-byte-budget inconsistency and a weak default credential. No injection, crypto-misuse, or fail-open authorization bypass was found in the messaging paths.

> This scope is unusually well-engineered for messaging plumbing: reconnect/redelivery/ack semantics are carefully reasoned, panic recovery and poison-pill handling are consistent across all three backends, nil receivers and metrics are defensively guarded, and TLS/auth hardening (FR-073, anti-downgrade floors, credential caching bridges) is thorough. The main real defect is the AMQP reconnect loop resetting its backoff and attempt counter before verifying onReconnect succeeded, which defeats both exponential backoff and the Dead()/max-attempts safety net when topology re-declaration fails against a reachable broker. The Kafka reader-reset-on-error redelivery strategy is correct for at-least-once but pays a group-wide rebalance cost that can amplify under a persistently failing handler.

> This is high-quality, defensively-written messaging code: the reconnect/redelivery state machines, poison-pill handling, TLS-floor enforcement, credential-rotation bridges, and header-size bounds are all thoughtfully engineered and heavily commented with prior-audit references. Most findings are consistency/ergonomics gaps rather than correctness bugs — the notable exception is the Kafka per-error Reader-recreate path, which is correct for offset semantics but can induce a group-wide rebalance storm under a persistently failing handler. The three backends are largely parallel in shape, but the metrics-label opacity and interface-assertion conventions have drifted between them.

## Findings

### [LOW] Graceful-shutdown handler path (WithoutCancel/IsShutdown) fires only by select-race chance

- **Where**: `infra/messaging/amqpbackend/consumer.go:289`
- **Dimension**: concurrency
- **Detail**: The shutdown-grace machinery in ConsumeOnce (lines 288-296: detached WithoutCancel base, shutdownSignalKey, handlerTimeout grace) can only execute when the select at line 257 randomly picks a ready delivery over the equally-ready ctx.Done() case after cancellation. Go's select chooses uniformly among ready cases, so in practice ConsumeOnce usually returns ctx.Err() on the first or second iteration and prefetched deliveries are requeued via channel close instead of being drained through the documented grace path. Failure scenario: a handler relying on IsShutdown(ctx) to 'finish quickly' during shutdown almost never observes it; whether an in-flight prefetched message is processed with the grace deadline or nacked/requeued is nondeterministic run to run. No message loss (at-least-once holds), but the documented shutdown semantics are effectively dead code.
- **Suggestion**: Either remove the grace path, or implement an explicit drain phase after ctx.Done() (bounded non-blocking receive loop over `deliveries` before returning).

### [LOW] AMQP Consume retries a permanent configuration error forever

- **Where**: `infra/messaging/amqpbackend/consumer.go:616`
- **Dimension**: error-handling
- **Detail**: Consume wraps ConsumeOnce in retry.Loop with the default WorkerPolicy (MaxRetries -1, no MaxElapsedTime, nil RetryIf), so every error is retried unboundedly. ConsumeOnce returns a permanent misconfiguration error when b.Retry != nil and c.publisher == nil (consumer.go:219-221) — NewConsumer permits a nil publisher, so this is reachable. That error can never become healthy, yet the loop keeps re-invoking ConsumeOnce every up-to-60s forever, only logging each failure. A misuse that should fail fast at startup instead degrades into a silent infinite retry.
- **Suggestion**: Validate the retry-binding/publisher invariant at construction or in Consume before entering the loop, or classify it as permanent (apperror) so retry.Loop stops.

### [LOW] Consume returns ctx.Err() on graceful shutdown, violating the messaging.Consumer contract and diverging from the Kafka backend

- **Where**: `infra/messaging/amqpbackend/consumer.go:619`
- **Dimension**: api-design
- **Detail**: messaging.Consumer's godoc (infra/messaging/consumer.go:40) states Consume "Returns nil when ctx is cancelled (normal shutdown)", and kafkabackend.Consume returns nil on cancellation, but amqpbackend.Consume ends with `return ctx.Err()` — always context.Canceled after a normal shutdown. Concrete scenario: an external caller following the interface contract (`if err := consumer.Consume(...); err != nil { alert/exit-nonzero }`) reports a spurious failure on every clean shutdown; the kit's own callers only tolerate it via special-casing (start_consumers.go:77 checks ctx.Err(), subscription.go:141 filters errors.Is(context.Canceled)).
- **Suggestion**: Return nil when ctx.Err() != nil after retry.Loop exits (matching kafkabackend), or fix the interface documentation.

### [LOW] deepCopyTable bounds node count/depth but not aggregate byte size, unlike extractStringHeaders

- **Where**: `infra/messaging/amqpbackend/delivery.go:160`
- **Dimension**: security
- **Detail**: headerToMap/deepCopyTable cap the number of nodes (maxHeaderNodes=256) and nesting depth (maxHeaderDepth=8) but never account for the byte size of individual values; the []byte and string cases at deepCopyValue copy the full value with no byte budget. extractStringHeaders (same file, used for msg.Headers) explicitly enforces maxHeaderBytes=64KiB for exactly this reason, so Delivery.Headers has a weaker bound than Message.Headers. Failure scenario: a peer with channel access sends a delivery whose header table holds up to 256 entries each carrying a large value; headerToMap materialises them all into the Delivery.Headers map with prefetch copies in flight. In practice the amplification is capped by the broker's negotiated frame_max (RabbitMQ default 128KiB), which is why this is LOW rather than a real OOM, but the code's own stated byte-budget defense is not applied here.
- **Suggestion**: Thread a byte budget through deepCopyTable/deepCopyValue mirroring extractStringHeaders' maxHeaderBytes so both header maps share the same aggregate-size cap.

### [LOW] ReplySender holds its mutex across the network publish, serializing all RPC replies

- **Where**: `infra/messaging/amqpbackend/rpc_reply.go:38`
- **Dimension**: performance
- **Detail**: Send acquires rs.mu and keeps it through ch.PublishWithContext (a broker round-trip), so concurrent handlers sending replies are fully serialized and one slow/stalled publish (bounded only by the caller's ctx) head-of-line blocks every other reply. This is the exact pattern the sibling Publisher documents avoiding (publisher.go:18-23 opens a per-publish channel precisely to eliminate head-of-line blocking). Concrete scenario: 50 concurrent RPC handlers replying through one ReplySender during a broker slowdown queue up behind a single in-flight publish.
- **Suggestion**: Publish outside the lock (only guard channel acquire/reset), or use a small channel pool / per-call channel like Publisher does.

### [LOW] Consume treats every non-context fetch error as transient forever, masking fatal misconfiguration

- **Where**: `infra/messaging/kafkabackend/subscriber.go:340`
- **Dimension**: error-handling
- **Detail**: The FetchMessage error path warn-logs, increments a metric, sleeps 500ms, and continues indefinitely for all errors that are not context.Canceled/DeadlineExceeded. Permanent failures — SASL authentication rejection, ACL denial, deleted topic — are retried every 500ms forever with no escalation and no way for the caller to observe the failure through Consume's return value; the service looks healthy while consuming nothing. Contrast with the publisher, where auth/config errors surface to the caller per publish.
- **Suggestion**: Classify known-fatal kafka-go errors (auth failed, topic authorization failed, unknown topic when auto-create is off) and return them from Consume; escalate the backoff for repeated fetch failures.

### [LOW] NATS Consume returns while a handler may still be executing; Stop() used instead of Drain()

- **Where**: `infra/messaging/natsbackend/natsbackend.go:854`
- **Dimension**: concurrency
- **Detail**: On ctx cancellation Consume sets stopped and calls consume.Stop() (lines 853-854), then returns nil immediately. jetstream's Stop unsubscribes without waiting for the in-flight callback, so c.dispatch may still be running a user handler (with the already-cancelled parent ctx) after Consume has returned. Failure scenario: caller sequence `Consume returns -> conn.Stop() closes the NATS connection` while the handler is mid-flight -> the handler completes its side effects but jm.Ack() fails on the closed connection, guaranteeing a duplicate redelivery after AckWait even though processing succeeded; there is also no way for the caller to await handler completion before process exit. The AMQP backend, by contrast, at least attempts a detached grace-deadline context for shutdown-time handlers.
- **Suggestion**: Use consume.Drain() (bounded by a timeout) or track dispatch completion (WaitGroup) and wait before returning from Consume.

### [LOW] NATS handlers get an already-cancelled context on shutdown; AMQP gives a grace deadline

- **Where**: `infra/messaging/natsbackend/natsbackend.go:965`
- **Dimension**: api-design
- **Detail**: Consumer.dispatch passes the Consume ctx straight to handler(ctx, delivery) (natsbackend.go:846,965). That ctx is the one cancelled on shutdown (Consume blocks on <-ctx.Done()), so an in-flight NATS handler observes a cancelled context immediately. The AMQP consumer deliberately detaches with context.WithoutCancel and grants a fresh handlerTimeout grace window during shutdown (consumer.go:288-298). The divergence means the same handler logic behaves differently across backends at shutdown — a NATS handler mid-write may be interrupted where the AMQP one would be allowed to finish.
- **Suggestion**: Give NATS handlers an equivalent detached grace deadline on shutdown, or document the intentional difference in both dispatch godocs.
