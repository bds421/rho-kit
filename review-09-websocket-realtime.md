# Code review: WebSocket & realtime (stage 1 — unverified findings)

## Scope

- **Directories**: httpx/websocket/, realtime/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 15 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 1 |
| LOW | 14 |
| **Total (deduplicated)** | **15** |

**Reviewer impressions:**

> Both modules show unusually security-conscious craftsmanship at the transport layer: httpx/websocket has safe same-origin defaults with a deliberately greppable WithAnyOriginUnsafe escape hatch, bounded read limits, redacted errors, bounded metric cardinality, panic recovery, and fail-fast option validation; the JWT verification chain (jwtutil) it delegates to enforces exp/sub, caps token length, and pins typ. The serious weakness is concentrated in realtime/centrifuge, whose default callback wiring inverts the upstream framework's fail-closed channel authorization into allow-all subscribe/publish while documenting an extension path that does not exist — an out-of-the-box node built per the quick start permits cross-user channel eavesdropping and message injection. Fix that wiring and the remaining findings are polish-level.

> httpx/websocket is high-quality kit code: fail-fast option validation, bounded metric cardinality, redacted errors, idempotent close, and a genuinely thorough test suite covering origin policy, size limits, heartbeat, and slow-consumer paths — the remaining issues there are consistency gaps (WithReadDrain's silent no-op, Ping's divergence) and one materially wrong lifecycle comment. realtime/centrifuge is noticeably less mature: its callback wiring inverts centrifuge's default-deny channel authorization into allow-all subscribe/publish as a side effect of metrics, its documented extension path (Underlying().OnConnect) silently destroys that same wiring, WebsocketHandler exposes zero configuration despite the sibling package setting the precedent, and the security-critical connect-auth path is untested with a non-compiling quick-start example. The two packages read as if written to the same standard on the surface (excellent godoc discipline) but only httpx/websocket actually meets it.

> This scope is well above average: idempotent close handling, nil-safe metrics receivers, bounded Prometheus label cardinality, careful redaction of driver error text, and unusually honest doc comments that explain concurrency rationale. The httpx/websocket package is close to production-solid — its remaining defects are a discarded CloseRead context that breaks the documented cancellation promise for read-drain push handlers, and a residual teardown/heartbeat ordering race that only produces metric/log noise. The realtime/centrifuge wrapper is weaker: its metrics-driven accept-all subscribe/publish callbacks invert centrifuge's deny-by-default authorization and cannot be safely overridden through the documented Underlying() path, and the Start/Stop lifecycle uses two uncoordinated atomics despite advertising full concurrency safety.

> Code quality in this scope is high: both packages show careful, security-literate engineering — same-origin enforcement by default with a deliberately grep-able WithAnyOriginUnsafe escape hatch, a 1 MiB default read limit applied via SetReadLimit, bounded Prometheus label cardinality everywhere, consistent error redaction, idempotent close handling, and unusually thorough documentation of trade-offs. The standout defect is in realtime/centrifuge: the metrics-driven OnSubscribe/OnPublish wiring silently reverses centrifuge's deny-by-default channel authorization into allow-all, while the documented override path (Underlying().OnSubscribe) does not actually exist — a fail-open authz default that undermines the package's otherwise strong posture. httpx/websocket is solid apart from its all-opt-in resource-exhaustion mitigations and one inaccurate lifecycle comment around WithReadDrain.

> httpx/websocket is high-quality kit code: thorough godoc with security rationale, fail-fast option validation, bounded metric cardinality, idempotent close, and good test coverage (handler, heartbeat, limiter, conn all tested) — remaining issues there are consistency polish (readDrain gating, doc overstating CloseRead's context cancellation, one duplicated close path). realtime/centrifuge is noticeably less mature: its metrics wiring silently converts centrifuge's deny-by-default channel model into allow-all subscribe/publish, the documented extension path for channel authorization does not actually work with centrifuge's single-handler callback model, WebsocketHandler exposes no origin or message-size configuration despite the sibling package treating those as first-class, and the JWT auth path is untested. The two packages share good conventions (redacted errors, OpaqueLabelValue cardinality guards, option panics) but diverge sharply in misuse-resistance.

> This is unusually disciplined library code: exhaustive doc comments that state concurrency contracts, idempotent close via sync.Once with a CAS-based close-code record, a correct CAS connection limiter, redaction-preserving error wrapping verified to keep errors.Is chains intact, and a heartbeat loop that carefully separates deadline expiry from ordinary connection death. The httpx/websocket adapter is close to production-solid; the real problems cluster in the centrifuge wrapper, where the metrics-motivated auto-approving subscribe/publish callbacks invert centrifuge's default-deny posture and the Start/Stop atomics have check-then-act windows, plus one contract-vs-implementation gap in the WithReadDrain context-cancellation path where the code's own comment describes behavior coder/websocket's CloseRead does not provide.

> Both packages are carefully written at the transport level: httpx/websocket has genuinely good security defaults (same-host origin, 1MiB read limit, redacted errors, bounded metric cardinality, deliberately grep-able WithAnyOriginUnsafe) and thoughtful concurrency/idempotency handling. The realtime/centrifuge wrapper is weaker where it matters most: its metrics-motivated callback wiring inverts centrifuge's default-deny channel authorization into allow-all subscribe/publish, and the documented remediation path (registering authz on the underlying node) does not actually exist as described — this is the one finding that should block adoption in multi-tenant services until fixed. Everything else is polish-level: lifecycle edge cases, metric accuracy, and missing configuration surface.

> Both packages are unusually well-documented and show real care around idempotent close, nil-safe metrics receivers, bounded label cardinality, redacted error wrapping, and thoughtful heartbeat race handling; the httpx/websocket module is close to production-grade with only observability-classification races and a readDrain lifecycle gap. The realtime/centrifuge wrapper is weaker: its default callback wiring silently converts centrifuge's deny-by-default subscribe/publish into allow-all, and its documented extension path (registering callbacks on the underlying node) conflicts with centrifuge's single-slot handler registration, so the two HIGH findings there are design-level rather than typo-level. Test coverage in scope is substantial (unit tests for limiter, heartbeat, handler, node), but none of the tests exercise the readDrain disconnect path or caller-supplied centrifuge callbacks.

> httpx/websocket is a genuinely well-crafted adapter: thorough godoc with security rationale, fail-fast option validation, nil-safe metrics, bounded-cardinality labels, and strong test coverage including origin, capacity, heartbeat and read-drain scenarios — the main defect is the discarded CloseRead context that breaks the documented WithReadDrain cancellation contract, plus some close-protocol duplication. realtime/centrifuge is noticeably less mature: its callback wiring inverts centrifuge's default-deny channel authorization into accept-all, its documented extension path references API that does not exist, WebsocketHandler exposes zero configuration, and the JWT connect-auth path is entirely untested.

> The httpx/websocket package is high quality: careful idempotent close, correct context-cancellation lifecycle, redacted errors, nil-safe metrics, a well-reasoned CAS-based connection limiter, and unusually thorough godoc that matches the code. The realtime/centrifuge wrapper is thinner and has one serious design/security flaw — its metrics hooks install allow-all OnSubscribe/OnPublish callbacks that silently disable centrifuge's deny-by-default channel authorization — plus a non-configurable WebSocket handler and a couple of smaller labelling/auth-input smells. Overall solid engineering, but the centrifuge authz downgrade needs to be fixed before relying on this module for multi-tenant channels.

> This scope is generally high quality: the httpx/websocket adapter is defensively written with fail-closed origin defaults (coder/websocket same-origin, opt-in WithAnyOriginUnsafe), a bounded 1 MiB read limit, an idempotent close path, redacted error text, and correct limiter/goroutine lifecycle management — I found no real defects there. The centrifuge JWT connect path is also fail-closed (empty/invalid tokens rejected, jwtutil enforces exp). The one material security concern is that the kit's centrifuge callback wiring installs blanket allow-all subscribe/publish handlers, silently converting centrifuge's fail-closed channel-access default into open access and creating a multi-tenant isolation risk for operators who wire connection auth but not channel authz.

> This is high-quality, carefully written transport-layer code: idempotent close via sync.Once, atomic close-code accounting with no double-count of the active gauge, consistent nil-receiver guards on all metrics helpers, redacted errors, a correct CAS-based connection limiter, and thorough doc comments explaining non-obvious lifecycle decisions. The issues found are narrow lifecycle/concurrency edges rather than systemic flaws: a TOCTOU window in centrifuge Start/Stop, a single-slot callback override that undermines the wrapper's stated auth/metrics guarantee, and two minor context-cancellation asymmetries around the heartbeat. Message-size limits, origin defaults (same-origin by default, explicit WithAnyOriginUnsafe), and JWT exp handling are all sound.

> This is high-quality, carefully-reasoned code: atomic close accounting is idempotent via a shared sync.Once, the connLimiter correctly uses a CAS loop, metric receivers are uniformly nil-guarded, and the heartbeat goes to real lengths to distinguish graceful teardown from a genuine pong-timeout. Concurrency primitives are used correctly and I found no data races, deadlocks, or hard goroutine leaks in supported configurations. The one substantive gap is the readDrain path discarding CloseRead's cancellation-signal return value, which makes an inline correctness claim untrue and delays peer-disconnect detection for push-only handlers.

> Both packages are carefully written, heavily documented, and thoughtful about metric cardinality, redaction, idempotent close, and connection-limit/heartbeat lifecycle — the httpx/websocket adapter in particular is close to production-grade with well-reasoned goroutine and context handling. The main weakness is in realtime/centrifuge, where the channel-authorization story is both permissive-by-default and documented against a centrifuge API that doesn't exist, making it hard to wire authz without losing the kit's own metrics. Remaining issues are minor consistency and dead-code polish.

> The httpx/websocket adapter is high quality for this lens: origin checks default to same-origin (verified against coder/websocket v1.8.15), read limits and connection caps are enforced, errors are redacted, and goroutine/heartbeat lifecycle is cleanly bounded by the per-connection context. The centrifuge wrapper's connection-level JWT auth is sound (jwtutil rejects tokens missing exp, so ExpireAt is never fail-open), but its channel-level authorization is a serious unsafe default: the kit converts centrifuge's deny-by-default subscribe/publish into accept-all purely to emit metrics, and the documented override path is non-functional.

## Findings

### [MEDIUM] Default config has no heartbeat and no write timeout — a peer that stops reading pins a goroutine and fd indefinitely

- **Where**: `httpx/websocket/options.go:64`
- **Dimension**: security
- **Detail**: defaultConfig (options.go:64-69) sets only maxMessageSize and logger; pingInterval, pongTimeout, writeTimeout and maxConnections all default to zero/disabled. With writeTimeout unset, Conn.writeCtx (conn.go:138-139) returns the per-connection context with no deadline, so WriteMessage/WriteJSON block forever on a peer that opens a connection and never reads (zero TCP receive window); with no ping interval, half-open connections survive until the kernel TCP keepalive (~2h, as the package's own doc at doc.go:90-93 admits). Failure scenario: an attacker opens N connections to a Handle endpoint configured with WithMaxConnections and simply stops ACKing — each server push parks a handler goroutine permanently, the connLimiter saturates, and all legitimate clients receive 503 until process restart; without WithMaxConnections the same attack exhausts fds/memory instead. All mitigations exist but every one is opt-in.
- **Suggestion**: Ship safe defaults: a non-zero default write timeout (e.g. 30s) and/or a default ping interval, with explicit opt-out options (WithNoWriteTimeout / WithNoHeartbeat) for callers who genuinely want unbounded blocking.

### [LOW] ReadJSON/WriteJSON duplicate the read/write + metrics + close-recording bodies of ReadMessage/WriteMessage

- **Where**: `httpx/websocket/conn.go:198`
- **Dimension**: smell
- **Detail**: ReadJSON (conn.go:198-212) repeats the inner Read, recordCloseFromError, and observeMessage sequence of ReadMessage (164-172); WriteJSON (219-232) repeats the writeCtx/Write/recordCloseFromError/observeMessage sequence of WriteMessage (182-191). Four copies of the connection-teardown-and-metrics protocol invite drift — e.g. a future fix to the metrics or close-recording path applied to one method but not its JSON twin would silently skew httpx_websocket_messages_total or miss context cancellation on one code path.
- **Suggestion**: Implement ReadJSON in terms of ReadMessage and WriteJSON in terms of WriteMessage (the only delta is the error-wrap prefix, which can be tolerated or parameterised).

### [LOW] Conn.Ping is inconsistent with the other I/O methods: no recordCloseFromError and no writeTimeout

- **Where**: `httpx/websocket/conn.go:241`
- **Dimension**: api-design
- **Detail**: ReadMessage, WriteMessage, ReadJSON, and WriteJSON all call recordCloseFromError on failure, which cancels the per-connection context and records the close code for metrics. Conn.Ping (documented for callers 'driving their own heartbeat') only wraps the error: a user-driven ping that observes connection death neither cancels conn.Context() nor records the close code, so a sibling goroutine parked on ctx.Done() does not wake — subtly violating the Conn.Context contract ('cancelled when the connection closes for any reason') for exactly the usage Ping's godoc advertises. Ping also ignores WithWriteTimeout, unlike every write path.
- **Suggestion**: Call c.recordCloseFromError(err) on Ping failure (matching read/write), and consider bounding the outbound ping frame with writeCtx().

### [LOW] Conn.Ping error path skips recordCloseFromError, unlike every other I/O method

- **Where**: `httpx/websocket/conn.go:242`
- **Dimension**: error-handling
- **Detail**: ReadMessage, WriteMessage, ReadJSON and WriteJSON all call c.recordCloseFromError(err) on failure so the per-connection context is cancelled and the close-code metric label reflects reality. Conn.Ping (conn.go:242) wraps the error but does not. Failure scenario: an application drives its own heartbeat via Conn.Ping (the documented use case, "instead of WithPingInterval") while another goroutine parks on Conn.Context().Done(); the peer dies, Ping returns an error, but the context is never cancelled and the parked goroutine leaks until some other read/write happens to fail.
- **Suggestion**: Call c.recordCloseFromError(err) in Conn.Ping's error branch.

### [LOW] Invalid WithOriginPatterns globs surface as a per-request 403 instead of failing fast at startup

- **Where**: `httpx/websocket/handler.go:80`
- **Dimension**: api-design
- **Detail**: Every other misconfiguration in this package panics at Handle() registration time (nil handler, non-positive sizes/durations, pongTimeout without pingInterval — the package explicitly champions fail-fast). But origin patterns are passed through unvalidated; a syntactically invalid path.Match pattern (e.g. "app.[example.com") only errors inside coderws.Accept, so every single upgrade request fails with a logged 'upgrade failed' warning and the endpoint is dead in production while the process starts cleanly and health checks pass.
- **Suggestion**: In Handle(), validate each pattern at registration with path.Match(pattern, "") and panic on path.ErrBadPattern, matching the package's fail-fast convention.

### [LOW] Handle's teardown re-implements Conn.Close by reaching into closeOnce/closed/closeCode from outside the type

- **Where**: `httpx/websocket/handler.go:167`
- **Dimension**: smell
- **Detail**: Lines 167-174 manipulate conn.closeOnce, conn.closed, and conn.closeCode directly and call raw.Close, duplicating the body of Conn.Close (conn.go:252-270). Conn.Close is already idempotent and returns nil on repeat calls, so the guard `!conn.closed.Load()` plus the inline copy buys nothing over `_ = conn.Close(StatusCode(closeCode), closeReason)`. The two copies have already drifted: the inline version omits cancelCtx (covered only by the surrounding defer cancel()), and any future change to Conn.Close (ordering, metrics) must be mirrored by hand or the paths diverge.
- **Suggestion**: Replace the inline block with a call to conn.Close(StatusCode(closeCode), closeReason).

### [LOW] Handle re-implements Conn.Close inline, reaching into closeOnce/closed/closeCode and dropping the cancel-before-handshake ordering Conn.Close documents as important

- **Where**: `httpx/websocket/handler.go:168`
- **Dimension**: smell
- **Detail**: Lines 167-174 duplicate Conn.Close's body (closeOnce.Do, closed.Store, closeCode.CompareAndSwap, metrics.connClosed) instead of calling conn.Close(StatusCode(closeCode), closeReason). The stated reason — keeping Conn.Close idempotent for handlers that closed explicitly — is already guaranteed by closeOnce, so the duplication buys nothing. It also silently diverges: Conn.Close cancels the per-connection context *before* the potentially multi-second close handshake (conn.go:258-262, with a comment explaining why), while the inline copy runs raw.Close first and relies on the deferred cancel() afterward, so the heartbeat goroutine can fire a spurious ping (counted as pings_total{result="error"}) during the handshake. Any future change to Conn.Close (e.g. new metric) must be mirrored here by hand. ReadJSON duplicating ReadMessage's read/metrics block (conn.go:199-204 vs 164-171) is the same pattern in miniature.
- **Suggestion**: Replace the inline block with `_ = conn.Close(StatusCode(closeCode), closeReason)`, and have ReadJSON call c.ReadMessage().

### [LOW] Handler teardown closes the raw conn before cancelling the per-connection context, re-opening the graceful-shutdown race the heartbeat's ctx re-check was added to fix

- **Where**: `httpx/websocket/handler.go:171`
- **Dimension**: concurrency
- **Detail**: The final close block (handler.go:167-174) calls raw.Close directly inside conn.closeOnce.Do without first cancelling ctx; cancel() only runs afterwards via the defer at line 99. Conn.Close deliberately cancels the context BEFORE inner.Close (conn.go:262) so the heartbeat's re-check (heartbeat.go:63-67) classifies a teardown-racing ping as a graceful exit — but this handler-exit path skips that ordering. Failure scenario: handler returns while a heartbeat Ping is in flight; raw.Close makes Ping unblock with net.ErrClosed (coder conn.go:252-253), the heartbeat re-checks ctx and finds it NOT yet done, so a routine shutdown emits pings_total{result="error"} and a WARN "websocket: ping failed; closing connection" log line. No functional damage (the subsequent conn.Close is a no-op because closeOnce is consumed), but the metric/log noise the comment claims was eliminated still occurs on every shutdown that races a tick.
- **Suggestion**: Inside the closeOnce.Do block call conn.cancelCtx() (or simply delegate to conn.Close(StatusCode(closeCode), closeReason)) before raw.Close so ctx is observably done when the heartbeat's Ping unblocks.

### [LOW] WithReadDrain without WithPingInterval is silently dropped, contradicting the package's own fail-fast rationale

- **Where**: `httpx/websocket/options.go:219`
- **Dimension**: api-design
- **Detail**: Handle() panics when WithPongTimeout is configured without WithPingInterval precisely because "an inert, silently-dropped setting" is treated as a startup bug (handler.go:40-42). Yet WithReadDrain in the identical situation is silently ignored (handler.go applies readDrain only inside the pingInterval > 0 branch). Failure scenario: a push-only service configures WithReadDrain but forgets WithPingInterval; no internal reader runs, control frames are never pumped, inbound data frames from the peer are never policed or drained, and half-open connections are detected only by the OS keepalive (often 2 h) — the exact failure modes the option exists to prevent, with no startup signal.
- **Suggestion**: Panic in Handle() when readDrain is set without a ping interval, matching the WithPongTimeout treatment (or actually honor readDrain independently of the heartbeat).

### [LOW] WithMetrics godoc contradicts itself about nil handling

- **Where**: `httpx/websocket/options.go:244`
- **Dimension**: smell
- **Detail**: The comment opens with 'Pass nil to fall back to [prometheus.DefaultRegisterer] semantics via [NewMetrics]' and then ends with 'this option panics on nil to surface miswiring at startup' — and the code panics (line 249-251). A reader skimming the first sentence will pass nil expecting the default registerer and get a startup panic instead.
- **Suggestion**: Reword to match the behavior, e.g.: 'WithMetrics registers the kit metric set on reg. Omit the option entirely for an unmetered handler; passing nil panics.'

### [LOW] Package quick-start example does not compile: jwtutil.NewVerifier and WithJWKSURL do not exist

- **Where**: `realtime/centrifuge/doc.go:32`
- **Dimension**: api-design
- **Detail**: doc.go's quick start shows `verifier, _ := jwtutil.NewVerifier(jwtutil.WithJWKSURL("https://issuer/.well-known/jwks.json"))`, and doc.go:68 references 'a kit [jwtutil.Verifier]'. The actual security/jwtutil API is `NewProvider(url string, httpClient *http.Client, refresh time.Duration, opts ...ProviderOption) *Provider` (jwtutil.go:740) and `NewProviderWithKeySet`; there is no NewVerifier, no WithJWKSURL option, and no Verifier type. A first-time integrator copying the canonical example gets compile errors and must reverse-engineer the real constructor.
- **Suggestion**: Update the quick start to the real jwtutil.NewProvider signature and fix the [jwtutil.Verifier] reference to [jwtutil.Provider] (also stated as 'jwtutil.Verifier' in options.go WithJWTAuth prose).

### [LOW] No-verifier connect path counts every connection as 'accepted' and relies on downstream centrifuge behavior to fail closed

- **Where**: `realtime/centrifuge/node.go:181`
- **Dimension**: security
- **Detail**: When no WithJWTAuth is configured (verifier == nil), OnConnecting immediately records connectOutcomeAccepted and returns an empty ConnectReply with no Credentials. centrifuge then disconnects the client with 'client credentials not found' unless the caller injected credentials via context (verified in client.go:2420), so the path is fail-closed only by virtue of upstream internals the kit does not control or test; meanwhile the kit's connects_total{outcome="accepted"} metric counts connections that were actually refused, misleading operators about auth posture. An unauthenticated node is also the silent default of omitting one option.
- **Suggestion**: When verifier is nil and no ctx credentials will be present, either require an explicit WithAnonymousUnsafe-style opt-in (matching the httpx 'Unsafe' convention) or at least document/name the anonymous mode explicitly and only count 'accepted' after centrifuge accepts.

### [LOW] Verified JWT with empty subject yields anonymous centrifuge credentials

- **Where**: `realtime/centrifuge/node.go:199`
- **Dimension**: security
- **Detail**: OnConnecting propagates claims.Subject directly into cfg.Credentials.UserID (node.go:197-202) without checking it is non-empty. A token that verifies but carries no 'sub' claim (e.g. a client-credentials/machine token from the same issuer) produces a centrifuge connection with an empty user ID — centrifuge's anonymous identity — so audit trails, per-user channel conventions ('user:<id>'), and presence attribution silently degrade while the connection still counts as connectOutcomeAccepted. Failure scenario: an issuer that also mints service tokens lets a service token holder connect as an indistinguishable anonymous user despite the operator believing WithJWTAuth enforces identified principals.
- **Suggestion**: Reject tokens whose Subject is empty (return cfg.DisconnectInvalidToken) or add an option to require a non-empty subject.

### [LOW] extractBearer treats arbitrary connect Data blob as a bearer token

- **Where**: `realtime/centrifuge/node.go:225`
- **Dimension**: api-design
- **Detail**: When e.Token is empty, extractBearer falls back to strings.TrimPrefix(string(data), "Bearer ") on e.Data. Data is a freeform, application-defined connect payload (commonly JSON), not an auth field. A client that sends legitimate non-token Data will have the whole blob treated as a candidate JWT (fails verification -> rejected, so it fails closed, which is acceptable), but the API conflates two unrelated inputs and makes the auth source ambiguous and fragile — a caller using Data for its own purposes cannot combine that with WithJWTAuth without surprising rejections.
- **Suggestion**: Restrict token extraction to the dedicated Token field, or make the Data fallback explicit and opt-in with a documented format rather than silently reinterpreting arbitrary Data as a bearer token.

### [LOW] extractBearer feeds the entire raw connect Data blob to the JWT verifier as a token

- **Where**: `realtime/centrifuge/node.go:234`
- **Dimension**: api-design
- **Detail**: When ConnectEvent.Token is empty, extractBearer casts the whole e.Data payload to a string, strips an optional "Bearer " prefix, and returns it as the bearer token. Centrifuge's connect Data is a freeform application payload — commonly a JSON object — not a token channel; there is no documented centrifuge client convention of sending a bare JWT there. Consequence: any client that sends structured connect data without the Token field has arbitrary attacker-controlled bytes run through jwtutil VerifyContext (harmless cryptographically, but an odd expansion of the auth input surface), gets a misleading 'token verification failed' WARN log per attempt, and legitimate no-token-but-has-data clients are indistinguishable from bad-token clients in logs/metrics.
- **Suggestion**: Drop the Data fallback (require the Token field), or at minimum only attempt it when the payload plausibly is a compact JWT (e.g. matches the three-dot-segment shape) and document which client versions actually need it.

