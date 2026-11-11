# ADR 0004: Platform foundations and the production reference baseline

## Status

Accepted; delivered.

## Context

rho-kit is a provider-neutral Go service substrate. Every serious service
otherwise rebuilds the same boundaries: durable inbound message processing,
contract publication and compatibility checks, production OIDC and JWT
composition, fleet-level adoption evidence, and proof that the composed path
behaves under failure. The examples deliberately use in-memory components to
teach one concept each; a product team also needs one reference that proves
the combined production boundary without imposing a deployment platform.

## Decision

The kit owns the mechanisms that make a new service production-shaped with
minimal application code, and nothing of a product's domain:

| Layer | Owns | Does not own |
|---|---|---|
| rho-kit | OIDC/JWT verification, principal projection, auth middleware, contract formats and checks, inbox/outbox mechanics, runtime safety, diagnostics | user records, passwords/MFA/recovery, provider tenancy, organisation administration |
| a service | domain schema, policy model, business transactions, contract compatibility intent, deployment topology | durable message handling and OIDC plumbing of its own |
| an identity product | users, organisations, memberships, invitations, provider administration | being a dependency of any kit package (`identity -> rho-kit`, never the reverse) |
| an external identity provider | the credential ceremony and federation | product membership and authorization policy |

Non-goals: SDK wrappers for Auth0, Firebase, Ory, Keycloak or Cognito
(standard OIDC/JWKS profiles and fixtures serve them); exactly-once delivery
(the target is atomic local side effects plus safe at-least-once redelivery);
a base `app/v2` that depends on an identity service, Redis, Postgres or a
deployment environment; `kit-doctor` as a substitute for a service's own
integration tests; a service mesh or deployment product.

The production reference is deployment-neutral and Docker-testable. Its
dependencies are Postgres, Redis and NATS JetStream. It uses resource JWT
authentication with canonical principal projection, an authorization-decider
seam, Postgres migrations with a transactional inbox and outbox, health,
metrics, tracing, bounded graceful shutdown, secret and key rotation, and
contract artifact generation with compatibility checking. It is not a Helm
chart, service mesh, identity provider or deployment framework; Docker
Compose configuration in it proves dependency lifecycle only.

## What delivered it

| Area | Mechanism | Evidence |
|---|---|---|
| Decisions | ADRs 0001–0004 define principal, contracts, inbox and deployment boundaries | `docs/adr/`, reviewed with the generated production profile |
| Contracts | `cmd/kit-contract` validates manifests and compares OpenAPI 3.1/JSON Schema contracts, with visible waivers | `go test ./cmd/kit-contract/...`; `infra/messaging/contract_fixture_test.go`; `make contracts` |
| Durable delivery | Postgres inbox, transactional outbox, migration publication, health and metrics; `SchemaVersion` persisted and relayed | `go test -tags integration ./testing/integrationtest/outboxpg`: duplicate delivery, rollback and redelivery, DLQ and schema mismatch, relay recovery, FIFO, concurrent replicas |
| Browser OIDC | durable Redis session and state stores, `app/oidc` routes and projected principal; memory stores need an explicit test-only opt-in | `go test ./app/oidc ./auth/oauth2/redis`; `go test -tags integration ./auth/oauth2/redis -run TestBrowserLoginReplicaContinuity_RealRedis` |
| Resource identity | JWT middleware and gRPC interceptor project one `security/identity.Principal`; authorization and the audit actor derive from it | `go test ./security/identity ./httpx/middleware/auth ./grpcx/interceptor` |
| Service OAuth | a lifecycle-managed client-credentials source that refreshes before expiry with deadline-preserving single flight, health and metrics | `go test ./auth/oauth2 -run ClientCredentials` |
| Fleet evidence | `kit-catalog -report`; versioned `kit-doctor` JSON with suppressions, OAuth-store, contract-manifest and runtime-schema-drift checks | `go test ./cmd/kit-catalog ./cmd/kit-doctor/...`; `go run ./cmd/kit-doctor -format json .` |
| Production reference | `kit-new -production` emits explicit JWT/OpenFGA/Postgres/RabbitMQ/inbox/outbox/tracing/audit wiring, contracts, migrations and the required secret configuration | `go test ./cmd/kit-new -run TestScaffold_ProductionProfileGeneratedTreeBuildsAndPasses` |
| Failure behaviour | real Postgres, RabbitMQ and Redis containers under bounded contexts; audit, readiness, relay recovery, DLQ, retry and contract failure paths are exposed through the profile and harness | `testing/integrationtest/outboxpg/README.md`, `auth/oauth2/redis/replica_integration_test.go`, `make ci-fast` |

The profile offers two mutually exclusive authentication starts: resource
APIs use its explicit JWT path; browser/BFF services compose `app/oidc` with
`auth/oauth2/redis`. Neither depends on an identity product or a
provider-specific SDK.

## Consequences

- `kit-new` scaffolds a visible subset of this composition.
- The release gate has end-to-end evidence against real dependencies without
  making ordinary package tests depend on Docker.
- A service's application code holds domain decisions, not security or
  delivery plumbing.
