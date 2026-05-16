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
| `core/validate` | `go-playground/validator` | apperror conversion + JSON tag binding. |
| `core/config/watcher` | `fsnotify` | Watchable wrapper for hot reload. |
| `security/jwtutil` | `lestrrat-go/jwx/v3` | JWKS lifecycle + revocation + timing-floor + metrics. |
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

**Decision:** **v2.1.0 candidate.** Low-risk migration; deferred only
because v2.0.0 tagging is the gating constraint.

### 6. `data/ratelimit/gcra` → `github.com/throttled/throttled/v2` (RE-CATEGORIZED AS MIGRATE)

**Maintenance verdict:** throttled v2 is **actively maintained**: last
push April 2026, recent fix titled "Prevent panic on unset rate
limiter" (PR #115). This is the exemplar of the "well-maintained
library has fixed bugs you may also have" failure mode — keeping a
kit-custom GCRA means re-discovering those bugs in production rather
than getting them for free.

**Current scope:** ~650 LOC. Per-key in-memory GCRA.

**OSS alternative:** `throttled/throttled/v2` implements GCRA against a
generic store interface; memory and Redis stores are first-party.

**Migration shape:** replace the kit's GCRA arithmetic with throttled's
`GCRARateLimiter`. The kit's per-key map + sweeper + metrics + redact
stay as the wrapper. Public surface (`gcra.Limiter`) doesn't change.

**Decision:** **v2.1.0 candidate** alongside the tokenbucket swap. Both
follow the same pattern: kit keeps its multi-key + lifecycle layer;
single-key algorithm core comes from upstream.

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
| `httpx/middleware/*` | The middleware stack IS the kit's HTTP value-add. Each middleware is small (auth, tenant, idempotency, rate-limit, recover, timeout). Adopting an external middleware framework would replace the kit's HTTP layer wholesale. |
| `httpx/pagination` | Cursor signer with HMAC fence. Kit-specific contract; signed cursors aren't a packaged OSS pattern. |
| `httpx/authz` | Decider/Logged seam plus per-route middleware. Wraps `authz` + `authz/openfga` which themselves wrap upstream. |
| `httpx/mcp` (post-migration) | Kit's tenant/audit/destructive-gate layer over the SDK. The wrapping IS the value. |
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

1. **Now** (waves 120–123): MCP migration to `modelcontextprotocol/go-sdk`.
2. **Before v2.0.0 tag** (waves 124–126): PASETO migration to
   `aidantwoods/go-paseto`. Removes hand-rolled crypto from the v2 API
   freeze — strongest security-posture argument in the audit.
3. **Before v2.0.0 tag** if scheduling permits (waves 127–128):
   `data/lock/redislock` to `go-redsync/redsync` (NOT bsm/redislock —
   bsm has no real code changes since March 2024). ~half day.
4. **v2.1.0** — high priority: `data/queue/redisqueue` to
   `hibiken/asynq`. 13k stars, pushed today, the strongest "the
   library has already fixed bugs you might have" case in the audit.
5. **v2.1.0**: `data/ratelimit/gcra` to `throttled/v2` and
   `data/ratelimit/tokenbucket` internal wrap with
   `golang.org/x/time/rate`. Both follow the same pattern: kit keeps
   multi-key + lifecycle + metrics; algorithm core comes from upstream.
6. **Open / contingent**: `security/csrf` — revisit if/when a
   maintained OSS Go CSRF library emerges. Gorilla and nosurf both
   fall short of the kit's maintenance bar today.
7. **Keep custom indefinitely**: everything in the 🔴 list, with the
   caveat that `runtime/eventbus` is on the weakest footing of those
   — flag in its package doc what would trigger a revisit.

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
