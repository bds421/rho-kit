# Platform foundations verification

This is the completion matrix for
[Platform Foundations Roadmap](../PLAN_PLATFORM_FOUNDATIONS.md). It records
the source of truth and executable evidence for every v1 boundary; it is not a
replacement for a downstream service's own integration tests.

| Roadmap area | Delivered mechanism | Executable evidence |
| --- | --- | --- |
| Decisions | ADRs 0001–0004 define principal, contracts, inbox, and deployment boundaries. | `docs/adr/` and the generated production profile are reviewed together. |
| Contracts | `cmd/kit-contract` validates manifests and compares OpenAPI 3.1/JSON Schema contracts, including visible waivers. | `go test ./cmd/kit-contract/...`; `infra/messaging/contract_fixture_test.go`; generated `make contracts`. |
| Durable delivery | Postgres inbox, transactional outbox, migration publication, health and metrics. `SchemaVersion` is persisted and relayed. | `go test -tags integration ./testing/integrationtest/outboxpg`. It covers duplicate delivery, rollback/redelivery, DLQ/schema mismatch, relay recovery, FIFO and concurrent replicas. |
| Browser OIDC | Durable Redis session/state stores and `app/oidc` routes/projected principal; memory stores require explicit test-only opt-in. | `go test ./app/oidc ./auth/oauth2/redis`; `go test -tags integration ./auth/oauth2/redis -run TestBrowserLoginReplicaContinuity_RealRedis`. |
| Resource/API identity | JWT/auth middleware and gRPC interceptor project one canonical `security/identity.Principal`; authorization and audit actor derive from it. | `go test ./security/identity ./httpx/middleware/auth ./grpcx/interceptor`. |
| Service OAuth | Lifecycle-managed client credentials source refreshes before expiry with deadline-preserving single-flight behavior, health, and metrics. | `go test ./auth/oauth2 -run ClientCredentials`. |
| Fleet evidence | `kit-catalog -report`; versioned `kit-doctor` JSON with suppressions, OAuth-store, contract-manifest, and runtime-schema-drift checks; incremental CI recipe. | `go test ./cmd/kit-catalog ./cmd/kit-doctor/...`; `go run ./cmd/kit-doctor -format json .`. |
| Production reference | `kit-new -production` emits explicit JWT/OpenFGA/Postgres/RabbitMQ/inbox/outbox/tracing/audit wiring, contracts, migrations and required secret configuration. | `go test ./cmd/kit-new -run TestScaffold_ProductionProfileGeneratedTreeBuildsAndPasses`. |
| Failure behavior | Real Postgres, RabbitMQ, and Redis testcontainers use bounded contexts. Audit, readiness/health, relay recovery, DLQ, retry, and contract failure paths are exposed through the profile and harness. | `testing/integrationtest/outboxpg/README.md`, `auth/oauth2/redis/replica_integration_test.go`, and `make ci-fast`. |

The profile deliberately offers two mutually exclusive authentication starts:
resource APIs use its small explicit JWT path; browser/BFF services compose
`app/oidc` with `auth/oauth2/redis`. Neither path introduces a dependency on a
future identity product or a provider-specific SDK.
