# rho-kit threat model — v2.0.0

> **Status:** living document. Update this file in lockstep with any
> change to a public security-relevant interface. The accompanying
> [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md) covers dependency, signing and
> provenance threats; this file covers the kit's runtime attack
> surface.

Snippet status: code blocks in this threat model are illustrative attack or
mitigation fragments unless a section explicitly labels a command as runnable.

This document is the canonical answer to the question "what attacks
does rho-kit defend against, and how?" It is intended for two
audiences:

1. **Service authors** building on the kit — to know which guardrails
   are on by default, which require an explicit opt-in, and which
   threats the kit *deliberately* leaves to the deployment
   environment.
2. **External auditors and customers** — to assess whether the kit's
   stance matches the risk profile of the services they consume.

The document is not a marketing piece. Every claim cites the package
and entry point that implements it. Every "out of scope" item below
is something that has been considered and explicitly deferred — not
something that has been overlooked.

---

## Table of contents

1. [Asset taxonomy](#1-asset-taxonomy)
2. [Adversaries](#2-adversaries)
3. [Trust boundaries (overview)](#3-trust-boundaries-overview)
4. [Attack surface per kit area](#4-attack-surface-per-kit-area)
   - 4.1 [HTTP entrypoint (`httpx`, `httpx/middleware`)](#41-http-entrypoint-httpx-httpxmiddleware)
   - 4.2 [gRPC entrypoint (`grpcx`)](#42-grpc-entrypoint-grpcx)
   - 4.3 [Message broker (`infra/messaging`)](#43-message-broker-inframessaging)
   - 4.4 [Relational database (`infra/sqldb`)](#44-relational-database-infrasqldb)
   - 4.5 [Redis (`infra/redis`, `data/*`)](#45-redis-infraredis-data)
   - 4.6 [Object/file storage (`infra/storage`, `infra/storage/storagehttp`)](#46-objectfile-storage-infrastorage-infrastoragestoragehttp)
   - 4.7 [JWT / PASETO verification (`security/jwtutil`, `crypto/paseto`)](#47-jwt--paseto-verification-securityjwtutil-cryptopaseto)
   - 4.8 [Signed-request middleware (`httpx/middleware/signedrequest`)](#48-signed-request-middleware-httpxmiddlewaresignedrequest)
   - 4.9 [Idempotency replay defence (`data/idempotency`, `httpx/middleware/idempotency`)](#49-idempotency-replay-defence-dataidempotency-httpxmiddlewareidempotency)
   - 4.10 [LLM-cost / runaway-work exhaustion](#410-llm-cost--runaway-work-exhaustion)
   - 4.11 [Outbox + transactional integrity (`infra/outbox`)](#411-outbox--transactional-integrity-infraoutbox)
   - 4.12 [Internal observability port](#412-internal-observability-port)
5. [Cross-cutting controls](#5-cross-cutting-controls)
6. [Request-flow walk-throughs](#6-request-flow-walk-throughs)
   - 6.1 [Authenticated user POST (idempotent + audited)](#61-authenticated-user-post-idempotent--audited)
   - 6.2 [Webhook receive (signed-request)](#62-webhook-receive-signed-request)
   - 6.3 [Multi-tenant cache read](#63-multi-tenant-cache-read)
   - 6.4 [Outbox-mediated downstream publish](#64-outbox-mediated-downstream-publish)
7. [Out of scope (deliberately)](#7-out-of-scope-deliberately)
8. [Known mitigation gaps](#8-known-mitigation-gaps)
9. [Process for filing new threats](#9-process-for-filing-new-threats)
10. [Appendix: STRIDE coverage matrix](#10-appendix-stride-coverage-matrix)
11. [Appendix: revision history](#11-appendix-revision-history)

---

## 1. Asset taxonomy

Every threat in §4 is framed in terms of one or more of the assets
below. The list is ordered by sensitivity — disclosure of `[1]`
is a single-tenant breach; disclosure of `[5]` is catastrophic.

| # | Asset | Stored where | Lifetime | Sensitivity |
|---|---|---|---|---|
| 1 | **End-user data** (records the service holds on behalf of users — PII, content, files) | Postgres / MariaDB; object storage; Redis cache | Indefinite | Per-tenant |
| 2 | **Tenant boundaries** — the invariant that tenant A's request never reads / writes tenant B's data | In-process context (`core/tenant`); enforced at every storage boundary | Per request | Multi-tenant integrity |
| 3 | **Authentication tokens** — JWTs / PASETOs in flight, OAuth client secrets, refresh tokens | Headers / cookies; never persisted by the kit | Token TTL | Per-user |
| 4 | **Session secrets** — CSRF HMAC keys, signed-request HMAC keys, idempotency owner tokens | `core/secret`-wrapped config; never logged | Process / config rotation | Per-deployment |
| 5 | **Long-lived signing & encryption keys** — JWKS private keys, envelope KEKs, audit-log HMAC keys | KMS / Vault / mounted secret files; the kit only sees handles or short-lived material | Months → years | Catastrophic if leaked |
| 6 | **Audit trail** — `observability/auditlog` records: who did what to which tenant's data and when | Append-only DB with HMAC chain | Retention policy (default 90d) | Compliance / forensic |
| 7 | **LLM / external API cost budgets** — per-tenant or global spend caps for paid backends | Application-level — kit provides primitives, services own the policy | Hour / day / month | Operational / financial |
| 8 | **Operational state** — message broker offsets, outbox claims, leader-election leases, idempotency keys, distributed locks | Broker / DB / Redis | Bounded by feature | Liveness |

The kit's job is to enforce that each asset can only be touched by
code paths the service author opted into, and that those code paths
fail loudly when misconfigured.

---

## 2. Adversaries

Threats throughout this document are attributed to one of the
following adversaries. Each has a stated capability set; nothing is
assumed beyond it.

### A1. External attacker

- Anonymous actor on the public internet.
- Capability: send arbitrary HTTP / gRPC requests to the service's
  exposed ports; upload files; exploit any unauthenticated endpoint.
- **Cannot** read internal traffic, internal cluster ports, or DB
  network.
- Primary defences: middleware on the public listener, JWT/PASETO
  verification, signed-request middleware on machine-to-machine
  endpoints, signed cursors on pagination
  (`httpx/pagination`, `observability/auditlog`).

### A2. Malicious tenant

- Has a valid account in a multi-tenant deployment of the service.
- Capability: A1 plus the ability to authenticate as themselves and
  hold a valid auth token.
- Goal: read/modify another tenant's data, escalate to admin
  privileges within their tenant, exhaust shared infrastructure
  (cache key namespace collisions, rate-limit avoidance).
- Primary defences: `core/tenant` propagation, tenant-scoped Redis
  keys, mandatory `tenant.Required` at storage boundaries,
  multi-tenant CSRF binding, RBAC via `httpx/authz`.

### A3. Compromised dependency

- A direct or transitive Go-module dependency ships malicious code
  or an exploitable bug.
- Capability: arbitrary code execution within the service process,
  possibly on package init.
- Primary defences (kit side): exact direct-dependency source
  allowlist in CI, `govulncheck`, CycloneDX SBOM published per release,
  dependency pinning policy
  (see [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md)).
- Note: the kit cannot fully prevent this — it can only narrow the
  blast radius and shorten time-to-detect.

### A4. Malicious agent input (LLM-driven)

- A user (legitimate or hostile) provides input that flows into an
  LLM prompt and the LLM emits output that re-enters the
  service's tool-calling layer.
- Capability: A1 (or A2 if authenticated) plus an indirection
  through the model — text the attacker controls becomes
  parameter values to internal functions.
- Goal: cause the service to take an action outside the user's
  authority (e.g., delete records belonging to other users via a
  "tool" the agent can call, exfiltrate secrets via prompt
  injection, exhaust paid-API budgets).
- Primary defences (kit side): tenant scoping (§4.5, §5), explicit
  cost-budget primitives the service must wire (§4.10),
  `core/secret` to keep credentials out of LLM-visible context.

### A5. Insider with code-write access

- Has legitimate commit access to a service repo or to the kit
  itself.
- Capability: introduce new code, change config, downgrade
  defaults.
- Primary defences: branch protection + required reviews
  (deployment-side), the always-on `app.Builder` production-safety
  validator, refuse-to-misconfigure invariants (§5), audit trail
  (`observability/auditlog`).

### A6. Leaked HMAC / JWT secret

- Adversary obtains a copy of a CSRF secret, signed-request HMAC
  key, audit-log HMAC key, or JWT signing key (e.g., via container
  image leak, log scraping, compromised CI runner).
- Capability: forge tokens, signed requests, audit-trail entries,
  or session-bound CSRF tokens.
- Primary defences: `core/secret` to make accidental leakage
  visible; key rotation hooks (`crypto/envelope` for KEK rotation,
  JWKS rotation in `security/jwtutil`); incident response procedure
  in [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md) §"Vulnerability response".

---

## 3. Trust boundaries (overview)

```
                        ┌──────────────────────────────────────┐
                        │          PUBLIC INTERNET             │
                        │    (Adversary A1, A2, A4 inputs)     │
                        └──────────────────┬───────────────────┘
                                           │
                ┌──────────────── L4 / L7 boundary ────────────────┐
                │       (TLS terminator, WAF — out of scope)       │
                └────────────────────────┬─────────────────────────┘
                                         │
   ┌─────────────────────────────────────┼─────────────────────────────────────┐
   │                          SERVICE PROCESS                                  │
   │  ┌────────────────────┐       ┌────────────────────┐                      │
   │  │  httpx public :8080 │      │  grpcx public :9090 │   <-- A1, A2, A4    │
   │  │  stack.Default      │      │  Recovery + auth    │                     │
   │  │  + recover/auth/    │      │  interceptors       │                     │
   │  │   csrf/...          │      │                     │                     │
   │  └─────────┬───────────┘      └──────────┬──────────┘                     │
   │            │                             │                                │
   │            └──────── core/tenant ────────┘  <-- propagated in ctx         │
   │            (every downstream call carries tenant ID + req ID)             │
   │                                                                           │
   │  ┌────────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────┐   │
   │  │ data/idempotency│  │  data/lock   │  │  data/cache  │  │data/queue  │   │
   │  └────────┬────────┘  └──────┬───────┘  └──────┬───────┘  └─────┬──────┘   │
   │           │                  │                 │                │          │
   │   ┌───────┴──────────────────┴─────────────────┴────────────────┴───────┐  │
   │   │   internal /metrics + /ready :9090  (internal-only — no auth)       │  │
   │   └───────────────────────────────────────────────────────────────────┐ │  │
   │                                                                       │ │  │
   └───────────────────────────┬───────────────────────────────────────────┴─┴──┘
                               │
       ┌───────────────────────┼─────────────────────────────────────┐
       │  PRIVATE NETWORK (Adversary A1 cannot reach by assumption)  │
       │                       │                                     │
       │  ┌─────────────┐    ┌──┴─────────┐   ┌────────────┐         │
       │  │ Postgres    │    │  Redis     │   │  RabbitMQ  │         │
       │  │ (TLS req'd) │    │ (TLS req'd)│   │ (TLS req'd)│         │
       │  └─────────────┘    └────────────┘   └────────────┘         │
       │                                                              │
       │  ┌─────────────┐    ┌────────────┐    ┌────────────────┐    │
       │  │  S3/GCS/Azure│   │ KMS / Vault│    │ JWKS issuer    │    │
       │  │   storage    │   │  (secrets) │    │ (auth0/etc.)   │    │
       │  └─────────────┘    └────────────┘    └────────────────┘    │
       └─────────────────────────────────────────────────────────────┘
```

**Trust boundaries enforced by the kit:**

- **Public listener → service code** — every middleware in
  [httpx/middleware/stack/chain.go](../../httpx/middleware/stack/chain.go)
  runs before user code; `recover` (§4.1) is unconditionally
  prepended.
- **Service code → datastore** — connection helpers in
  [infra/sqldb/config.go](../../infra/sqldb/config.go) and
  [infra/redis/config.go](../../infra/redis/config.go) refuse to
  start if TLS is downgraded outside development.
- **Service code → broker** — AMQP connector requires a TLS scheme
  (`amqps://`) outside development.
- **Service code → external HTTP** — `httpx.NewResilientHTTPClient`
  does not use `http.DefaultClient` and does not add proxy / SSRF
  exemptions; for SSRF-sensitive paths the service author wires
  `security/netutil.SSRFSafeTransport`.

A small mermaid version of the same diagram is embedded near the
top of [docs/ai/security.md](../ai/security.md).

---

## 4. Attack surface per kit area

### 4.1 HTTP entrypoint (`httpx`, `httpx/middleware`)

**Default stack.** Services must use
[httpx/middleware/stack.Default](../../httpx/middleware/stack/chain.go)
unless they have a documented reason to deviate; the chain is

```
recover -> metrics -> requestID -> tracing -> logging -> timeout -> handler
```

with optional `outer` (CSRF, content-type pinning) and `inner`
(auth, rate-limiting, idempotency) wedges. Every middleware below is
positioned in the chain by `stack.Default`; the kit defaults to a
panic-catching `recover` middleware in every constructed stack.
[`stack.WithoutRecover()`](../../httpx/middleware/stack/stack.go) is
the only way to remove it and requires an explicit acknowledgement in
the service author's wiring — it is documented as strongly discouraged
and is not reachable via a default option.

#### Threats and mitigations

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| H-01 | Panic in handler kills the entire HTTP server | A1 | `recover` middleware unconditionally prepended; logs + emits 500 | [httpx/middleware/recover/recover.go](../../httpx/middleware/recover/recover.go) |
| H-02 | Slowloris / oversized requests exhaust file descriptors | A1 | `httpx.NewServer` sets ReadHeaderTimeout, ReadTimeout, MaxHeaderBytes; `maxbody` middleware caps body | [httpx/httpx.go](../../httpx/httpx.go), [httpx/middleware/maxbody/maxbody.go](../../httpx/middleware/maxbody/maxbody.go) |
| H-03 | Unbounded request handlers — long-running requests pin connections | A1 | `timeout` middleware in `stack.Default` with 30s default and 1 MiB buffer cap | [httpx/middleware/timeout/timeout.go](../../httpx/middleware/timeout/timeout.go) |
| H-04 | Cross-site request forgery against authenticated browser sessions | A1 + browser | Session-bound CSRF (double-submit + HMAC binding); construction panics if no `WithSecret`/`WithSecrets` HMAC material is supplied and `WithDevSecret` has not been opted into — the kit ships no allow-list of forbidden specific values, so deployments must ensure the configured secret is not a documented placeholder | [httpx/middleware/csrf/csrf.go](../../httpx/middleware/csrf/csrf.go), [security/csrf/csrf.go](../../security/csrf/csrf.go) |
| H-05 | Missing security headers permit clickjacking, mixed content, MIME sniffing | A1 + browser | `secheaders` middleware sets HSTS (when behind trusted proxy), CSP, X-Content-Type-Options, X-Frame-Options, Referrer-Policy | [httpx/middleware/secheaders/secheaders.go](../../httpx/middleware/secheaders/secheaders.go) |
| H-06 | XSS via inline scripts | A1 + browser | `cspnonce` middleware emits a per-request nonce that the secheaders CSP refers to | [httpx/middleware/cspnonce](../../httpx/middleware/cspnonce/) |
| H-07 | Spoofed `X-Forwarded-For` allows rate-limit bypass / log poisoning | A1 | `clientip` defaults to loopback-only trust; `clientip.ParseTrustedProxiesStrict` fails-fast on bad config | [httpx/middleware/clientip/clientip.go](../../httpx/middleware/clientip/clientip.go) |
| H-08 | CORS misconfiguration leaks data to attacker origin | A1 | `cors` middleware has no `*` default for credentials; explicit allowlist required | [httpx/middleware/cors/cors.go](../../httpx/middleware/cors/cors.go) |
| H-09 | Unauthenticated access to authenticated routes | A1 | `auth` middleware verifies JWT/PASETO; service composes `auth.RequireScope` for finer checks | [httpx/middleware/auth/auth.go](../../httpx/middleware/auth/auth.go), [httpx/middleware/auth/scope.go](../../httpx/middleware/auth/scope.go) |
| H-10 | Information disclosure via verbose errors | A1 | `httpx.WriteServiceError` / `WriteServiceProblem` map `core/apperror` codes to safe HTTP status + RFC 7807 — never returns wrapped error strings | [httpx/error_handler.go](../../httpx/error_handler.go), [httpx/problemdetails](../../httpx/problemdetails/) |
| H-11 | Request smuggling via duplicate Content-Length | A1 | Go stdlib server rejects ambiguous CL/TE — relied on, not reimplemented |
| H-12 | Open redirect via attacker-controlled URL params | A1 | `httpx.SafeRedirect` validates untrusted redirect targets, rejects scheme-relative / encoded scheme-relative targets, userinfo, non-HTTP schemes, control bytes, and absolute hosts outside an explicit allowlist | [httpx/redirect.go](../../httpx/redirect.go) |
| H-13 | Log injection via crafted headers | A1 | Structured logging (`slog`) — fields are key/value, not formatted; `secret.String` redacts credential fields | [observability/logging](../../observability/logging/), [core/secret](../../core/secret/) |
| H-14 | Mass-assignment via permissive JSON decoding | A2 | The `httpx` typed-handler JSON decoder enables `DisallowUnknownFields`, rejecting any payload field not present in the destination struct; this is the load-bearing mass-assignment defence. Services that decode JSON via custom paths bypass this and must apply equivalent strictness. | [httpx/httpx.go](../../httpx/httpx.go) |

**Important interaction:** `recover` MUST run before `metrics` in the
chain so panic responses are still counted; `stack.Default` enforces
this ordering and there is no exposed setter that lets the service
reorder it.

### 4.2 gRPC entrypoint (`grpcx`)

The threat surface is similar to §4.1, with TLS and binary framing
removing some categories (no CSRF, no header smuggling) and adding
others (interceptor ordering, streaming exhaustion).

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| G-01 | Panic in unary/streaming handler kills server | A1 | Recovery interceptor (unary + stream) prepended by `grpcx.NewServer`; `WithoutRecovery` is opt-out, not default | [grpcx/server.go](../../grpcx/server.go), [grpcx/interceptor](../../grpcx/interceptor/) |
| G-02 | Unauthenticated RPC | A1 | Auth interceptor wired by `app.WithJWT`/`WithPASETO`; rejects requests without a valid token | [app/jwt_module.go](../../app/jwt_module.go), [app/paseto_module.go](../../app/paseto_module.go) |
| G-03 | Streaming flood — client opens N streams and never sends | A1 | gRPC's max-concurrent-streams + per-stream deadline; service authors must set per-RPC timeouts. **Listed as gap if defaults are not set.** |
| G-04 | Error message leakage via gRPC status messages | A1 | `grpcx/apperror_status.go` maps `core/apperror` codes to gRPC codes + safe messages — never returns the underlying `error.Error()` | [grpcx/apperror_status.go](../../grpcx/apperror_status.go) |
| G-05 | Health probe leaks service liveness to attacker | A1 | Builder serves gRPC Health Checking Protocol on the internal ops listener over h2c; public gRPC health is disabled unless the operator explicitly calls `WithPublicGRPCHealth()` | [app/builder.go](../../app/builder.go), [app/internal_grpc_health.go](../../app/internal_grpc_health.go) |

### 4.3 Message broker (`infra/messaging`)

The kit supports AMQP (`amqpbackend`), Redis Streams
(`redisbackend`), NATS JetStream (`natsbackend`), and an in-memory
test broker (`membroker`). All four implement the same `Publisher`
and `Consumer` interfaces.

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| M-01 | Unroutable message silently dropped (AMQP) | A1, A4, A5 | AMQP publisher sets `mandatory=true` and listens on `NotifyReturn`; returned messages surface as a publish error, not a silent drop | [infra/messaging/amqpbackend](../../infra/messaging/amqpbackend/) |
| M-02 | Message-payload schema drift between producer/consumer leads to silent corruption | A4, A5 | `messaging.Schema` + `VersionedHandler` verify a JSON Schema before invoking the handler | [infra/messaging/schema.go](../../infra/messaging/schema.go), [infra/messaging/versioned_handler.go](../../infra/messaging/versioned_handler.go) |
| M-03 | Consumer ACKs on transient error → message lost | A1 | Convention: handlers return `error` (kit re-queues); `apperror.NewPermanent` is the only way to signal "do not retry, send to DLQ" | [infra/messaging/delivery.go](../../infra/messaging/delivery.go) |
| M-04 | DLQ poison-pill exhausts consumer retry budget | A1 | DLQ topology + retry-with-backoff handler in [infra/messaging/amqpbackend](../../infra/messaging/amqpbackend/) (`Retry`, `RetryIfNotPermanent`) |
| M-05 | Producer outage drops events that should reach the broker | A1 (DoS) | `messaging.BufferedPublisher` with an optional state file persists pending messages across restarts; **state file path validated against directory traversal** | [infra/messaging/buffered_publisher.go](../../infra/messaging/buffered_publisher.go) |
| M-06 | Internal `debughttp` Publish/Consume HTTP endpoints expose broker to attacker | A1 | `debughttp` requires a `Guard` middleware + Authenticator — refuses to mount otherwise | [infra/messaging/amqpbackend/debughttp/guard.go](../../infra/messaging/amqpbackend/debughttp/guard.go) |
| M-07 | TLS-less broker connection on the wire | A1 (network observer) | Connector validates `amqps://` scheme; pure `amqp://` is rejected by the always-on `app.Builder` production-safety validator | [infra/messaging/amqpbackend](../../infra/messaging/amqpbackend/), [app/builder.go](../../app/builder.go) |
| M-08 | Oversized messages exhaust broker/client memory or poison the buffered retry state file | A1, A4 | `messaging.MessageSizeLimiter` defaults to 1 MiB, supports exact route overrides, and is wired into AMQP, NATS, Redis Streams, `membroker`, and `BufferedPublisher`; Builder exposes `WithMaxMessageBytes` and `WithRouteMaxMessageBytes` for golden-path services | [infra/messaging/size_limit.go](../../infra/messaging/size_limit.go), [app/builder.go](../../app/builder.go) |

**Note:** `BufferedPublisher` is **not** a transactional outbox.
Where strict at-most-once / exactly-once is required, services use
`infra/outbox` (§4.11). The kit refuses to silently merge the two
patterns.

### 4.4 Relational database (`infra/sqldb`)

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| D-01 | Postgres connection in clear text on private network | A1 (network observer), A3 | `sslmode` defaults to `prefer`; the always-on `app.Builder` validator rejects `disable`/`prefer`/`allow` and requires one of `require`/`verify-ca`/`verify-full`. The `pgx.Config.AllowPlaintextLoopbackForTests` field is the only relaxation, gated on every host (and every multi-host fallback) resolving to loopback | [infra/sqldb/config.go](../../infra/sqldb/config.go), [app/validate.go](../../app/validate.go), [infra/sqldb/pgx/pgx.go](../../infra/sqldb/pgx/pgx.go) |
| D-02 | SQL injection via string concatenation | A1, A2 | Kit runtime DB path is pgx/sqlc-style parameterized queries; no string-concat query helper in the kit's surface area |
| D-03 | DB credential leakage via process logs | A6 | `sqldb.Config` / `pgxbackend.Config` implement safe `LogValue` renderers and `_FILE` secret loading avoids embedding password material in config errors; `pgxbackend.Config.PasswordProvider` supports rotating credentials without logging returned values | [infra/sqldb/config.go](../../infra/sqldb/config.go), [infra/sqldb/pgx/pgx.go](../../infra/sqldb/pgx/pgx.go), [core/config](../../core/config/) |
| D-04 | Connection-pool exhaustion DoS | A1 | `PoolConfig.MaxOpenConns` is set by `DefaultPool()` to 100 (services that need a different ceiling override it explicitly); `WithDBMetrics` exposes saturation | [infra/sqldb/config.go](../../infra/sqldb/config.go) |
| D-05 | Stolen DB backup discloses encrypted-at-rest fields | A6 | `crypto/envelope` for envelope encryption with KMS/Vault-rotatable KEKs; field-level helpers in `crypto/encrypt` | [crypto/envelope/envelope.go](../../crypto/envelope/envelope.go), [crypto/envelope/awskms](../../crypto/envelope/awskms/), [crypto/envelope/azurekeyvault](../../crypto/envelope/azurekeyvault/), [crypto/envelope/gcpkms](../../crypto/envelope/gcpkms/), [crypto/envelope/vaulttransit](../../crypto/envelope/vaulttransit/), [crypto/encrypt](../../crypto/encrypt/) |
| D-06 | Migration downgrade in prod (drop column) | A5 | Goose migrations are forward-only by convention; the kit ships no down-migration helper for prod use | [docs/ai/sqldb.md](../ai/sqldb.md) |

### 4.5 Redis (`infra/redis`, `data/*`)

Redis is used for cache (`data/cache/rediscache`), idempotency
(`data/idempotency/redisstore`), distributed locks
(`data/lock/redislock`), rate limiting
(`data/ratelimit/redis`, `data/ratelimit/gcra`,
`data/ratelimit/tokenbucket`), event streams
(`data/stream/redisstream`), and queues (`data/queue/redisqueue`).

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| R-01 | Cross-tenant key collision (tenant A reads tenant B's cached value) | A2 | `core/tenant.Key(ctx, parts...)` builds length-prefixed scoped keys, `data/cache/tenant.Wrap` / `data/idempotency/tenant.Wrap` use the same encoder, `cmd/kit-new -tenant` scaffolds those wrappers, and `kit-doctor` flags hand-written `tenant:` key prefixes. | [core/tenant](../../core/tenant/), [data/cache/tenant](../../data/cache/tenant/), [data/idempotency/tenant](../../data/idempotency/tenant/), [cmd/kit-new](../../cmd/kit-new/), [cmd/kit-doctor](../../cmd/kit-doctor/) |
| R-02 | Lock split-brain (two holders for the same name) | A1, A3 (clock skew) | `redislock.Locker.Acquire` returns a per-call `Lock` handle with a fencing token; `Unlock` checks owner; `Acquire` twice without `Release` returns an error | [data/lock/redislock](../../data/lock/redislock/), [data/lock/lock.go](../../data/lock/lock.go) |
| R-03 | Idempotency replay across instances → two writes | A1 | Redis store uses Lua to atomically claim key + owner token; `Unlock` requires matching owner | [data/idempotency/redisstore/store.go](../../data/idempotency/redisstore/store.go) |
| R-04 | Idempotency permanent lock (TTL=0) wedges further requests | A1 (induced) | `WithTTL(0)` on the middleware panics at construction; backends return `ErrInvalidTTL` | [httpx/middleware/idempotency/idempotency.go](../../httpx/middleware/idempotency/idempotency.go) |
| R-05 | Queue race — one consumer steals another's in-flight message | A1 (load-induced) | Per-consumer `:processing` list; Lua `removeByID` + LRANGE peek + dispatch-failure preserves message | [data/queue/redisqueue](../../data/queue/redisqueue/) |
| R-06 | Redis `KEYS *` exposed via debug endpoint allows enumeration | A1 | Kit ships no debug-KEYS endpoint; `health` checks use `PING` only | [observability/health/health.go](../../observability/health/health.go) |
| R-07 | TLS-less Redis on private network | A1 (observer), A3 | `redis.Config.Validate` rejects plaintext `redis://` URLs unless the deployment opts in via `redis.Config.AllowPlaintext` (env `REDIS_ALLOW_PLAINTEXT`); URLs carrying `skip_verify=` are rejected outright because the kit refuses to disable TLS verification | [infra/redis/config.go](../../infra/redis/config.go) |
| R-08 | Rate-limit fixed-window allows burst at boundary | A1 | Sliding-window primitives `data/ratelimit/gcra`, `data/ratelimit/tokenbucket` available; service author chooses; old fixed-window implementation retained for back-compat with documented caveats | [data/ratelimit/ratelimit.go](../../data/ratelimit/ratelimit.go) |
| R-09 | `MemoryCache` unbounded growth → OOM | A1 | Default 64 MiB cost cap; `WithMaxSize` / `WithMaxCost` are the only configuration knobs and both panic at construction if given a non-positive value, so a misconfigured cap fails fast instead of degenerating into an unbounded cache | [data/cache/memory_cache.go](../../data/cache/memory_cache.go) |

### 4.6 Object/file storage (`infra/storage`, `infra/storage/storagehttp`)

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| S-01 | Path traversal via attacker-controlled key | A1 | `storagehttp.UUIDKeyFunc` recommended; raw client filenames as keys are explicitly disallowed by AGENTS.md "Never use raw client filenames as storage keys" | [infra/storage/storagehttp/keyfunc.go](../../infra/storage/storagehttp/keyfunc.go) |
| S-02 | Local backend replaces a file but doesn't fsync parent dir → corruption on crash | A1 (DoS) | Local backend fsyncs parent directory after rename | [infra/storage/localbackend](../../infra/storage/localbackend/) |
| S-03 | Server-side encryption keys leaked in logs | A6 | `storage/encryption` uses `crypto/envelope` KEKs; KEK material wrapped in `core/secret.String` | [infra/storage/encryption](../../infra/storage/encryption/) |
| S-04 | Upload bypasses content-type/size limits | A1 | `storagehttp/uploadsec` provides MIME sniffing, size limits, dimension limits, and a generic malware scanner contract; `infra/storage/storagehttp/uploadsec/clamav` ships a ClamAV adapter that also exposes a `storage.Validator` bridge for `storagehttp.ParseAndStore`. | [infra/storage/storagehttp/uploadsec](../../infra/storage/storagehttp/uploadsec/), [infra/storage/storagehttp/uploadsec/clamav](../../infra/storage/storagehttp/uploadsec/clamav/) |
| S-05 | SSRF via "fetch from URL" feature when service supports remote ingestion | A1 | `security/netutil.SSRFSafeTransport` resolves and rejects RFC1918, link-local, multicast destinations; documented "do not store SSRFSafeTransport long-term" anti-pattern | [security/netutil/ssrf.go](../../security/netutil/ssrf.go) |
| S-06 | Cross-tenant file disclosure via guessable key | A2 | UUIDKeyFunc + tenant-prefixed namespace in `storage.Manager` policy; service-author responsibility documented in [docs/ai/storage.md](../ai/storage.md) |

### 4.7 JWT / PASETO verification (`security/jwtutil`, `crypto/paseto`)

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| T-01 | `alg=none` token accepted | A1 | `jwtutil` uses `github.com/MicahParks/keyfunc` + jwx/jwt; `none` is rejected by the parser | [security/jwtutil/jwtutil.go](../../security/jwtutil/jwtutil.go) |
| T-02 | JWT issuer not validated → token from different IDP accepted | A1 | `WithJWT` requires `WithJWTIssuer` (pin the expected `iss`) or the explicit opt-out `WithoutJWTIssuer`; the always-on `app.Builder` validator fails `Build()` if neither is set, so a service cannot ship with an unpinned issuer by accident. The audience check has the same shape (`WithJWTAudience` or `WithoutJWTAudience`) | [app/jwt_module.go](../../app/jwt_module.go), [app/validate.go](../../app/validate.go) |
| T-03 | JWKS rotation makes valid tokens unverifiable (key cache stale) | A1 (DoS via timing) | `keyfunc` library refreshes JWKS on cache miss; configurable refresh interval |
| T-04 | JWT replay after logout (no revocation) | A1 | `security/jwtutil/revocation` stores revoked `jti` values until token expiry over any cache-compatible backend; `jwtutil.Provider` can fail closed through `WithRevocationChecker`. | [security/jwtutil](../../security/jwtutil/), [security/jwtutil/revocation](../../security/jwtutil/revocation/) |
| T-05 | PASETO key confusion (v3.local vs v3.public) | A1 | `crypto/paseto` exposes purpose-typed handles; you cannot accidentally hand a local key to the public verifier | [crypto/paseto/paseto.go](../../crypto/paseto/paseto.go) |
| T-06 | PASETO key in environment variable visible to `ps` / `/proc/<pid>/environ` | A6 | All PASETO keys go through `core/secret.String`; the kit's config loader supports `_FILE` suffix to mount keys from disk | [core/secret/secret.go](../../core/secret/secret.go), [core/config](../../core/config/) |

### 4.8 Signed-request middleware (`httpx/middleware/signedrequest`)

Used for machine-to-machine endpoints (webhooks, internal RPC over
HTTP) where JWT is overkill or impossible.

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| W-01 | Replay of a captured signed request | A1 | Nonce store with TTL; `signedrequest.NonceStore` interface implemented over Redis (`data/idempotency/redisstore`-style) — duplicate nonce rejected | [httpx/middleware/signedrequest/noncestore.go](../../httpx/middleware/signedrequest/noncestore.go) |
| W-02 | Header-strip attack — attacker removes signed headers and resigns | A1 | The signature input includes the canonical sorted list of signed-headers; receiver computes the same list and rejects mismatches | [httpx/middleware/signedrequest/signedrequest.go](../../httpx/middleware/signedrequest/signedrequest.go) |
| W-03 | Clock-skew bypass of timestamp window | A1 | Configurable max-clock-skew with a default in single-digit minutes; rejection on stale or future-dated requests |
| W-04 | HMAC key reuse across services | A5 | Convention: per-service keys; `app.WithSignedRequests` accepts a key reference, not a key string | [app/signedrequest_module.go](../../app/signedrequest_module.go) |
| W-05 | Signing key in source control | A5, A6 | `core/secret.String` wrapping; signing-key access flagged in code review by grepping for `SecretString.Reveal` |

### 4.9 Idempotency replay defence (`data/idempotency`, `httpx/middleware/idempotency`)

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| I-01 | Client retries POST → write happens twice | A1 (or honest retry) | `Idempotency-Key` header + request-fingerprint match; second match returns the cached response | [httpx/middleware/idempotency/idempotency.go](../../httpx/middleware/idempotency/idempotency.go) |
| I-02 | Different request body, same key → cached response leaks across calls | A1 | Middleware computes a fingerprint over `(method, path, body-hash)` and compares it to the stored fingerprint; mismatch returns 422 | [httpx/middleware/idempotency/idempotency.go](../../httpx/middleware/idempotency/idempotency.go) |
| I-03 | TTL=0 wedges all subsequent requests | A1 (DoS) | Middleware panics at construction with TTL=0; backends return `ErrInvalidTTL` | [data/idempotency/idempotency.go](../../data/idempotency/idempotency.go) |
| I-04 | pgstore Unlock without owner check → split brain | A1 | pgstore migration adds `owner_token`; `Unlock` requires matching token | [data/idempotency/pgstore/store.go](../../data/idempotency/pgstore/store.go) |
| I-05 | Memory store used in production → no cross-instance protection | A5 | AGENTS.md explicitly forbids `idempotency.NewMemoryStore` in prod; `kit-doctor` flags it | [cmd/kit-doctor](../../cmd/kit-doctor/) |

### 4.10 LLM-cost / runaway-work exhaustion

This section covers the asset class "external API budgets" (asset
[7]) that becomes critical when services proxy to paid LLM
backends or other expensive operations.

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| L-01 | Single tenant exhausts global LLM spend in minutes | A2, A4 | `data/budget`, `data/budget/redis`, `httpx/middleware/budget`, and `httpx/budget` provide per-tenant inbound and outbound spend controls; Builder wires them with `WithTenantBudget` | [data/budget](../../data/budget/), [httpx/middleware/budget](../../httpx/middleware/budget/), [httpx/budget](../../httpx/budget/), [app/budget_module.go](../../app/budget_module.go) |
| L-02 | Prompt-injection causes the model to call an internal tool with attacker payloads | A4 | Defence-in-depth: `core/tenant.Required` at every storage boundary means tool-call args inherit the *legitimate* tenant context, not the attacker's claim; the kit refuses to let an LLM-emitted token override an authenticated tenant ID. **Service still owns input validation on tool call args.** | [core/tenant/tenant.go](../../core/tenant/tenant.go) |
| L-03 | Background job loop runs forever after a partial failure | A1 (poisoned input) | `runtime/concurrency.FanOut` returns first error; `FanOutSettled` always cancels child contexts on parent cancellation | [runtime/concurrency](../../runtime/concurrency/) |
| L-04 | Cron job fires on every replica and the work runs N times | A5 (deployment misconfig) | `runtime/cron` integrates with `infra/leaderelection` so only the leader runs jobs; `app.WithLeaderElection` opt-in | [runtime/cron](../../runtime/cron/), [infra/leaderelection](../../infra/leaderelection/), [app/leader_module.go](../../app/leader_module.go) |
| L-05 | `BufferedPublisher` queue grows unboundedly while broker is down → OOM | A1 (DoS) | `defaultBufferedMaxSize = 10_000`; once exceeded, the publisher returns a `"buffered publisher: buffer full, message dropped"` error rather than evicting silently; state-file path validated | [infra/messaging/buffered_publisher.go](../../infra/messaging/buffered_publisher.go) |

### 4.11 Outbox + transactional integrity (`infra/outbox`)

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| O-01 | Dual-write inconsistency — DB commit succeeds but broker publish fails | A1 (broker outage) | `outbox.Writer` can require an ambient transaction via `WithRequireTransaction`; relay reads, publishes, and marks processing rows through the store contract | [infra/outbox/outbox.go](../../infra/outbox/outbox.go), [infra/outbox/relay.go](../../infra/outbox/relay.go) |
| O-02 | Tight retry loop with no backoff hammers broker | A1 (broker outage) | Relay uses `next_retry_at` + exponential backoff; exhausted rows move to failed state for dead-letter inspection | [infra/outbox/relay.go](../../infra/outbox/relay.go) |
| O-03 | Two relays claim the same row → duplicate publish | A1 (cluster condition) | Atomic `UPDATE … WHERE claimed_at IS NULL` claim pattern; `updated_at` used for stale-claim detection | [infra/outbox/gormstore](../../infra/outbox/gormstore/) |
| O-04 | Outbox table grows forever | A5 (housekeeping omitted) | `Relay` cleans old published rows and old failed rows on startup and periodic ticks; `WithRetention` and `WithFailedRetention` tune the windows | [infra/outbox/relay.go](../../infra/outbox/relay.go) |

### 4.12 Internal observability port

The kit binds `/ready`, `/metrics`, `/debug/pprof`, and the SLO
handler on a separate internal port (default `:9090`).

| ID | Threat | Adversary | Mitigation | Where |
|---|---|---|---|---|
| P-01 | Public exposure of `/debug/pprof` allows arbitrary heap dump | A1 | The kit's internal port is intended to be cluster-internal only; deployment manifests must not expose it. The `pprof` handler is mounted via `observability/pprof` opt-in, not on by default | [observability/pprof](../../observability/pprof/) |
| P-02 | Public `/metrics` discloses internal cardinality | A1 | Same as P-01 — internal port; AGENTS.md "Never embed user IDs or request IDs in Redis/Prometheus metric names" prevents per-user metric label cardinality | [observability/promutil](../../observability/promutil/) |
| P-03 | Health check leaks dependency credentials in error message | A1 | `health` checks return safe error strings; underlying error chain logged but not surfaced | [observability/health/health.go](../../observability/health/health.go) |

---

## 5. Cross-cutting controls

The mitigations below apply across multiple kit areas. Each is a
deliberate "refuse-to-misconfigure" property, not a best-effort
hint.

### 5.1 Refuse-to-misconfigure invariants

The kit panics at startup (NOT at first request) when any of the
following hold:

- `WithPostgres` without a non-empty pgx DSN.
- `WithJWT` without `WithJWTIssuer` (or the explicit `WithoutJWTIssuer` opt-out). Audience has the same shape.
- Postgres `sslmode=disable` outside development.
- AMQP scheme `amqp://` (cleartext) outside development.
- `idempotency.WithTTL(0)`.
- `clientip` config that fails `ParseTrustedProxiesStrict`.
- CSRF middleware constructed without a per-deployment shared
  secret and without the explicit `WithDevSecret` opt-in (the
  constructor panics; no specific placeholder value is matched, so
  operators must source the secret from a non-placeholder).
- `signedrequest` middleware mounted without a nonce store.
- `BufferedPublisher` configured with a state file path that
  escapes the configured directory.
- Nil dependency to any constructor (the kit's "fail-fast nil-deps"
  cluster).
- `MemoryCache` configured via `WithMaxSize` / `WithMaxCost` with a
  zero or negative value (the option panics at construction).
- Negative or zero values to `NewRateLimiter`, `NewKeyedRateLimiter`,
  `Timeout`, `MaxBodySize`.

The [`app.Builder`](../../app/builder.go) production-safety validator
runs unconditionally inside `Build()` — every service the kit builds
gets the same posture. There is no `KIT_ENV` (or `APP_ENV`) escape
hatch in any kit code path. Per-feature relaxations are explicit
opt-outs the operator declares consciously: `WithoutTLS`,
`WithInternalNonLoopback`, `WithoutJWTIssuer`, `WithoutJWTAudience`.

### 5.2 Refuse-to-print secrets

[`core/secret.String`](../../core/secret/secret.go) refuses to render
through:

- `fmt.Sprintf("%v", s)` and other verbs (via `fmt.Formatter` + `fmt.Stringer`).
- `slog.Info("...", "key", s)` (via `LogValue`).
- `json.Marshal` (via `json.Marshaler`).
- Any encoder that consults `encoding.TextMarshaler` (yaml.v3,
  TOML, and similar serialisers); `MarshalText` returns the
  redaction marker, never the underlying bytes.

Intentional access is via `s.RevealString()` / `s.Reveal()`, which
are greppable for audit. Not every integration config stores credentials as
`core/secret.String`; URL/DSN and SDK-provider based fields instead implement
safe `LogValue` renderers, stable errors, `_FILE` loading, or provider
callbacks. Current high-risk credential surfaces are covered by:

- DB/Redis/AMQP/NATS config log redaction plus DB/Redis/AMQP/NATS provider
  hooks for rotation.
- Storage config log redaction plus S3 default/provider credentials, Azure
  token credentials, GCS ADC/client options, and SFTP password providers.
- JWT/PASETO/HMAC signing keys through JWKS refresh, caller-supplied PASETO
  providers, CSRF secret rings, and signed-request key stores.
- Envelope KEK material through KMS/Vault provider SDKs and recorded KEK IDs.

### 5.3 Tenant scoping

`core/tenant` propagates a tenant ID through `context.Context`. Two
helpers enforce it:

- `tenant.Required(ctx)` — returns the ID or an error if absent.
- `tenant.WithID(ctx, id)` — adds an ID; refuses to overwrite an
  existing one in the same context (prevents a downstream caller
  from "re-stamping" the request as another tenant).

Storage-layer adapters (Redis, Postgres) accept a tenant ID and
build keys / `WHERE` clauses. Free-form tenant-scoped keys should go
through `tenant.Key(ctx, parts...)`, which length-prefixes each variable
field so `tenant=a:b, key=c` cannot collide with `tenant=a, key=b:c`.

### 5.4 Structured audit logs

[`observability/auditlog`](../../observability/auditlog/auditlog.go)
appends HMAC-chained audit records. Each record carries:

- Actor (user ID, tenant ID, request ID).
- Action verb (taken from a service-defined enum).
- Object (typed reference; the auditlog package does not serialise
  the object itself).
- `PrevHMAC` — the HMAC of the previous chain entry. The first
  event in a chain has an empty `PrevHMAC`.
- `HMAC` — the tamper-evident tag for this entry.

**Chain key (asset [5]).** `auditlog.New` requires
`WithChainKey(...)` with a ≥32-byte secret (`auditlog.MinChainKeyLen`).
Missing / short keys fail fast at constructor time — operators cannot
silently ship the package without tamper-evidence. The key must be
shared across every replica that appends to the same store; source it
from KMS / config secrets, never from per-pod random material. Rotating
the chain key invalidates `VerifyChain` for all previously-appended
records, so operate the chain on a single long-lived key for the
chain's lifetime and archive the key alongside the records.

**Canonical encoding.**
[`canonicalEvent`](../../observability/auditlog/chain.go) serialises
every wire-relevant event field — `prev_hmac`, `id`, `timestamp` (as
fixed-width UnixNano), `actor`, `action`, `resource`, `status`,
`ip_address`, `trace_id`, `metadata`, and `prev_hmac` again as a
position-sensitive tail — as `uint32 length || bytes` blocks. Length
prefixing prevents adjacent-field confusion (e.g. `actor="a\0action=b"`
colliding with `actor="a", action="b"`). The `HMAC` field is excluded
from the canonical form because it is the value being computed.

**Append serialisation.** `Logger.LogE` holds an internal mutex across
the read-prev-HMAC / compute / `Store.Append` window so two concurrent
appenders cannot read the same predecessor HMAC and fork the chain.
`Store.LastHMAC(ctx) ([]byte, error)` is the persistence-layer
contract that feeds the chain tail back to the Logger — bundled
`MemoryStore` and downstream stores must implement it. Caller-supplied
`PrevHMAC` / `HMAC` fields on the input event are discarded; the
Logger is the sole authority on chain HMACs.

**Verify entry points.** `auditlog.VerifyChain(events, chainKey)`
validates a slice of events in chain order (oldest first), wrapping
[`ErrChainBroken`](../../observability/auditlog/chain.go) at the
offending index for the first mismatch. `Logger.VerifyChain(ctx)`
streams every event from the underlying store (paging in batches of
`verifyChainPageSize`) and runs the same validation, so on-call
operators can verify the live ledger without hand-rolling a Query
loop. HMAC comparison is constant-time to prevent timing oracles.
Empty chains are valid by definition.

**Signed cursors (asset [6]).** `Logger.Query` rejects forged
cursors via [`signedCursor`](../../observability/auditlog/cursor.go).
Cursors handed to clients are
`base64url(payload) "." base64url(HMAC-SHA256(cursorKey, payload))`
— the same envelope as `httpx/pagination.CursorSigner`, replicated
inline because the `observability` module cannot import `httpx`. A
≥32-byte `WithCursorKey(...)` is required at constructor time;
missing / short keys panic so the audit log cannot ship with
forgeable cursors. Malformed, truncated, or foreign-signed cursors
return a wrapped `ErrInvalidCursor` that callers can match with
`errors.Is` to map to 400 Bad Request at the HTTP boundary. The
cursor key is independent of the chain key so the two can be
rotated separately.

Threats defused: **A1 / A6 forge a future cursor to skip records or
enumerate IDs**; **A6 tamper with a stored record after the fact**;
**A5 silently strip middle records before retention runs**.

### 5.5 Outbound HTTP discipline

The kit's `httpx.NewResilientHTTPClient` and
`httpx.NewHTTPClient` set:

- A non-zero connect/read/write timeout.
- A transport constructed by cloning `http.DefaultTransport` at HTTP
  client construction time; the resulting transport is owned by the
  kit and the global `http.DefaultTransport` is never referenced
  thereafter, so mutations *after* the client is built are isolated.
  Mutations *before* construction are inherited — services should
  avoid mutating `http.DefaultTransport` from any package `init()`.
- A `core/secret.String`-aware logger that redacts auth headers.

`security/netutil.SSRFSafeTransport` wraps the default transport
with a destination-resolution check that rejects RFC1918, link
local, and loopback unless explicitly allowlisted.

---

## 6. Request-flow walk-throughs

The matrices in §4 enumerate threats and mitigations independently
per kit area. To make the chain of defences concrete, this section
walks four representative request flows from boundary entry to data
commit, naming every middleware/component the request crosses and
the threats each one defuses.

### 6.1 Authenticated user POST (idempotent + audited)

The canonical "user submits a payment" flow. Wired via:

```go
app.New("payments", version, cfg.BaseConfig).
    WithPostgres(cfg.Postgres).
    WithRedis(&redis.Options{Addr: cfg.RedisAddr}).
	    WithJWT(cfg.JWKSURL).WithJWTIssuer(cfg.Issuer).WithJWTAudience(cfg.Audience).
	    WithIPRateLimit(100, time.Minute).
	    Router(func(infra app.Infrastructure) http.Handler {
	        h := payments.NewHandler(infra.DB, infra.Cache, audit)
	        csrfMW := csrf.New(
	            csrf.WithSecrets(cfg.CSRFSecret, cfg.PreviousCSRFSecrets...),
	            csrf.WithAllowedOrigins(cfg.PublicOrigin),
	        )
	        return stack.Default(h, infra.Logger,
	            stack.WithOuter(csrfMW, csrf.RequireJSONContentType),
	            stack.WithInner(auth.Required, idempotency.Middleware(infra.IdempStore)),
	        )
	    }).Run()
```

The Builder's production-safety validator runs in `Build()` — the
service refuses to start unless TLS, JWT issuer, JWT audience, internal-
host loopback, Postgres `sslmode`, and tracing sample rate are all set
to the secure defaults (or the operator declared an explicit
`Without*()` opt-out).

Request lifecycle and threat coverage:

| Step | Component | Threats defused |
|---|---|---|
| 1 | TLS terminator (out of kit) | OS-02 |
| 2 | `httpx.NewServer` ReadHeaderTimeout / MaxHeaderBytes | H-02 |
| 3 | `recover` middleware | H-01 |
| 4 | `metrics` middleware | (counts even if downstream panics — see H-01 interaction) |
| 5 | `requestID` middleware | (correlates audit log entry with request) |
| 6 | `tracing` middleware | (Builder validator caps sample rate ≤ 0.1 by default — D-04 cardinality) |
| 7 | `logging` middleware (with `secret.String`-aware logger) | H-13 |
| 8 | `csrf.New(...)` double-submit cookie + Origin/Referer allowlist (outer wedge — runs before auth so CSRF failures are not gated by auth) | H-04 |
| 9 | `csrf.RequireJSONContentType` | H-04 (defence-in-depth) |
| 10 | `timeout` middleware (30s default) | H-03 |
| 11 | `auth.Required` (JWT verify, issuer + audience checked) | T-01, T-02, H-09 |
| 12 | `idempotency.Middleware` (Redis-backed) | I-01, I-02, I-04 |
| 13 | `tenant.Required` (called by handler from JWT claim) | A2 cross-tenant prevention |
| 14 | `core/validate.Struct` on decoded body | H-14 |
| 15 | `infra/sqldb` Tx with TLS-enforced connection | D-01, D-02, D-03 |
| 16 | `crypto/envelope` for sensitive fields | D-05 |
| 17 | `observability/auditlog.Append` (HMAC-chained) — the auditlog layer is not tenant-aware; the service is expected to encode tenant identity in the Actor or Metadata fields of the `Event` before calling `Log` so the forensic trail carries the upstream tenant context | (forensic trail per asset [6]) |

If any single mitigation in steps 3–13 fails (panic, bad token,
replayed Idempotency-Key, missing CSRF, tenant ID absent), the
request stops at that step with an `apperror.Code` mapped to the
correct HTTP status by `httpx.WriteServiceError` — no downstream
side effect occurs, and no internal error string leaks (H-10).

### 6.2 Webhook receive (signed-request)

A different authentication model: no end-user, no JWT, no CSRF.
Authenticity is HMAC over canonical request bytes plus a server-side
nonce store.

```go
mux.Handle("/webhooks/billing", signedrequest.Verify(
    nonceStore,
    signedrequest.WithKey("billing", billingKey),
    signedrequest.WithMaxClockSkew(2*time.Minute),
)(handler))
```

Request lifecycle:

| Step | Component | Threats defused |
|---|---|---|
| 1 | `recover`, `metrics`, `requestID`, `tracing`, `logging` | H-01, H-13 |
| 2 | `maxbody` middleware sized for webhook payloads | H-02 |
| 3 | `signedrequest.Verify` reads `(timestamp, nonce, signature, signed-headers)` | W-01, W-02, W-03, W-04 |
| 4 | Canonical request bytes recomputed; HMAC compared in constant time | W-02, W-04 |
| 5 | Nonce checked against TTL'd Redis store; recorded if absent | W-01 |
| 6 | Handler runs with tenant ID derived from a signed claim in the body, not the URL | A4 (LLM cannot inject), L-02 |

The signed-request middleware **never** falls back to JWT auth;
mounting both on the same path is a documented anti-pattern.

### 6.3 Multi-tenant cache read

Demonstrates the §4.5 / R-01 control flow.

```go
key, err := tenant.Key(ctx, "user", userID, "profile")
if err != nil {
    return err
}
hit, err := infra.Cache.Get(ctx, key, &profile)
```

Threats and the mitigation chain:

| Step | Component | Threats defused / status |
|---|---|---|
| 1 | `tenant.Required(ctx)` returns error if context is unscoped | R-01 (precondition) |
| 2 | `tenant.Key(ctx, ...)` length-prefixes tenant and key parts | R-01 |
| 3 | `data/cache/rediscache.Get` reads from Redis with TLS | R-07 |
| 4 | Cache miss → upstream fetch with same tenant scope | R-01 transitive |

This flow still depends on service code using the helper or a tenant-scoped
wrapper. Code review and `kit-doctor` flag hand-written tenant prefixes.

### 6.4 Outbox-mediated downstream publish

The transactional outbox pattern is the kit's answer to dual-write
inconsistency (§4.11 / O-01). Two-step flow:

**Write path** — same Postgres transaction as the business write:

```go
writer := outbox.NewWriter(store, outbox.WithRequireTransaction(requireTx))
err := txRunner(ctx, func(txCtx context.Context) error {
    if err := createOrder(txCtx, order); err != nil { return err }
    return writer.Write(txCtx, outbox.WriteParams{
        Topic: "orders", RoutingKey: "order.created", MessageID: order.ID,
        MessageType: "order.created", Payload: payload,
    })
})
```

**Relay path** — separate goroutine started by `app.WithOutboxRelay`:

| Step | Component | Threats defused |
|---|---|---|
| 1 | Relay claims rows by atomically moving pending entries to processing state | O-03 (atomic claim) |
| 2 | For each claimed row, `Publisher.Publish` to broker | M-01 (mandatory ack) |
| 3 | On success, mark the row published and set `published_at` | (idempotent — see I-01 interaction) |
| 4 | On transient failure, `UPDATE outbox SET next_retry_at = NOW() + backoff(attempts)` | O-02 |
| 5 | After max attempts, row is marked failed and skipped by future relays | M-04 (no retry storm) |
| 6 | Retention cleanup reaps published and failed rows automatically on relay startup and cleanup ticks | O-04 |

The outbox table is a tier-1 asset (carries unredacted business
events). Access control is via DB grants: only the service's own
DB user can SELECT/INSERT/UPDATE; the relay never grants SELECT to
analytics users.

---

## 7. Out of scope (deliberately)

The kit does **not** defend against the following. Each is listed
with rationale; treating any as in-scope would require either an
upstream layer or a major redesign.

| # | Out-of-scope concern | Why |
|---|---|---|
| OS-01 | **L4/L3 DDoS** (TCP SYN flood, UDP amplification) | The kit operates at L7. Mitigation belongs in a CDN / WAF / cloud LB. |
| OS-02 | **TLS termination** | The kit can serve TLS (`httpx.NewServer` with cert/key), but production deployments terminate at the load balancer. We assume one of the two is configured. |
| OS-03 | **OS-level attacks** (kernel privilege escalation, container escape, hypervisor flaws) | Mitigation is the responsibility of the runtime (Kubernetes node hardening, container image hardening, CIS benchmarks). |
| OS-04 | **Side channels in Go's runtime** (timing attacks on `crypto/subtle.ConstantTimeCompare`, GC timing) | The kit uses `crypto/subtle` for HMAC comparisons and Argon2id for password hashing. Beyond these, side-channel resistance is the upstream Go team's responsibility. |
| OS-05 | **Hardware tampering / cold-boot attacks on running hosts** | Out of scope for any pure-Go library. KMS-managed keys mitigate the offline-disk variant (see asset [5]). |
| OS-06 | **Compromised KMS / Vault** | If the KMS root is compromised, every kit-encrypted record is at risk. The kit's job is to make rotation cheap; preventing KMS compromise is the deployment's job. |
| OS-07 | **Compromised CI runner during release** | Tracked as supply-chain risk in [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md). The kit cannot defend itself from a runner that has been root'd; CI hardening (ephemeral runners, SLSA attestation) is the deployment's responsibility. |
| OS-08 | **Application-layer authorisation logic** (RBAC / ABAC policy decisions) | The kit ships `httpx/authz` primitives but the policy is service code. Mis-modelled permissions are not a kit bug. |
| OS-09 | **Browser-side XSS via service-rendered HTML** | The kit serves JSON; if a service emits HTML, the service author is responsible for escaping. CSP nonce middleware is provided as a partial mitigation. |
| OS-10 | **Cryptographic primitives we do not implement** (custom asymmetric, post-quantum) | The kit uses `crypto/...` from Go stdlib and `paseto.v3` only. We do not ship our own primitives. |
| OS-11 | **Network policy between pods** | The kit assumes the deployment configures network policy so internal `:9090` is not reachable from the public internet. |
| OS-12 | **Backups / disaster recovery integrity** | Encryption-at-rest helpers (`crypto/envelope`, `storage/encryption`) cover the disclosure half. Integrity of backups themselves is a deployment concern. |

---

## 8. Known mitigation gaps

Items the threat model lists as a **gap** (i.e., a real threat
without a clear in-kit mitigation). Each becomes a follow-up audit
item. Severity uses the standard scale (CRITICAL / HIGH / MEDIUM / LOW).

GAP-01 (cost budgets), GAP-02 (safe redirects), GAP-03 (gRPC
default deadline), GAP-04 (internal gRPC health), GAP-05 (tenant
scaffold), GAP-06 (JWT revocation), GAP-07 (message-size overrides),
GAP-08 (`storagehttp/uploadsec` AV adapter), GAP-09 (outbox retention
cleanup), and GAP-10 (dependency allowlist plus heavy SDK boundary
gate) are closed in v2.0.0.

New gaps belong in the table below with severity and owner before
they are worked. Items below are doc-fidelity follow-ups surfaced by
audit round 4 (Agent J): the runtime mitigations are in place, but a
small code-level enhancement would strengthen the surface area.

| ID | Gap | Severity | Owner / next step |
|---|---|---|---|
| GAP-11 | `observability/auditlog.Event` has no first-class `Tenant` field; tenant identity is encoded by callers into `Actor` / `Metadata`. Consider promoting it to a typed field so `ValidateEvent` and queries can reason about tenant scope. | LOW | auditlog maintainer / next audit pass |
| GAP-12 | `infra/messaging/buffered_publisher.go` returns a plain `fmt.Errorf` for buffer-full back-pressure. Exporting an `ErrBufferFull` sentinel would let services match the condition with `errors.Is` without string comparisons. | LOW | messaging maintainer / next audit pass |
| GAP-13 | `core/secret.String` implements `encoding.TextMarshaler` but not `encoding.BinaryMarshaler`. Adding the binary marshaler would close the (small) hole where a serialiser consults the binary contract before falling back to text. | LOW | core/secret maintainer / next audit pass |

---

## 9. Process for filing new threats

1. **Reproduce the threat against `main`.** A threat is a verifiable
   claim, not a hypothesis. If you cannot make the kit do the bad
   thing, write a failing test first.
2. **Place it in the right §4 sub-section.** If none fits, add a
   sub-section — kit areas are not closed.
3. **Identify the adversary** (A1–A6) and the affected asset (1–8).
4. **Specify the mitigation.** "Service author has to be careful"
   is not a mitigation. Concrete defaults, refuse-to-misconfigure
   panics, or interface invariants are.
5. **Open a PR** that updates this file (the §4 sub-section and the
   §8 gap list as appropriate) and a test that demonstrates the
   mitigation. CI's `vuln.yml` and `sbom.yml` runs gate the merge.
6. **For HIGH/CRITICAL findings**, follow the response SLO in
   [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md) §"Vulnerability response".

---

*Cross-references in this document use repo-relative paths so they
remain valid when the file is read inside `docs/audit/`.*

---

## 10. Appendix: STRIDE coverage matrix

A compact view of how the §4 threats map to the STRIDE categories.
Each row references one of the threat IDs above; the columns are
the standard six. Cells marked ✅ have a concrete in-kit
mitigation; ⚠ marks a known gap (cross-referenced to §8).

| Threat ID | Spoof | Tamper | Repudiate | Info-disc | DoS | Elev-priv |
|---|---|---|---|---|---|---|
| H-01 panic | – | – | – | – | ✅ | – |
| H-02 slowloris | – | – | – | – | ✅ | – |
| H-03 timeout | – | – | – | – | ✅ | – |
| H-04 CSRF | – | ✅ | – | – | – | ✅ |
| H-05 sec-headers | ✅ | ✅ | – | ✅ | – | – |
| H-06 CSP nonce | – | ✅ | – | ✅ | – | – |
| H-07 spoof XFF | ✅ | ✅ | – | – | ✅ | – |
| H-08 CORS | ✅ | – | – | ✅ | – | ✅ |
| H-09 unauth route | ✅ | – | – | ✅ | – | ✅ |
| H-10 verbose err | – | – | – | ✅ | – | – |
| H-12 redirect | – | – | – | ✅ | – | ✅ |
| H-13 log inject | – | ✅ | ✅ | ✅ | – | – |
| H-14 mass-assign | – | ✅ | – | ✅ | – | ✅ |
| G-01 grpc panic | – | – | – | – | ✅ | – |
| G-02 grpc unauth | ✅ | – | – | ✅ | – | ✅ |
| G-03 stream flood | – | – | – | – | ✅ | – |
| G-05 grpc health | – | – | – | ✅ | – | – |
| M-01 unroutable | – | ✅ | ✅ | – | – | – |
| M-02 schema drift | – | ✅ | – | – | – | – |
| M-03 transient ack | – | ✅ | – | – | – | – |
| M-05 broker outage | – | – | – | – | ✅ | – |
| M-06 debug auth | ✅ | ✅ | – | ✅ | – | ✅ |
| M-07 plaintext AMQP | ✅ | ✅ | – | ✅ | – | – |
| M-08 message size | – | – | – | – | ✅ | – |
| D-01 plaintext PG | ✅ | ✅ | – | ✅ | – | – |
| D-03 cred leakage | – | – | – | ✅ | – | ✅ |
| D-04 pool DoS | – | – | – | – | ✅ | – |
| D-05 backup leak | – | – | – | ✅ | – | – |
| R-01 tenant collision | ✅ | ✅ | – | ✅ | ✅ | ✅ |
| R-02 lock split | – | ✅ | – | – | – | ✅ |
| R-03 idem replay | – | ✅ | – | – | – | – |
| R-04 idem TTL=0 | – | – | – | – | ✅ | – |
| R-05 queue race | – | ✅ | – | – | – | – |
| R-07 plaintext Redis | ✅ | ✅ | – | ✅ | – | – |
| R-08 fixed-window | – | – | – | – | ✅ | – |
| R-09 mem cache OOM | – | – | – | – | ✅ | – |
| S-01 path traversal | – | ✅ | – | ✅ | – | ✅ |
| S-04 upload bypass | – | ✅ | – | – | ✅ | – |
| S-05 SSRF | ✅ | – | – | ✅ | – | ✅ |
| S-06 file disclosure | – | – | – | ✅ | – | ✅ |
| T-01 alg=none | ✅ | ✅ | – | – | – | ✅ |
| T-02 issuer | ✅ | – | – | – | – | ✅ |
| T-04 revocation | ✅ | – | – | – | – | ✅ |
| T-05 paseto purpose | ✅ | ✅ | – | – | – | ✅ |
| T-06 key in env | – | – | – | ✅ | – | – |
| W-01 replay | – | ✅ | – | – | – | ✅ |
| W-02 header strip | – | ✅ | – | – | – | ✅ |
| W-03 clock skew | – | ✅ | – | – | – | – |
| I-01 retry dup | – | ✅ | – | – | – | – |
| I-02 fingerprint | – | ✅ | – | ✅ | – | – |
| I-04 pgstore split | – | ✅ | – | – | – | – |
| L-01 budget | – | – | – | – | ✅ | – |
| L-02 prompt-inj | ✅ | ✅ | – | ✅ | – | ✅ |
| L-04 leader-only | – | ✅ | – | – | ✅ | – |
| L-05 buffer OOM | – | – | – | – | ✅ | – |
| O-01 dual-write | – | ✅ | – | – | – | – |
| O-02 retry storm | – | – | – | – | ✅ | – |
| O-03 dup claim | – | ✅ | – | – | – | – |
| O-04 table bloat | – | – | – | – | ✅ | – |
| P-01 pprof public | – | – | – | ✅ | ✅ | – |
| P-02 metrics public | – | – | – | ✅ | – | – |

The intent is not 100% per-cell coverage — STRIDE classes overlap,
and many threats have a primary class with secondary effects. The
matrix is useful for showing auditors that every kit area has been
considered through every STRIDE lens.

---

## 11. Appendix: revision history

| Date | Theme | Change |
|---|---|---|
| 2026-04 | (pre-Theme-5 audit) | Original audit landed as per-package implementation notes; threat surface implicit, no consolidated doc. |
| 2026-05 | Theme 5 | This document created. STRIDE coverage matrix populated from §4. Initial gap list (GAP-01..GAP-10) filed. |
| 2026-05 | Theme 6+ hardening | GAP-01, GAP-02, GAP-03, GAP-04, GAP-05, GAP-06, GAP-07, GAP-08, GAP-09, and GAP-10 closed by cost-budget primitives, `httpx.SafeRedirect`, `grpcx` default deadlines, internal-only gRPC health, `cmd/kit-new -tenant`, `jwtutil` revocation checks, cross-backend message-size limits, `uploadsec/clamav`, outbox self-managed retention cleanup, direct dependency allowlist CI, and heavy optional SDK boundary CI. |
| 2026-05 | Audit round 4 doc-fidelity sweep | Aligned §4.1 H-04, §4.1 H-14, §4.4 D-04, §4.5 R-07, §4.5 R-09, §4.7 T-02, §4.10 L-05, §5.1, §5.2, §5.3, §5.5, and §6.1 prose with the implementing code (function names, struct fields, opt-out shapes); filed GAP-11 (`auditlog` first-class tenant field), GAP-12 (export `ErrBufferFull`), and GAP-13 (secret `BinaryMarshaler`) as LOW-severity follow-ups. |

Future updates: amend this table whenever §4 acquires a new threat
ID, §6 acquires a new walk-through, or §8 closes a gap. The
revision history is intentionally brief; detailed implementation history lives
in `git log`.
