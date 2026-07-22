# Platform Foundations Roadmap

## Decision

`rho-kit` remains a provider-neutral Go service substrate. It owns the
mechanisms that let a new service become production-shaped with minimal
application code. It does **not** own a central identity service, user
management, deployment control plane, or a provider-specific Auth0/Firebase/
Ory SDK.

The near-term goal is to close the reusable boundaries that every serious
service otherwise has to rebuild:

1. durable inbound message processing;
2. contract publication and compatibility verification;
3. production OIDC/authentication composition;
4. fleet-level adoption and upgrade evidence; and
5. proof that the composed path behaves correctly under failure.

Later, a separately deployable `rho-identity` product may consume these
seams. Its dependency direction is `rho-identity -> rho-kit`, never the
reverse.

## Architecture boundaries

| Layer | Owns | Explicitly does not own |
|---|---|---|
| `rho-kit` | OIDC/JWT verification, principal projection, auth middleware, contract formats/checks, inbox/outbox mechanics, runtime safety, diagnostics | User records, password/MFA/WebAuthn/recovery, provider tenancy, organisation admin UI |
| Downstream service | Domain schema, policy model, business transaction, contract compatibility intent, deployment topology | Reimplementing durable message handling or generic OIDC plumbing |
| Future `rho-identity` | Users, organisations, memberships, invitations, provider administration, identity lifecycle | A required dependency of kit packages |
| External identity provider | Credential ceremony and federation (for example OIDC provider, passwordless, MFA) | Product-domain membership and authorization policy |

## Non-goals

- Do not create Auth0, Firebase, Ory, Keycloak, or Cognito SDK wrappers.
  Support them through standard OIDC/JWKS profiles and compatibility fixtures.
- Do not claim exactly-once delivery. The target is atomic local side effects
  plus safe at-least-once redelivery.
- Do not make the base `app/v2` module depend on a central identity service,
  Redis, Postgres, or a deployment environment.
- Do not turn `kit-doctor` into a substitute for service integration tests.
- Do not add an all-purpose internal service mesh or deployment product.

## Workstreams and ordering

### 0. Shared contracts and decision records

Before adding packages, write short ADRs/RFCs for the following decisions:

1. Canonical principal fields: subject, actor, actor kind, tenant, scopes,
   permissions, and provider claims. Define which values are stable IDs versus
   display metadata, and which are safe to place in logs/audit events.
2. Contract artifact shape: identity, owner, semantic version, transport,
   schema/spec bytes, compatibility policy, and deprecation metadata.
3. Inbox transaction boundary: what the caller must place in the callback,
   what is atomically committed, when a broker ACK is permitted, retention,
   and duplicate semantics.
4. The supported deployment baseline for the production reference service.

Exit gate: each decision is narrow enough to implement independently and is
reviewed with at least one downstream-service example. No public API is added
in this step.

### 1. Contract lifecycle foundation

**Problem:** `httpx/openapigen` produces a useful OpenAPI spec and
`infra/messaging` validates versioned JSON schemas, but neither gives the
fleet a published artifact, compatibility verdict, or consumer proof.

**Scope the first release to HTTP OpenAPI and JSON event schemas.** Do not
bring protobuf/gRPC code generation into the first slice.

Deliverables:

1. Define a small provider-neutral contract manifest and on-disk artifact
   layout. A service can commit/export its OpenAPI document and event schemas
   with type/version/owner metadata.
2. Add a contract command (name to be chosen after the ADR) that can:
   validate an artifact, compare a proposed artifact with a named baseline,
   and emit a machine-readable verdict for CI.
3. Start with conservative, explainable compatibility rules: removed HTTP
   operation/required response field; narrowed accepted request input; removed
   event field/required event field; changed event type or schema version
   without an explicit transition. Unsupported constructs must fail closed or
   require an explicit reviewed waiver, never silently pass.
4. Connect `openapigen` and the messaging schema registry through adapters;
   preserve their existing public APIs and avoid making HTTP depend on a
   message broker.
5. Add producer/consumer contract fixtures: an old consumer with a new event,
   a new consumer with an old event, and an HTTP client contract sample.

Exit gate:

- a breaking change fails in CI with an actionable reason;
- a compatible additive change passes;
- an explicit deprecation/waiver is visible in the generated report;
- generated artifacts and runtime validators agree on one fixture per
  transport.

### 2. Durable inbox plus outbox composition

**Problem:** outbound atomicity is covered by `infra/outbox`; inbound handlers
are told to be idempotent but each service must manually construct the durable
deduplication and transaction boundary.

Deliverables:

1. Add a minimal inbox contract and a Postgres implementation. Its primary API
   must execute a callback within the caller's database transaction after a
   unique, durable claim on `(consumer, message_id)`.
2. Make the transaction callback compatible with `outbox.WithTx`/`RequireTx`,
   so one incoming message can atomically record the inbox receipt, mutate
   domain state, and enqueue outgoing events.
3. Expose duplicate as a normal outcome, not an error. Only ACK a broker
   delivery after the transaction commits. Failed callbacks must leave a retry
   path; retention/pruning must be explicit and observable.
4. Publish migrations through `kit-migrate`, metrics/health diagnostics, and a
   conformance suite usable by future durable backends.
5. Add a real-broker + Postgres integration example. It must show the full
   `consume -> inbox transaction -> domain write -> outbox relay` path.

Exit gate:

- kill/retry at each boundary proves no committed duplicate domain side effect;
- duplicate delivery produces no second mutation or outgoing event;
- failed work redelivers; an ACK cannot precede the local commit;
- concurrent replicas contend safely on the same delivery ID.

### 3. Provider-neutral authentication composition

**Problem:** JWT verification, browser OIDC login, API keys, PASETO, mTLS, and
identity helpers exist, but a production app still hand-wires the pieces and
`auth/oauth2` has only in-memory session/state stores.

Deliverables:

1. Add durable session and state-store adapters, starting with Redis. Add a
   Postgres adapter only if a concrete service needs database-only operation;
   do not add both merely for symmetry.
2. Define `security/identity`'s canonical `Principal` projection and a
   declarative claim-mapping profile. It must represent human, API-key,
   OAuth-client, and service identities without provider-shaped fields leaking
   into business handlers.
3. Add an opt-in `app/oidc` composition module for browser/BFF services:
   OIDC login/callback/logout routes, durable stores, secure cookies, lifecycle
   ownership, and principal middleware. Keep API-resource JWT verification
   (`app/jwt` + auth middleware) as the smaller alternative.
4. Add a lifecycle-managed client-credentials token source/cache for outbound
   service-to-service OAuth. It must preserve caller deadlines, refresh before
   expiry with single-flight behaviour, redact credentials, and expose health
   and metrics.
5. Provide compatibility fixtures—not vendor modules—for generic OIDC,
   Auth0, Keycloak/Ory, Cognito, and Firebase JWT verification. Document the
   Firebase browser-login distinction clearly.
6. Make principal projection feed HTTP, gRPC, audit, and `authz.Decider`
   uniformly. Preserve existing context/identity public surfaces during the
   migration.

Exit gate:

- a two-replica browser-login test survives restart and callback replay;
- issuer/audience/nonce/PKCE/cookie misconfiguration fails closed;
- the same authenticated principal reaches HTTP, gRPC, audit, and OpenFGA;
- client-credentials refresh neither stampedes nor leaks a token into logs;
- the default resource-API path remains only a few explicit lines.

### 4. Fleet adoption and upgrade evidence

**Problem:** `kit-catalog` can inventory module versions, and `kit-doctor`
finds local misuse, but neither creates an actionable fleet baseline.

Deliverables:

1. Extend `kit-catalog` with a version/support report: module versions,
   deprecated API exposure where statically detectable, required release
   baseline, and recommended upgrade order.
2. Give `kit-doctor` a stable JSON/SARIF-like output and suppression inventory:
   owner, reason, expiry/review date, and whether a suppression changes a
   security posture.
3. Add optional checks for contract artifacts, generated artifact drift,
   production-ineligible OAuth memory stores, and unsafe missing principal
   mapping. Rules should be added only when false-positive behaviour is
   understood and a safe suppression story exists.
4. Create one CI recipe downstream services can adopt incrementally:
   `test -> doctor -> contracts -> migration drift -> release baseline`.

Exit gate:

- a fleet report names concrete services/modules and upgrade order;
- all policy findings are machine-readable and attributable;
- a downstream repo can adopt the checks without changing its runtime
  architecture.

### 5. Production-shaped reference and scaffold

**Problem:** existing examples teach individual compositions well, but most
intentionally use in-process identity/broker/storage components.

Deliverables:

1. Add one production-shaped reference service, not a second framework. It
   should use OIDC/JWT resource authentication, principal projection,
   authorization, Postgres, inbox, outbox, migrations, health/metrics/tracing,
   and a real broker under Docker-backed integration tests.
2. Extend `kit-new` with an explicit profile that scaffolds this composition.
   Generated source must be minimal and readable; all default security choices
   must be visible rather than hidden in templates.
3. Add deployment-neutral manifests/config examples only where they prove the
   startup, migration, readiness, shutdown, and secret-rotation contract.
   Kubernetes/Helm distribution is not required for the first version.

The reference may be materialized from the checked-in `kit-new -production`
profile instead of duplicating a static service tree, provided the generated
tree is compiled as a downstream module and its real-dependency integration
suite remains executable from this repository.

Exit gate:

- a new service can start from the scaffold, pass its integration suite, and
  expose a compatible contract artifact without hand-copying glue;
- the reference proves real dependency lifecycle rather than just compiling.

### 6. Composed resilience verification

**Problem:** individual packages are thoroughly tested, but a fleet needs
evidence about the composed failure behaviour.

Deliverables:

1. Build a reusable integration test harness around the reference service and
   real Postgres/broker/Redis dependencies.
2. Exercise dependency outage, duplicate delivery, poison message, schema
   mismatch, expired/rotated key, deployment shutdown, in-flight drain, and
   migration contention scenarios.
3. Assert product-visible outcomes: status/error code, retry/DLQ behaviour,
   inbox/outbox state, audit entry, metric, trace, readiness, and recovery.
4. Add bounded benchmark/regression cases for high-throughput inbox/outbox
   processing only after correctness behaviour is pinned.

Exit gate:

- every named failure scenario has an executable test with a documented
  operator-visible outcome;
- the suite runs in release-candidate CI without indefinite waits;
- performance claims are based on realistic message and transaction shapes.

## Delivery sequence

Ship this as small, releasable vertical slices:

1. Workstream 0 ADRs plus a contract artifact prototype and compatibility
   fixtures.
2. Contract lifecycle v1 (HTTP + JSON event schemas) and its CI command.
3. Postgres inbox v1 plus one end-to-end consumer/outbox reference test.
4. Durable OAuth stores and canonical principal projection.
5. `app/oidc` and outbound client-credentials composition.
6. Fleet reports/doctor integration, then the production reference/scaffold.
7. Compose the resilience suite around the now-real reference path.

Do not combine inbox, OIDC, and fleet diagnostics into one release. Each has
its own schema/API/lifecycle compatibility risk and should receive a focused
design review and release note.

## Definition of done for the programme

The programme is complete when a new web/API/worker service can adopt a
documented scaffold, select browser OIDC or resource JWT authentication,
receive a canonical principal, verify its transport contracts in CI, process
at-least-once messages without rebuilding an inbox, publish side effects via
an outbox, and demonstrate the expected recovery behaviour against real
dependencies. Its application code should contain domain decisions, not
security or delivery plumbing.
