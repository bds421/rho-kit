# rho-kit v2.0.0 Deep Review Round 2 - 2026-05-14

Repo state reviewed:

- HEAD: `b0ae9e1c0fb7cfd9c35b6e15da57672c008ef1c5`
- Worktree: dirty, with 53 tracked files changed and new audit/NATS credential files.
- Release inventory: `RELEASE_MODE=all make release-plan` reports 73 workspace modules across 6 dependency levels.
- Review scope: recent waves 31b-36 plus adjacent public API, rotation, metrics, audit-log, middleware, KMS, release-evidence, and operational-readiness surfaces.

Verdict: **yellow, not tag-ready**. The broad test and lightweight release gates pass, but there are still release-blocking contract/correctness issues and several documentation/Prometheus contract contradictions that should be fixed before freezing 2.0.

## Commands Run

```bash
git diff --check
make check-dependency-allowlist
RELEASE_MODE=all make release-plan
make check-operational-readiness
make test
```

Results:

- `git diff --check`: passed.
- `make check-dependency-allowlist`: passed, 59 approved direct external dependencies.
- `RELEASE_MODE=all make release-plan`: passed, 73 modules, 6 dependency levels.
- `make check-operational-readiness`: passed, 73 modules covered.
- `make test`: passed for all workspace modules.

## Findings

### R2-001 - auditlog chain verification depends on timestamp order, not append order

Severity: **release blocker**

Files:

- `observability/auditlog/auditlog.go:187`
- `observability/auditlog/auditlog.go:416`
- `observability/auditlog/auditlog.go:562`
- `observability/auditlog/auditlog.go:624`
- `observability/auditlog/memory.go:98`
- `observability/auditlog/chain.go:102`

The `Store.Query` interface says stores return events ordered by timestamp descending. `Logger.LogE` preserves a non-zero caller-supplied timestamp, so append order and timestamp order are not guaranteed to match. `Logger.VerifyChain` then streams `Store.Query(Filter{}, ...)` and validates HMAC links as if the stream were newest append first. The bundled memory store returns reverse insertion order instead, contradicting the public `Store` contract, so the tests pass while a durable store that follows the documented contract can reject valid chains whenever events are backfilled or timestamps skew.

This makes the audit-log `Store` interface internally inconsistent at the exact point where tamper-evidence matters.

Fix before tag: separate append-order chain scanning from user-facing query order. Add a store method such as `RangeChain(ctx, cursor, limit)` or persist a monotonically increasing sequence and have `VerifyChain` read by that sequence. Keep `Query` for timestamp/list views.

Tests to add:

- Append two valid events with timestamps intentionally out of append order.
- Verify that `Logger.VerifyChain` succeeds when the store scans append order.
- Verify that a store sorting by timestamp for `Query` still satisfies `Logger.List` without being used for chain validation.

### R2-002 - ReloadingClientTLS can skip hostname verification when ServerName is empty

Severity: **high**

File: `security/netutil/tls_reload.go:362`

`ReloadingClientTLS` sets `InsecureSkipVerify: true` and replaces verification with `VerifyConnection`. The custom verification passes `DNSName: state.ServerName` into `x509.Verify`. If a caller uses the returned config with a raw TLS client or an SDK that does not populate `ServerName`, `state.ServerName` is empty and `x509.Verify` validates the chain without hostname verification.

That is weaker than the normal Go TLS client path, where an empty server name is usually a configuration error unless verification is disabled.

Fix before wiring this into the Builder or adapters:

- Fail closed when `state.ServerName == ""`, or
- require a server name option on the helper, for example `ReloadingClientTLS(src, WithServerName(name))`, and clone it per target where SDKs do not set it.

Tests to add:

- A reloading client config with no server name must fail against a cert for the wrong host.
- A config with a valid server name must pass.

### R2-003 - hot TLS rotation exists as a helper but is not wired into the golden path

Severity: **high**

Files:

- `security/netutil/tls.go:193`
- `security/netutil/tls_reload.go:302`
- `app/builder.go:957`
- `app/httpclient_module.go:39`
- `docs/release/OPERATIONAL_READINESS_V2.md:35`

The repo now has `TLSConfig.Reloading`, `ReloadingServerTLS`, and `ReloadingClientTLS`, but the Builder still calls `b.cfg.TLS.ServerTLS(...)`, and the default HTTP client module still calls `mc.Config.TLS.ClientTLS()`. Both load static certificate material. `OPERATIONAL_READINESS_V2.md` still says TLS material is a rolling-restart contract for v2.0.0.

So the implementation, release docs, and desired "top-tier rotation" goal are no longer aligned. The helper API is useful, but services following the documented golden path do not get hot TLS/mTLS rotation.

Fix before tag if hot rotation is required for 2.0:

- Add a Builder-level TLS source option or default to `TLSConfig.Reloading(...)` when TLS env vars are present.
- Register the source with lifecycle shutdown and readiness/metrics for reload failures.
- Thread the same source into public HTTP, gRPC receivers, and the default outbound HTTP client where applicable.
- Update `credential-rotation.md`, `OPERATIONAL_READINESS_V2.md`, and `AGENTS.md` to state the actual contract.

### R2-004 - httpx/sign WrapKeyStore can block startup on remote secret stores

Severity: **high**

Files:

- `httpx/sign/sign.go:181`
- `httpx/sign/sign.go:195`
- `httpx/sign/sign.go:235`
- `httpx/sign/sign.go:308`

`WrapKeyStore` is meant for a reloading or provider-backed signing key store. It validates the store during construction by calling `keys.CurrentKeyID(context.Background())`. That has no caller cancellation or deadline. Per-request signing correctly uses `req.Context()`, but construction can hang indefinitely or force an unavailable remote secret manager to become a startup hard-block without a bounded context.

Fix before tag:

- Add `WrapKeyStoreContext(ctx, base, keys, opts...)`, or
- remove remote I/O from construction and validate lazily on the first request using the request context, or
- add an explicit validation option with a bounded timeout.

Tests to add:

- A key store blocked on context must not hang construction forever.
- First request cancellation must propagate to key resolution.

### R2-005 - NATS credential provider bridge logs raw errors and caches empty credentials

Severity: **high**

Files:

- `infra/messaging/natsbackend/credentials.go:30`
- `infra/messaging/natsbackend/credentials.go:38`
- `infra/messaging/natsbackend/credentials.go:49`
- `infra/messaging/natsbackend/credentials.go:70`
- `infra/messaging/natsbackend/credentials.go:78`
- `infra/messaging/natsbackend/credentials.go:87`

The new NATS provider bridge is the right direction for rotation, but two edge cases are still unsafe:

1. Provider errors are logged with raw `slog.Any("error", err)`. Secret managers often include secret paths, vault namespaces, mount names, token hints, or backend URLs in error messages. Most surrounding broker/storage code uses redaction helpers for errors.
2. A provider returning `("", "", nil)` or `("", nil)` is treated as success and cached. A transient provider bug can replace a good cached credential with an empty one, after which reauth fails even though the previous credential was available.

Fix before tag:

- Log provider failures through `redact.Error` or a typed sanitized error.
- Treat empty token/user/password as an error and preserve the last good cached value.
- Add tests for empty-success provider returns.

### R2-006 - AMQP/NATS route-label metric contract contradicts release docs and inline comments

Severity: **high**

Files:

- `infra/messaging/amqpbackend/metrics.go:63`
- `infra/messaging/amqpbackend/metrics.go:105`
- `infra/messaging/amqpbackend/metrics.go:139`
- `infra/messaging/natsbackend/metrics.go:45`
- `infra/messaging/natsbackend/metrics.go:83`
- `infra/messaging/natsbackend/metrics.go:111`
- `docs/release/RC_CHECKLIST_V2.md:379`

The code now defaults AMQP and NATS route labels to opaque hashes and exposes `WithRawRouteLabels()` for the v1 behavior. That may be the correct secure default. But the release checklist says the opposite: default behavior keeps raw labels and services opt into `WithOpaqueRouteLabels()`. The inline comments on both metrics structs also still claim identity/raw is the default, while the constructors set `labelRoute: opaqueRouteLabel`.

Prometheus metric names and label keys are not the whole contract. Label values are part of dashboard behavior and alert debugging. This must be frozen deliberately before 2.0.

Fix before tag:

- Decide the stable v2 default.
- If opaque-by-default is intended, update the release checklist, docs, comments, migration notes, and dashboard examples.
- If raw-by-default is intended, change the constructors and tests.

### R2-007 - Redis app module pool metrics ignore custom Redis registerers

Severity: **medium**

Files:

- `app/redis/redis.go:122`
- `app/redis/redis.go:134`
- `infra/redis/connection.go:141`
- `infra/redis/metrics.go:310`

`infra/redis.WithRegisterer` builds connection metrics on a custom registry. `infra/redis.StartPoolMetricsCollector` has `WithPoolMetrics` exactly so pool gauges can use the same metrics instance. But `app/redis.Module.Init` starts the pool collector without passing `WithPoolMetrics`, and `Connection` does not expose its metrics instance.

Result: app-level Redis command/health metrics can be on a custom registry while pool metrics silently go to the default registry. This breaks test isolation and multi-registry deployments.

Fix before tag:

- Expose the connection's metrics instance safely, or
- make `StartPoolMetricsCollector` accept the `Connection`, or
- add an `app/redis.WithRegisterer` option that wires both connection and pool metrics consistently.

### R2-008 - metric constructor standardization is overclaimed

Severity: **medium**

Files:

- `AGENTS.md:223`
- `docs/release/RC_CHECKLIST_V2.md:371`
- `data/cache/rediscache/cache.go:39`
- `infra/redis/metrics.go:40`
- `data/stream/redisstream/producer.go:36`
- `httpx/middleware/signedrequest/metrics.go:42`
- `observability/redmetrics/redmetrics.go:117`
- `observability/redmetrics/redmetrics.go:380`

The release checklist says every metric-producing package now exposes `NewMetrics(opts ...MetricsOption)` with canonical `WithRegisterer`, except where a component-level option owns the short name. The actual surface still has several package-specific registerer names: `MetricsWithRegisterer`, `WithProducerMetricsRegisterer`, `WithMetricsRegisterer`, `WithHTTPRegisterer`, and `WithBatchRegisterer`.

Some exceptions may be justified by same-package option collisions, but the release docs and AGENTS convention currently read as a stronger promise than the code delivers.

Fix before tag:

- Either complete the standardization, or
- explicitly document the exception taxonomy and add a small API inventory table so users know which spelling is stable.

### R2-009 - approval middleware docs reference APIs and defaults that no longer exist

Severity: **medium**

Files:

- `app/builder.go:496`
- `httpx/middleware/approval/doc.go:13`
- `httpx/middleware/approval/approval.go:206`

The Builder comment shows `approval.Middleware(..., approval.WithActorFromContext(auth.UserID))`, but `app/builder.go` imports `data/approval` as `approval`; the middleware package is `httpx/middleware/approval`, and the option is `WithActorExtractor`, not `WithActorFromContext`. The package doc also says tenant extraction uses a configured tenant header, while the code now defaults to `tenantFromContext`.

This is not just prose drift: it affects how services wire destructive-operation approval.

Fix before tag: update the snippet imports and option names, and rewrite the package doc to state that context tenant is the secure default and header extraction is opt-in.

### R2-010 - benchmark baseline evidence is stale against current HEAD

Severity: **release evidence**

Files:

- `docs/release/benchmarks/v2.0.0/MANIFEST.md:3`
- `docs/release/benchmarks/v2.0.0/MANIFEST.md:5`
- `docs/release/benchmarks/v2.0.0/MANIFEST.md:28`
- `docs/release/benchmarks/v2.0.0/MANIFEST.md:32`

The benchmark manifest was generated on 2026-05-12 from commit `5fb930d7ac5b206b6174be535fc4751a6c33d6b2`. Current HEAD is `b0ae9e1c0fb7cfd9c35b6e15da57672c008ef1c5`, and the worktree contains substantial API/metrics/rotation changes. The manifest also records benchmark rows for removed/renamed surfaces such as `httpx/reqsign` and `WithIDChecked`.

Fix before tag: regenerate baselines from a clean current release-candidate commit, or mark the existing files explicitly as historical/preliminary rather than canonical `kit-bench-gate` inputs.

### R2-011 - PASETO rotation docs are verifier-focused but read broader

Severity: **medium**

Files:

- `docs/ai/credential-rotation.md:20`
- `crypto/paseto/provider.go:125`
- `crypto/paseto/paseto.go:260`
- `app/paseto_module.go:18`

`crypto/paseto.Provider` hot-refreshes public verification keys. The signing side remains a static `V4PublicSigner` constructed from one private key. That may be a deliberate split, but the rotation matrix says "expose the new signing key while keeping old verification keys" without giving a kit-level signing-key provider, swapper, or Builder lifecycle hook.

For a top-tier library, the docs should not imply symmetric issuer/verifier rotation support when only verifier rotation is packaged.

Fix before tag:

- Either add a signer provider/swapper type for PASETO issuance, or
- rewrite the rotation matrix to say signing-key cutover is caller-managed and show the exact overlap sequence.

## Audited Clean Or Improved

These areas were checked in this round and did not produce a new finding:

- AWS/GCP/Azure/Vault envelope KEK adapters now reject mismatched key IDs before unwrap or constrain unwrap to the configured key family.
- `data/lock/pgadvisory.sessionLock.Extend` now performs a `SELECT 1` round trip instead of returning a no-op success.
- Redis Builder/module plaintext and passwordless guards now reject non-loopback unsafe configs unless explicitly opted out.
- `crypto/signing` key-store APIs are now context/error-aware, and static stores return copied key material.
- `data/actionlog` signing APIs are now context/error-aware and carry `SignatureKeyID`.
- MCP destructive tools now require explicit destructive configuration/gating.
- Outbox writer construction now requires a transaction check unless callers use an explicit opt-out constructor.
- Idempotency method enforcement now has an explicit opt-out instead of silently allowing missing method lists.

## Review Method Notes

This round intentionally checked code against claims, not just diffs. The most useful searches were:

- context detachment: `context.Background`, `context.WithoutCancel`
- credential/rotation APIs: `Provider`, `SecretSource`, `Credential`, `CertificateSource`, `Reloading`
- metrics contract drift: `NewMetrics`, `WithRegisterer`, `WithMetricsRegisterer`, `prometheus.Registerer`
- unsafe/default escape hatches: `AllowInsecure`, `Without`, `Trusted`, `return true, nil`
- release-evidence freshness: benchmark manifest source revisions and release-plan output

The review is still a source-backed engineering review, not a formal proof. The blockers above are confirmed enough that I would not tag v2.0.0 before resolving them.
