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

**Status:** APPLIED on v2 as intentional break (review-11 MEDIUM).

**v2 change (applied):** `NewTenantStore` requires `TenantScopedMutator`
(`ApproveForTenant` / `RejectForTenant` / `MarkExecutedForTenant`) and
panics if the inner `Store` lacks those methods. Check-then-act fallback
and post-write tripwire are removed. Kit `memory` and `postgres` backends
already implement the mutator; third-party Stores must implement it to use
`TenantStore`.

## data/idempotency: reject forgeable tenant-scoped raw keys

**Status:** APPLIED.

**v2 change (applied):** Package doc on `data/idempotency/tenant` states that
mounting a bare store and a tenant-wrapped store on the same Redis prefix /
Postgres table lets a bare-path caller address tenant slots via the
length-prefixed key format.

**v3 change (applied):**
- `idempotency.ValidateKey` rejects reserved prefixes `tenant:` and `tns:`
  (`ErrKeyReservedPrefix`).
- `idempotency.ValidateStorageKey` accepts ordinary user keys and well-formed
  opaque tenant storage keys (`tns:` + 64 lowercase hex); backends
  (memory/redis/pg) validate via `ValidateStorageKey`.
- Tenant wrapper always stores `tns:` + hex(sha256(coretenant.KeyFor(...))),
  never the readable length-prefixed form. Storage keys are not human-readable.
- Tests cover forge attempts against a shared bare+wrapped keyspace.

## data/queue + data/stream: Consumer.Consume returns error

**Status:** APPLIED on v2 as intentional break (review-12 LOWs).

**v2 change (applied):** `queue.Consumer.Consume` and `stream.Consumer.Consume`
return `error`. Clean cancel returns `ctx.Err()`; terminal backend failures
return a wrapped non-context error. `redisstream.Consumer.Consume` and
`redisqueue.Queue.Process` implement the contract; `StartConsumers` /
`StartProcessors` and `infra/messaging/redisbackend` propagate permanent
failures to `shutdownFn`. `infra/redis.RunWithBackoff` returns the last
error / `ctx.Err()` so lifecycle runners can observe exits.

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

**Status:** APPLIED on v2 as intentional break (review-09 MEDIUM).

**v2 change (applied):** `defaultConfig` enables fail-safe defaults —
`writeTimeout: 30s` (`DefaultWriteTimeout`), `pingInterval: 30s`
(`DefaultPingInterval`), `pongTimeout: 10s` (`DefaultPongTimeout`).
Explicit opt-outs: `WithNoWriteTimeout()` and `WithNoHeartbeat()`.
`WithWriteTimeout` requires a positive duration (zero only via
`WithNoWriteTimeout`); `WithPingInterval(0)` still disables heartbeat
and clears pong timeout.

## httpx/websocket: shutdown-linked connection context

**Status:** v2 FIXED via additive `Hub` API (`NewHub`, `Hub.Handler`,
`Hub.Shutdown`). Package-level `Handle` still uses WithoutCancel (no
process-level drain); callers that need graceful WebSocket teardown use Hub.

**v3 proposal:** Optionally fold Hub tracking into `Handle` by default, or
accept a caller-supplied base context that is not stripped by WithoutCancel
so `http.Server` BaseContext cancellation propagates without opting into Hub.

## infra/storage: implement or park optional capability interfaces

**Status:** APPLIED.

**v3 change (applied):**
- Removed dead public surfaces with no production backends: `Tagger`,
  `Versioner`, `BatchDeleter` and their `As*` helpers (`tagging.go` /
  `version.go` deleted; batch path simplified).
- `DeleteMany` is always sequential via `Storage.Delete` so hooks and
  decorators always see per-key deletes (no BatchDeleter bypass).
- Kept `MultipartUploader` / `AsMultipartUploader` (and lister sibling) —
  s3backend implements them and retry/circuitbreaker already forward them.
- Hooks / retry / circuitbreaker combinator tables shrunk to four shared
  forwarder types + thin embedding (review-18 LOWs); method bodies live in
  one place per decorator.

## sftpbackend: reference-counted reconnect leases

**Status:** APPLIED.

**v2 change (applied):**
- `connect` dials SSH outside `b.mu` under a dedicated `dialMu` with
  singleflight + 2s post-failure cooldown so concurrent ops against a dead
  server do not serialise into N×10s lock-held dials.
- `List` with `MaxKeys > 0` retains only the MaxKeys smallest keys after
  `StartAfter` via a max-heap (unbounded when MaxKeys is 0).

**v3 change (applied):**
- `clientSession` reference-counts leases; `getClient` returns
  `(Client, release, error)`. Put/Delete/Exists/List `defer release()`;
  Get holds the lease until the body `ReadCloser` is closed
  (`leasedReadCloser`).
- Reconnect installs a new session immediately and retires the old one;
  old SSH/SFTP FDs close only when inflight leases drain (no force-close
  under transfers). Drain grace (30s) logs then continues waiting.
- `Backend.Close` retires the live session, waits up to drain grace, then
  force-closes so a leaked Get body cannot hang shutdown forever.
- Tests: `TestClientLease_HeldAcrossReconnect`,
  `TestClientLease_GetHoldsUntilBodyClose`.

## redisstream: separate shutdown grace from handlerTimeout

**Status:** v2 FIXED.

**v2 change (applied):** `WithShutdownGrace` (default 30s) independently caps
how long an in-flight handler may continue after parent cancel. Long
`WithHandlerTimeout` values no longer force multi-minute `Consume` return on
shutdown. See `streamHandlerContext`.
