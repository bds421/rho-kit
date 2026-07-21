# v3 Breaking Proposals

Proposals collected during review remediation. **Not applied on v2.** Each item
is a candidate for the next major version only.

## grpcx: propagate permissions/scopes across trusted-S2S hops

**Status:** Partial mitigation applied on v2; full fix is v3.

**v2 change (applied):** `RequirePermission*` / `RequireScope*` no longer treat
`IsTrustedS2S` as a blanket bypass. Opt in with `WithTrustedS2SBypass()` for
service-level trust. Safer default against permission laundering when mTLS
impersonation stamps empty perms/scopes.

**v3 proposal:** Propagate the caller's permissions and scopes across the S2S
hop (signed metadata or token exchange) so downstream `Require*` can enforce
user entitlements without a bypass. Extend the impersonation guard to receive
the target method for per-RPC delegation policy. Align httpx middleware with
the same default (HTTP still bypasses on trusted-S2S today).

## infra/storage/encryption: bound retained plaintext independently of decrypt

**Status:** Partial v2 fix applied; residual memory-budget redesign is v3.

**v2 change (applied):** `getSem` now gates concurrent decrypt work only and
is released once plaintext is materialised (not on reader `Close`), so a
leaked `ReadCloser` cannot permanently starve encrypted downloads. `Close`
still zeros the plaintext buffer. Tests cover post-materialise concurrent Gets.

**v3 proposal:** Add a distinct retained-plaintext budget / TTL reclaim for
unclosed readers (the v2 fix removes permanent DoS but no longer caps how many
plaintext buffers a leaky caller can hold). Consider a metric when acquire
waits exceed a threshold.

## data/budget/redis: Refund against charge-period window

**Status:** Documented on v2; behavior change is v3.

**v2 change (applied):** `budget.Refunder` and `budget/redis.Budget.Refund`
godoc warn that refunds credit the *current* period bucket, so estimate-then-
reconcile across a window boundary can under-count or drop credit.

**v3 proposal:** Accept an explicit period id / charge timestamp on `Refund` so
window-boundary reconciles credit the correct bucket rather than the current
period.

## jwtutil: KeySet.Verify fail-closed issuer/audience

**Status:** Documented on v2; fail-closed is v3.

**v2 change (applied):** KeySet.Verify godoc now warns that empty
`ExpectedIssuer` / `ExpectedAudience` skip those checks, and points
callers at Provider (which already panics without an explicit policy).

**v3 proposal:** Make `KeySet.Verify` (and/or `ParseKeySet`) require an
explicit issuer and audience policy — either non-empty Expected* fields
or new `AllowAnyIssuer` / `AllowAnyAudience` flags — failing with a
typed error when both are unset. Mirrors the Provider constructor
guardrail and closes the confused-deputy path for raw KeySet users.

## approval.TenantStore: fold ForTenant into Store interface

**Status:** v2 FIXED for kit backends; optional interface remains for third parties.

**v2 change (applied):** `memory` and `postgres` implement
`ApproveForTenant` / `RejectForTenant` / `MarkExecutedForTenant`.
`TenantStore` type-asserts `tenantScopedMutator` and prefers the atomic
path; check-then-act + post-write tripwire remains only for third-party
`Store` implementations that lack the optional methods. Tests cover the
atomic preference path.

**v3 proposal:** Promote ForTenant methods onto the core `Store` interface
(or replace id-only mutations) so every backend is atomic by construction
and the optional type-assert / fallback path can be deleted.

## data/idempotency: reject forgeable tenant-scoped raw keys

**Status:** Documented on v2 (bare + tenant-wrapped stores must not share a
backend keyspace). Cryptographic unforgeability is v3.

**v2 change (applied):** Package doc on `data/idempotency/tenant` states that
mounting a bare store and a tenant-wrapped store on the same Redis prefix /
Postgres table lets a bare-path caller address tenant slots via the
length-prefixed key format.

**v3 proposal:** Either (a) reject raw keys matching the canonical
`tenant:<len>:…` shape in `idempotency.ValidateKey` while letting backends
accept pre-scoped keys through an internal API, or (b) HMAC/hash the
scoped key so the on-disk form is not forgeable as a raw key.

## data/queue + data/stream: Consumer.Consume returns error

**Status:** Docs corrected on v2 (no in-tree Consumer implementers;
redisqueue/redisstream use their own types). Error-return shape is v3.

**v3 proposal:** Change `Consumer.Consume(ctx, name, handler) error` (nil on
clean ctx cancel, non-nil on terminal backend failure) in lockstep for queue
and stream, matching `MemoryStore.Run` lifecycle conventions.

## data/lock/redislock: extract redlock shared internals

**Status:** v2 FIXED — shared helpers live in
`data/lock/redislock/internal/redsyncutil` (key validation, tryCount,
jittered backoff, contention/lost classification, handle release/extend,
ReleaseAndJoin). Both redislock and redlock consume it; package-level
Option types remain separate (export-compatible).

**v3 proposal (optional polish):** Unify Option types / LockerWithValue
parity for QuorumLocker if callers still need a single option surface.

## messaging: strict unknown schema version by default

**Status:** Documented on v2; fail-closed default is v3.

**v2 change (applied):** `InMemorySchemaRegistry.ValidateMessage` and
validating-handler docs warn that unknown/producer-controlled versions
pass through unless `WithStrictUnknownVersion` is set.

**v3 proposal:** Make strict unknown-version rejection the default, with
an explicit `WithLegacyPassThrough` / `WithLooseUnknownVersion` opt-out.
Also consider rejecting version-0 for types that have any registered
schema.

## httpx/websocket: safe heartbeat and write-timeout defaults

**Status:** Documented on v2; non-zero defaults are v3.

**v2 change (applied):** `defaultConfig` / package docs state that
heartbeat and write timeout are opt-in, and recommend
`WithPingInterval` + `WithWriteTimeout` (+ `WithMaxConnections`) for
untrusted clients. `WithReadDrain` now cancels the per-conn context
promptly on peer disconnect.

**v3 proposal:** Ship non-zero defaults (e.g. 30s write timeout, 30s
ping interval) with explicit `WithNoWriteTimeout` / `WithNoHeartbeat`
opt-outs.

## httpx/websocket: shutdown-linked connection context

**Status:** v2 FIXED via additive `Hub` API (`NewHub`, `Hub.Handler`,
`Hub.Shutdown`). Package-level `Handle` still uses WithoutCancel (no
process-level drain); callers that need graceful WebSocket teardown use Hub.

**v3 proposal:** Optionally fold Hub tracking into `Handle` by default, or
accept a caller-supplied base context that is not stripped by WithoutCancel
so `http.Server` BaseContext cancellation propagates without opting into Hub.

## infra/storage: implement or park optional capability interfaces

**Status:** OPEN on v2 (Tagger/Versioner/MultipartUploader/BatchDeleter
exported without backend implementations; opaque decorators strip them).

**v3 proposal:** Either implement the surfaces in s3backend (and forward
through retry/circuitbreaker/encryption decorators) or unexport/park them
until a backend needs them, with a compile-time guard that every optional
interface is forwarded by each decorator.

## sftpbackend: reference-counted reconnect leases

**Status:** Partial v2 fix applied; ref-counted cleanup remains v3.

**v2 change (applied):**
- `connect` dials SSH outside `b.mu` under a dedicated `dialMu` with
  singleflight + 2s post-failure cooldown so concurrent ops against a dead
  server do not serialise into N×10s lock-held dials.
- `List` with `MaxKeys > 0` retains only the MaxKeys smallest keys after
  `StartAfter` via a max-heap (unbounded when MaxKeys is 0).

**v3 proposal:** Reference-count client leases (`getClient` returns a
release func; reconnect cleanup waits for the count to drain) instead of
the fixed 5s grace / generation-bump early close under flapping.

## redisstream: separate shutdown grace from handlerTimeout

**Status:** v2 FIXED.

**v2 change (applied):** `WithShutdownGrace` (default 30s) independently caps
how long an in-flight handler may continue after parent cancel. Long
`WithHandlerTimeout` values no longer force multi-minute `Consume` return on
shutdown. See `streamHandlerContext`.
