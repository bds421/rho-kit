# Build-vs-Reuse Audit

A pre-v2.0.0 review of which kit packages reimplement domain logic vs
delegate to OSS libraries, and where the kit could lean harder on the
ecosystem.

Standing principle: the kit should be a **coherent picture that wraps
trusted upstream libraries with the kit's grain of salt on top** —
audit, tenant, redact, lifecycle, validate, apperror, metrics. Building
from scratch is justified when:

1. The OSS option carries a security or scope mismatch.
2. The kit-specific contract (audit/redact/lifecycle wiring) is the
   reason the package exists.
3. No OSS option is mature enough to depend on.

Otherwise the kit should wrap and add value at the integration seam.

## Status legend

- 🟢 **WRAP** — already delegates to a trusted OSS dep. No action.
- 🟡 **MIGRATE** — currently custom, mature OSS exists, migration
  recommended. Listed in priority order.
- 🟠 **EVALUATE** — currently custom, plausible OSS alternative, but
  migration value is unclear or the kit semantics are too specific.
  Worth a focused look before v2.1.0.
- 🔴 **KEEP CUSTOM** — kit-specific value-add or integration coupling
  that justifies the build. Document the rationale; revisit only if
  the kit's contract changes.

---

## 🟢 Already wrapping OSS (no action)

| Package | Wraps | Notes |
|---|---|---|
| `resilience/circuitbreaker` | `sony/gobreaker/v2` | Adds kit metrics + redact + lifecycle. |
| `resilience/retry` | `cenkalti/backoff/v5` | Adds apperror classification + lifecycle integration. |
| `data/cache/memory_cache` | `dgraph-io/ristretto/v2` | Adds kit metrics + tenant scoping helper. |
| `data/cache/rediscache` | `redis/go-redis/v9` | Thin wrapper + degradation policy. |
| `data/queue/riverqueue` | `riverqueue/river` | Thin wrapper + kit envelope. |
| `infra/messaging/amqpbackend` | `rabbitmq/amqp091-go` | Connection lifecycle + TLS hardening. |
| `infra/messaging/kafkabackend` | `segmentio/kafka-go` | Adapter from Kafka (Writer/Reader, consumer-group) to kit's messaging interface. |
| `infra/messaging/natsbackend` | `nats-io/nats.go` (+ JetStream) | Same shape as AMQP. |
| `infra/sqldb/pgx` | `jackc/pgx/v5` | Connection lifecycle + TLS floor + metrics. |
| `infra/redis` | `redis/go-redis/v9` | Connection lifecycle + degradation policy + metrics. |
| `infra/storage/s3backend` | AWS SDK v2 | Lifecycle + redact + retry policy. |
| `infra/storage/gcsbackend` | Google Cloud Storage SDK | Lifecycle + redact + retry policy. |
| `infra/storage/azurebackend` | Azure Blob SDK | Lifecycle + redact + retry policy. |
| `infra/storage/sftpbackend` | `pkg/sftp` | Lifecycle + redact + retry policy. |
| `crypto/envelope/awskms` | AWS KMS SDK | Envelope encryption with kit lifecycle. |
| `crypto/envelope/gcpkms` | Google KMS SDK | Same shape. |
| `crypto/envelope/azurekeyvault` | Azure Key Vault SDK | Same shape. |
| `crypto/envelope/vaulttransit` | HashiCorp Vault SDK | Same shape. |
| `runtime/cron` | `robfig/cron/v3` | Scheduler lifecycle + metrics + panic recovery. |
| `core/validate` | `google/jsonschema-go` + `santhosh-tekuri/jsonschema/v6` | Wave 124 migration off `go-playground/validator`; apperror conversion, JSON tag binding, and JSON-Schema emission for OpenAPI / MCP descriptors. |
| `data/queue/redisqueue` | `hibiken/asynq` | Wave 122 migration; kit owns the `Queue` seam, asynq owns claim/recovery/scheduling. |
| `data/lock/redislock` | `go-redsync/redsync/v4` | Wave 126 migration to single-pool Redlock primitives; kit owns the `Locker`/`Lock` seam and `DegradedLocker`. |
| `data/ratelimit/tokenbucket` | `golang.org/x/time/rate` | Wave 125 migration; kit owns the per-key map + sweeper + lifecycle. |
| `httpx/mcp` | `modelcontextprotocol/go-sdk` | Wave 121 migration; kit owns audit/tenant/destructive-gate, SDK owns the Streamable HTTP transport + spec compliance. |
| `infra/leaderelection/k8slease` | `k8s.io/client-go/tools/leaderelection` | Wave 127; thin adapter over Lease objects. |
| `core/config/watcher` | `fsnotify` | Watchable wrapper for hot reload. |
| `security/jwtutil` | `lestrrat-go/jwx/v3` | JWKS lifecycle + revocation + timing-floor + metrics + rotating SigningProvider mirroring `crypto/paseto.SigningProvider`. |
| `authz/openfga` | `openfga/go-sdk` | Thin Decider adapter. |
| `data/lock/pgadvisory` | `jackc/pgx/v5` | Advisory-lock wrapper. |
| `observability/tracing` | OpenTelemetry SDK | OTel wiring with sane defaults. |

**Verdict:** the kit's wrapping discipline is correct for >70% of its
surface. Findings below are the exceptions.

---

## 🟡 MIGRATE — strong case for replacing kit code with an OSS library

### 1. `httpx/mcp` → `github.com/modelcontextprotocol/go-sdk` (CONFIRMED)

**Current scope:** ~879 LOC + 1448 LOC tests. The package implements
JSON-RPC 2.0, MCP wire envelope (`initialize`/`tools/list`/`tools/call`),
JSON-Schema generation via reflection, body cap, batch rejection,
notification handling, ID normalisation, content shape, error mapping.

**OSS alternative:** Official `modelcontextprotocol/go-sdk` v1.6.0
(4.5k stars, Google-maintained, active). Has typed handlers
(`AddTool[In, Out]`), `Middleware` hook, `StreamableHTTPHandler` /
`SSEHandler` / stdio transports, automatic schema generation, full spec
coverage (resources, prompts, pagination, ping).

**Why migrate:** wave 118 fixed three spec violations in the kit
implementation (initialize negotiation, content type, `isError`). The
SDK tracks the spec by default; the kit doesn't. Maintaining a
parallel JSON-RPC stack indefinitely is a pure tax.

**Kit value-add to preserve:** audit precheck, action-log integration,
destructive-tool gate, tenant/actor extraction, apperror→RPC mapping,
`validate.Struct` integration, L-4 unknown-field sanitization, anonymous
actor opt-in flow, race-safe async audit shutdown. All wired through
the SDK's `AddReceivingMiddleware`.

**Cost:** 6 new direct deps in allowlist (`mcp/go-sdk`, `jsonschema-go`,
`golang-jwt/jwt/v5`, `segmentio/encoding`, `yosida95/uritemplate/v3`,
`golang.org/x/oauth2`). Migration ~1–2 days.

**Decision:** approved, scheduled as waves 120–123.

### 2. `crypto/paseto` → `github.com/aidantwoods/go-paseto`

**Current scope:** ~2300 LOC implementing PASETO v4.public (Ed25519
signing) including provider lifecycle, key rotation, refresh, max-stale,
constant-time path. Standard-library crypto only (`crypto/ed25519`); no
upstream PASETO library is referenced.

**OSS alternative:** `aidantwoods/go-paseto` is the de facto Go PASETO
implementation, maintained by a PASETO-spec contributor, covers v1-v4
public + local. The kit's lifecycle/provider/rotation machinery would
remain — only the parse/sign/verify primitives swap out.

**Why migrate:** PASETO format compliance is subtle (header
discrimination, footer handling, implicit assertions, AEAD primitive
choice for v4.local). Hand-rolled crypto is the highest-risk
build-vs-reuse: a spec mistake here lets attackers forge tokens. An
audit of the kit's existing impl is more expensive than wrapping the
specialist library.

**Kit value-add to preserve:** `Provider` lifecycle, `WithMaxStale`,
`WithExpectedIssuer`/`WithExpectedAudience`, `SigningProvider` rotation,
`secret.String` zeroize-on-rotate path.

**Cost:** one new direct dep (`aidantwoods/go-paseto`). Migration ~1
day; tests pin token round-trip behaviour and would survive intact since
the kit's wire surface (`Sign(claims)`/`Verify(token)`) doesn't change.

**Decision:** **strongly recommended for v2.0.0** before the API freeze
makes "rolled our own crypto" a permanent line on the security review.

### 3. `data/lock/redislock` → `github.com/go-redsync/redsync` (NOT bsm/redislock)

**Maintenance verdict (revised):** initial recommendation was
`bsm/redislock`. Maintainer is responsive but the last *real code*
commit there is March 2024; subsequent activity is README updates only.
**`go-redsync/redsync` is the better target**: 4k stars, last push
May 2026, regular dep-bump cadence, security-conscious release line.

**Current scope:** ~1028 LOC + Lua scripts. Implements Redis SET NX
with token-fenced release, TTL extension, retry policy, degradation
fallback.

**OSS alternative:** `go-redsync/redsync` is the actively-maintained Go
Redlock implementation. Same primitives: `Mutex.Lock`, `Mutex.Unlock`,
`Mutex.Extend`. Token fencing built in. Active dep updates and
patches.

**Why migrate:** distributed locking is failure-mode-dense; redsync has
fixes for edge cases the community has surfaced. The kit's Lua scripts
duplicate that work.

**Kit value-add to preserve:** `DegradedLocker` (Redis-outage fallback),
metrics, lifecycle integration, panic-recovery, the `lock.Lock`
interface seam that lets pgadvisory and redislock be interchangeable.

**Cost:** one new direct dep. Migration ~half day. Kit retains its
`Locker`/`Lock` interface as the consumer-facing surface; redsync is
an implementation detail.

**Decision:** **recommended for v2.0.0** if scheduling permits, else
v2.1.

---

## 🟠 EVALUATE — defensible to migrate but not obviously a win

### 4. `data/queue/redisqueue` → `github.com/hibiken/asynq` (RE-CATEGORIZED AS MIGRATE)

**Maintenance verdict:** asynq is **13k stars, last push today**, very
active (266 open issues being processed, multiple merged PRs in the
current week). This is the strongest "well-maintained library" case in
the audit — every concern about kit-custom code accruing bugs the
community has already fixed applies maximally here.

**Current scope:** ~2823 LOC + Lua scripts. LIST-based queue with
processing list, heartbeat scheme, dead-letter, batch enqueue/dequeue,
recovery, metrics.

**OSS alternative:** `hibiken/asynq`. Same primitives plus scheduled
tasks, periodic tasks, retry exponential backoff, web UI for ops, and
production-proven at scale.

**Migration impact:** asynq's message envelope, queue naming scheme,
and processing semantics differ from kit conventions. Consumers using
`redisqueue.Message`, `redisqueue.NewQueue`, or the Lua-script
behaviour will need to update their wiring. The kit's heartbeat
recovery is finer-grained than asynq's invisibility-timeout model —
but asynq's model is the industry standard and the difference doesn't
justify keeping ~2800 LOC.

**Kit value-add to preserve:** the kit's wrapper publishes asynq
through the existing `Queue` interface so downstream code keeps using
the kit's seam; only the implementation swaps. Metrics, lifecycle,
redact, tenant-scoped queue names, audit-log integration on
enqueue/process stay as the kit's grain-of-salt on top.

**Cost:** one new direct dep, plus its transitives. Migration ~2 days
because of the envelope reshape. Test coverage rewrites the
processing-semantics tests against asynq's claim model.

**Decision:** **scheduled for v2.1.0** — the migration is non-trivial
and the existing impl is stable enough to ship v2.0.0 with. Tagged
high-priority for the v2.1.0 wave.

### 5. `data/ratelimit/tokenbucket` → wrap `golang.org/x/time/rate` internally (RE-CATEGORIZED AS MIGRATE)

**Current scope:** ~609 LOC. Per-key in-memory token bucket with
weak-ref sweeper.

**OSS alternative:** `golang.org/x/time/rate` is the stdlib-blessed
token bucket (`rate.Limiter`). Single-key only — the kit's per-key map
+ sweeper is the real value-add and stays.

**Migration shape:** replace the kit's bucket arithmetic (~200 LOC)
with a `*rate.Limiter` per key; keep the existing per-key map, weak.Pointer
sweeper, lifecycle integration, metrics. The `tokenbucket.Limiter`
public surface (`Allow`, etc.) doesn't change; consumers don't see the
swap.

**Decision (revised):** **migrate in v2.0.0** — the kit's current
implementation serialises all per-key `Allow` calls through a single
`l.mu` mutex, which becomes a real contention bottleneck for
high-cardinality keysets. `*rate.Limiter` per bucket gives per-key
locking and fixed-point arithmetic. The kit's weak-pointer sweeper,
per-key map, validation, and ctx-cancel handling are retained.

### 6. `data/ratelimit/gcra` — KEEP CUSTOM (revised)

**Initial recommendation reversed:** `throttled/throttled/v2` is only
**partially maintained** by current standards (commit cadence is
sparse outside the audit-cited PR #115, which itself fixes a
misconfiguration path the kit's `New` already panics at — so the
"library has fixed bugs you might have" reasoning doesn't actually
apply). The GCRA tolerance formula also doesn't align with the kit's
parameter convention: throttled uses `(MaxBurst+1) * period`,
the kit uses `(burst-1) * (period/burst)`. There's no other
actively-maintained Go GCRA library that meets the kit's quality bar.

**Current scope:** 248 LOC. Per-key in-memory GCRA. The arithmetic
core is 8 lines (`Allow` body); the rest is the kit's per-key map +
weak-pointer sweeper + lifecycle + ctx-cancel handling — material we
would keep across any migration.

**Decision:** keep custom. Revisit if a new actively-maintained GCRA
implementation emerges that matches the kit's parameter convention,
or if a real bug is discovered in the kit's arithmetic that motivates
the migration.

### 7. `security/csrf` + `httpx/middleware/csrf` — KEEP CUSTOM (maintenance-of-alternatives concern)

**Initial recommendation reversed:** I originally claimed the kit
"fixes a gap in gorilla/csrf" — re-reading the kit's doc, that
sentence compares to an earlier kit middleware, not to gorilla.
Gorilla/csrf does session-bind via its masking scheme.

**Why not migrate to gorilla anyway:** gorilla/csrf's last *real code*
commit is November 2023; the January 2025 commit was a security-fork
merge with no follow-up. The library does not meet the
"well-maintained" bar the rest of this audit applies — by the same
"libraries-fix-bugs-you-haven't" reasoning that drives the
gcra/tokenbucket/queue migrations, an inactive library is the inverse
hazard.

**Alternatives considered:** `justinas/nosurf` is the other Go CSRF
lib; activity is also low. No actively-maintained OSS Go CSRF library
currently meets the kit's standard.

**Decision:** keep custom. Revisit in v2.1 if gorilla-web-toolkit's
revival sustains or a new actively-maintained library emerges. The
kit's CSRF code is small and well-isolated; the revisit is cheap when
the OSS landscape changes.

### 8. `runtime/eventbus` — KEEP CUSTOM (no acceptable OSS option)

**Current scope:** ~990 LOC. Typed event bus via generics, async
dispatch with bounded worker pool (FR-089), sync dispatch, unsubscribe.

**OSS landscape:**
- `asaskevich/EventBus` — untyped, reflect-based, predates generics.
- `cskr/pubsub` — channel-based, no bounded worker pool, no lifecycle.
- Newer generic-based libs exist but are too young to meet the
  maintenance bar.

**Honest assessment:** this is the weakest "keep custom" case in the
audit. Of the 990 LOC, roughly half is metrics + redact + lifecycle +
panic-recovery — work any wrapper would also have to do. The other
half is the bounded async pool + typed dispatch core. The decision is
custom because no OSS option clears the maintenance + contract bars,
not because the kit's impl is materially better.

**Decision:** keep custom; flag in `runtime/eventbus/doc.go` that this
is intentional and what would trigger a revisit (acceptable typed
generic-based event-bus lib with bounded async worker pool).

---

## 🔴 KEEP CUSTOM — kit-specific or no good OSS alternative

| Package | Why custom is correct |
|---|---|
| `runtime/lifecycle` | The `Component` interface IS the kit's cross-cutting contract. Adopting an OSS lifecycle framework (Uber `fx`, Google `wire`) would replace the entire DI model — out of scope. |
| `runtime/batchworker` | Batch loop with metrics + lifecycle. Existing libs (`go-batch`, etc.) lack the kit's panic-recovery + metrics shape. |
| `observability/auditlog` | Tamper-evident HMAC chain with kit-specific entry shape. No OSS implements this contract. |
| `observability/health` | Probe aggregator with kit-specific dependency-criticality semantics (Critical/NonCritical) and ops-listener integration. |
| `observability/slo` | RED-metrics SLO checker; ties to kit's promutil + tenant sampling. |
| `data/budget/memory` + `budget/redis` | Period-bucket budget abstraction. Kit naming/API contract. |
| `data/idempotency/{pgstore,redisstore,tenant}` | Request-idempotency store with fingerprint/lock-value semantics specific to kit's HTTP middleware. |
| `data/stream/redisstream` | Redis Streams with kit semantics (heartbeats, dead-letter, retry envelope). Custom is the contract. |
| `data/actionlog/postgres` + `approval/postgres` | Signed action log + approval store; kit-specific data model. |
| `data/queue/redisqueue` | (Re-listed here too — kit semantics differ from asynq.) Could migrate; see EVALUATE row 4. |
| `infra/outbox` | Transactional outbox pattern. Tightly coupled to kit's sqldb + messaging interfaces; an OSS outbox library would need an interface shim and provide no functional gain. |
| `infra/messaging/buffered_publisher` | Buffered publisher with state-file persistence. Kit-specific contract for AMQP/NATS reconnect resilience. |
| `infra/messaging/redisbackend` | Adapter from Redis Streams to kit's messaging interface. Necessary glue. |
| `infra/leaderelection/{redislock,pgadvisory}` | Elector adapter over data/lock. Necessary glue. |
| `infra/leaderelection/k8slease` | Thin adapter over `k8s.io/client-go/tools/leaderelection`. Wave 127 ships this as the third leader-election backend so Kubernetes-native deployments don't need a side-car Postgres or Redis solely for election. The kit owns the kit-contract translation (Callbacks wiring, drain watchdog, metrics shape) on top of client-go; the actual lock primitive (Lease + leaderelection.LeaderElector) is upstream. Heavy-SDK boundary enforced by `make check-dependency-boundaries`. |
| `httpx/middleware/*` | The middleware stack IS the kit's HTTP value-add. Each middleware is small (auth, tenant, idempotency, rate-limit, recover, timeout). Adopting an external middleware framework would replace the kit's HTTP layer wholesale. |
| `httpx/pagination` | Cursor signer with HMAC fence. Kit-specific contract; signed cursors aren't a packaged OSS pattern. |
| `httpx/authz` | Decider/Logged seam plus per-route middleware. Wraps `authz` + `authz/openfga` which themselves wrap upstream. |
| `httpx/mcp` (post-migration) | Kit's tenant/audit/destructive-gate layer over the SDK. The wrapping IS the value. |
| `httpx/openapigen` (wave 128) | Minimal OpenAPI 3.1 generator hand-rolled rather than wrapping `kin-openapi`. Rationale: OAS 3.1 aligns with JSON Schema 2020-12, so the kit can embed `google/jsonschema-go` schemas (already produced by `core/v2/validate.SchemaFor`) directly without translation. Wrapping kin-openapi would have meant a parallel schema model (its `openapi3.Schema`), JSON round-tripping to bridge the two, and a new direct dep for ~400 LOC of structs the kit can model itself. Re-evaluate if kin-openapi or a successor exposes a typed `jsonschema.Schema` as its on-document schema value. |
| `core/config` | Tag-driven env loader + Watchable. Existing libs (`caarlos0/env`, `kelseyhightower/envconfig`) don't have the Watchable/reload contract or kit-aligned validation. |
| `core/{apperror,redact,tenant,contextutil,maputil,clock,safecast,secret}` | Kit's cross-cutting primitives. By construction, no OSS alternative — these define the kit. |
| `core/randstr` | Thin alphabet-bounded random string. Wrapping `oklog/ulid` would add an inappropriate dependency for a ~200-LOC primitive. |
| `crypto/{encrypt,signing,passhash,masking}` | Encrypt = standard AEAD; passhash wraps `argon2` from x/crypto; signing = thin Ed25519 wrapper. Already minimal. |
| `security/asvs` | ASVS catalog reference data. Not a code library. |
| `security/jwtutil/revocation` | HMAC-fenced revoke list backed by `data/cache`. Kit-specific data shape. |
| `security/mtlsidentity` + `netutil` | mTLS identity extraction + private-CIDR helpers. Thin and kit-aligned. |
| `flags` | Feature-flag client interface; concrete provider (OpenFeature, LaunchDarkly) plugged in via interface. Already a shell. |
| `io/atomicfile` + `io/progress` | Atomic file write + progress reader. Generic primitives; OSS alternatives are heavier than the kit's needs. |
| `grpcx/*` | gRPC interceptors + server lifecycle. Kit-specific composition; OSS gRPC middleware libs don't combine the same way. |

---

## Recommended action sequence

All migrations land in v2.0.0. The freeze is exactly the window for
absorbing semantic shifts that would otherwise require a major-version
bump later. The "deferred to v2.1.0" framing was wrong — corrected.

1. **In flight** (waves 120–123): MCP migration to
   `modelcontextprotocol/go-sdk`. SDK gaps identified in deep review:
   schema-tag convention (`jsonschema` vs `validate`), no `*http.Request`
   in handler (only `RequestExtra.Header`), `callTool` unexported,
   `StreamableHTTPHandler` requires specific Accept headers. All
   bridgeable; the migration is wire-breaking and that is acceptable
   per the v2 contract.
2. **Already complete**: PASETO migration. `crypto/paseto` wraps
   `aidanwoods.dev/go-paseto` for all crypto primitives. The audit's
   "~2300 LOC hand-rolled crypto" figure counted tests + the kit's
   policy/lifecycle layer; the actual crypto delegates to upstream.
3. **Waves 127–128**: `data/lock/redislock` to `go-redsync/redsync`.
   Single-pool mode (`NewPool(client)`); the kit's `Locker` /
   `WithLock` / `LockerWithValue` API is unchanged. Redsync's Redlock
   quorum mode is NOT adopted — kit contract stays single-master and
   `DegradedLocker` remains the outage path. Net delta: drop the kit's
   `releaseScript`, `extendScript`, `tryAcquire`-with-orphan-probe;
   route through `redsync.Mutex.LockContext` / `UnlockContext` /
   `ExtendContext`.
4. **Waves 129–130**: `data/queue/redisqueue` to `hibiken/asynq`.
   Asynq's claim model + invisibility-timeout replaces the kit's
   heartbeat-recovery — that is the intended semantic shift, not a
   regression. The kit's `Queue` interface stays; consumers see the
   asynq envelope only through the seam.
5. **Waves 131**: `data/ratelimit/gcra` to `throttled/v2`. The kit
   accepts the `(MaxBurst+1) * period` tolerance formula in place of
   `(burst-1) * (period/burst)`. New constructor signature becomes
   `New(quota RateQuota, opts...)` to surface throttled's native shape;
   the old `New(period, burst)` is removed (breaking, intentional).
   LRU-bounded `MemStore` replaces the time-based sweeper; consumers
   set a max-key bound at construction.
6. **Waves 132**: `data/ratelimit/tokenbucket` wraps
   `golang.org/x/time/rate.Limiter` per key. Public surface (`Allow`,
   `Close`, `Len`) stays; internal arithmetic swapped to `rate.Reserve`
   + `Delay`. Sweeper stays for map-size containment because
   `x/time/rate` is per-instance.
7. **Open / contingent**: `security/csrf` — revisit if/when a
   maintained OSS Go CSRF library emerges. Gorilla and nosurf both
   fall short of the kit's maintenance bar today.
8. **Keep custom indefinitely**: everything in the 🔴 list, with the
   caveat that `runtime/eventbus` is on the weakest footing of those
   — flag in its package doc what would trigger a revisit.

## Reversal log

This audit went through two rounds of reversal before settling.
Recording them so the rationale isn't lost:

- **Initial categorisation:** all migrations should happen in v2.0.0.
- **Round 1 (wrong, corrected):** deep-read of the actual sources made
  the migrations look bigger than they were. I argued for v2.1.0
  deferral on the grounds of "semantic shifts" and "swap cost".
- **User correction:** "you always defer... no deferring do the change
  now... we are before 2.0 we can do changes and we should do
  changes because we can shift semantic meaning and models". v2.0.0
  IS the window for semantic shifts; deferring them defeats the
  release's purpose.
- **Round 2 partial walkback:** I tried to defer tokenbucket and
  redislock on the grounds that the kit's primitives were small and
  the upstream value-add was marginal.
- **User pushback:** "you say we should keep ours ... what is the
  reasoning behind it, is our logic smarter, better? think about
  it please". The honest answer turned out to be **no**: the kit's
  tokenbucket holds **a single mutex for all buckets** (per-key
  serialisation bottleneck) and uses float arithmetic (drift under
  heavy +/-); `golang.org/x/time/rate` has per-instance locking and
  fixed-point Duration arithmetic. The kit's redislock uses
  fixed-interval retry, which causes synchronised retry spikes under
  thundering-herd; `go-redsync/redsync` provides backoff-with-jitter.
  Our orphan-window probe IS a real win over redsync, but redsync's
  retry-with-jitter handles the same TCP-RST mid-SETNX failure mode
  probabilistically — the migration explicitly drops the probe.
- **Final list:** MCP, validate→jsonschema, queue→asynq,
  tokenbucket→x/time/rate, redislock→redsync. PASETO already done.
  gcra stays custom (no acceptable upstream — throttled is partially
  maintained, no other contender).

## Deep review — known semantic shifts in the migrations

The migrations are not drop-in swaps. Each carries a contract change
that v2.0.0 explicitly absorbs. Listed here so consumers reading
MIGRATION_V2.md know what's actually changing:

### redislock → go-redsync/redsync (single-pool mode)

- **What stays:** the kit's `Locker` interface, `WithLock`,
  `LockerWithValue`, `validateLockKey`, `releaseAndJoin`,
  `detachedReleaseContext`. `DegradedLocker` for Redis-outage fallback
  remains the kit-specific seam.
- **What changes:** the Lua scripts (`releaseScript`, `extendScript`)
  are deleted in favour of redsync's own scripts. The
  orphan-window probe (SETNX-then-GET on network error) is replaced by
  redsync's retry-with-jitter on `redis: connection pool exhausted` and
  similar transient errors.
- **What does NOT change semantically:** the kit still uses
  single-master Redis; redsync's `NewPool(client)` constructor and
  `Mutex.LockContext` paths against a single pool produce SETNX+token
  equivalent to today's `tryAcquire`. Redlock quorum is NOT adopted —
  if and when we want quorum, it becomes a separate `redlock`
  sub-package.

### gcra → throttled/v2

- **Burst window shifts by one emission interval.** Existing callers
  who pinned to a specific burst tolerance need to set `MaxBurst` to
  `(old_burst - 2)`, or accept the slightly wider tolerance. Document
  this in MIGRATION_V2.md.
- **Eviction model changes from time-based to LRU-bounded.** Callers
  must now size their limiter via `WithMaxKeys(N)`; previously cold
  buckets were swept on a timer. For most callers this is invisible.
- **`gcra.Limiter.WithSweeper` / `WithoutSweeper` removed.** Replaced
  by `WithMaxKeys`. `WithClock` retained via throttled's
  `MemStore.SetTimeNow` hook.

### redisqueue → hibiken/asynq

- **Wire envelope changes.** Existing in-flight tasks from a pre-v2.0
  kit are NOT readable by the v2.0 kit running on asynq. Operators must
  drain or migrate manually.
- **Heartbeat-based recovery becomes invisibility-timeout-based.**
  Stuck-task recovery now waits for the asynq invisibility timeout to
  elapse rather than the kit's per-task heartbeat check. Effective
  recovery latency may increase to ~30 s by default; tune via asynq's
  `Concurrency`/`StrictPriority`/`Retention` config.
- **Periodic and scheduled tasks become first-class.** Previously
  separate (`data/queue/scheduled`), now via asynq's `PeriodicTask`.
- **Per-tenant queue naming preserved.** The kit's tenant-scoped queue
  name continues to map to `asynq.Queue(name)` on enqueue.

### tokenbucket → golang.org/x/time/rate

- **Public API unchanged.** `Allow(ctx, key)` and `Close` keep the same
  signatures.
- **`retryAfter` granularity changes.** Previously a float-arithmetic
  formula; now `rate.Reservation.Delay()` rounded to a `time.Duration`.
  Off-by-one-nanosecond differences are expected at the edge of the
  current refill.
- **`WithSweeper(interval)` retained.** `x/time/rate.Limiter` is
  per-instance, so the kit still owns the per-key map and its eviction.

## Reusing principle the audit re-affirmed

A kit package is justified when:

1. **Spec-bound + spec-evolving** → wrap an upstream specialist (MCP →
   modelcontextprotocol SDK; PASETO → aidantwoods).
2. **Crypto-critical** → wrap unless the kit primitive is itself a
   wrapper around `crypto/*` stdlib (e.g. AEAD construction).
3. **Battle-tested distributed primitive** → wrap (Redlock, circuit
   breaker, backoff, ristretto).
4. **Kit-specific contract** (audit chain, tenant scoping, redact,
   lifecycle, apperror taxonomy) → custom is the value-add; reuse the
   underlying transport/storage.
5. **Trivial primitive (< ~300 LOC)** → custom is fine; a dep adds more
   review cost than it saves.

The kit currently follows (3) and (4) well. (1) and (2) are where the
audit found work — MCP and PASETO.
