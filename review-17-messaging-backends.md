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
| LOW | 0 |
| **Total (deduplicated)** | **0** |

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

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
