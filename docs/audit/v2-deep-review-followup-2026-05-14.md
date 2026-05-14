# rho-kit v2.0.0 Deep Review Follow-up - 2026-05-14

Repo: `/Users/markusnissl/Developer/private/rho-kit`
Mode: source-backed review only; no source fixes implemented in this pass.
Verdict: RED for the v2 API freeze until the HIGH items below are fixed or explicitly accepted as release-blocking debt.

This artifact records completed review coverage and findings only.

## Current Tree Snapshot

Commands run from the repository root unless noted:

- `go work edit -json`: 73 workspace modules.
- `git ls-files '**/go.mod' go.mod | wc -l`: 73 module files.
- `git ls-files -z -- '*.go' | xargs -0 rg -n "^(func|type|const|var) [A-Z]|^func \\([^)]*\\) [A-Z]" | wc -l`: 8088 exported declaration/method/test hits.
- `git ls-files -z -- '*.go' | xargs -0 rg -n "^type [A-Z][A-Za-z0-9_]* interface" | wc -l`: 86 exported interface hits.
- `git ls-files -z -- '*.go' | xargs -0 rg -n "prometheus\\.|WithRegisterer|With.*Registerer|NewMetrics|MetricsOption" | wc -l`: 1179 metrics-contract hits.
- `make check-dependency-allowlist`: passed, 59 direct external dependencies approved.
- `make check-dependency-boundaries`: passed, 393 direct module edges reviewed.
- `make check-operational-readiness`: passed, 73 modules covered.
- `RELEASE_MODE=all make release-plan`: 73 selected modules across dependency levels 0-5.
- `go test -count=1 ./middleware/approval` from `httpx`: passed.
- `go test -count=1 ./...` from `infra/outbox`: passed.
- `go test -count=1 ./...` from `observability`: passed.
- `go test -count=1 ./actionlog ./actionlog/memory` from `data`: failed in `data/actionlog` tests; `actionlog/memory` passed.
- `GOFLAGS=-count=1 make test`: failed in `data/actionlog`; the current `SecretSource` interface migration is incomplete.
- `make check-release-team`: passed; `@bds421/security` exists and CODEOWNERS enforcement is configured.

Working tree state during this review included source/doc edits that were not made by this review pass:

- Modified tracked files: `data/actionlog/actionlog.go`, `data/actionlog/actionlog_test.go`, `docs/ai/messaging.md`, `docs/audit/THREAT_MODEL.md`, `httpx/middleware/approval/approval.go`, `httpx/middleware/approval/approval_test.go`, `infra/outbox/doc.go`, `infra/outbox/outbox.go`, `infra/outbox/outbox_test.go`, `infra/outbox/relay_test.go`, `observability/auditlog/auditlog.go`, `observability/auditlog/auditlog_test.go`.
- Untracked audit artifacts: `docs/audit/v2-api-freeze-hostile-review-2026-05-13.md`, `docs/audit/v2-deep-review-followup-2026-05-14.md`.
- Untracked root build outputs: `kit-bench-gate`, `kit-migrate`, `kit-new`, `kit-verify`.
- Untracked `task.md`.

## Completed Review Coverage

The review used broad mechanical inventories first, then manual inspection of the high-risk hits. Completed surfaces in this pass:

- Public API freeze footguns: panicking constructors, positional bool options, unsafe opt-outs, `Must*` exports, no-arg variadic options, and misleading safe-looking names.
- Secure defaults and caller-controlled trust boundaries: approval, idempotency, MCP destructive tools, S2S bypass helpers, pprof, KMS unwrap, outbox atomicity, request signing, and tenant propagation.
- Credential and secret rotation matrix: Postgres, Redis, AMQP, NATS, SFTP, JWT/JWKS, PASETO, TLS/mTLS reload, KMS adapters, HMAC signing, signed request verification, action log, and audit log.
- Prometheus contract freeze: constructor/option names, registerer behavior, default registerer behavior, duplicate registration behavior, route/topology label cardinality, and test registry support.
- Lifecycle/shutdown/context behavior: goroutine starters, tickers, close/drain paths, contextless APIs, memory-store cancellation, in-flight singleflight, queue heartbeat/process cancellation, and leader-election callback drain.
- Bounded work and resource amplification: `io.ReadAll`, `LimitReader`, body caps, pagination, list materialization, audit-chain verification, upload validators, and pre-auth request signing paths.
- Release hygiene: module counts, dependency gates, release-plan output, CODEOWNERS/team verification, license/security files, rehearsal evidence, dirty tracked files, and root build artifacts.

## HIGH Findings

### H2-001 - Approval docs still imply Builder auto-installs approval middleware

Files:

- `examples/agentic-service/internal/app/app.go:10-14`
- `examples/agentic-service/internal/app/app.go:66-68`
- `app/builder.go:484-495`
- `app/builder.go:1076-1084`
- `app/infrastructure.go:61-64`
- `httpx/middleware/approval/approval.go:32-36`
- `httpx/middleware/approval/approval.go:207-237`

The approval middleware's tenant default has moved in the right direction: current dirty source reads the tenant from `core/tenant` request context by default, and header trust is explicit via `WithTenantFromHeader`. The uncached approval package test passes.

The remaining release problem is documentation/API meaning. The agentic-service example still says production services should use `.WithApprovalStore` and that "the Builder composes the middleware chain correctly" / "let the Builder install the middleware on the public mux." The Builder code does not install approval middleware. `WithApprovalStore` only stores an `approval.Store` and exposes it as `Infrastructure.ApprovalStore`.

Failure scenario: a service follows the example comment, calls `WithApprovalStore`, and assumes destructive routes are gated. They are not; handlers must explicitly wrap `httpx/middleware/approval` or implement their own approval flow. That is the same class of security miss as a fail-open default, except it enters through the recipe layer instead of the constructor.

Fix direction: update the example and Builder docs to state exactly what Builder does and does not enforce. If the intended golden path is automatic approval middleware, add a Builder method that takes tenant/actor/action/resource extractors and installs the middleware. Otherwise, make the docs say handlers must wire approval explicitly.

### H2-002 - Secret rotation migration is incomplete: actionlog no longer builds

Files:

- `data/actionlog/actionlog.go:357-385`
- `data/actionlog/actionlog.go:717-729`
- `data/actionlog/actionlog.go:751-758`
- `data/actionlog/actionlog_test.go:710-827`
- `httpx/sign/sign.go:48-52`
- `httpx/sign/sign.go:281-284`
- `crypto/signing/keystore.go:26-30`
- `httpx/middleware/signedrequest/signedrequest.go:135-138`
- `httpx/middleware/signedrequest/signedrequest.go:338-340`
- `infra/messaging/natsbackend/natsbackend.go:111-123`
- `infra/messaging/natsbackend/natsbackend.go:344-350`

The actionlog `SecretSource` API has been moved in the right direction: it now has `CurrentKeyID(ctx) (string, error)` and `Resolve(ctx, keyID) ([]byte, error)`, plus `ErrSecretSourceUnavailable`. That is the production-grade shape this review was looking for.

The migration is incomplete in the current dirty tree. `GOFLAGS=-count=1 make test` and the focused `go test -count=1 ./actionlog ./actionlog/memory` fail in `data/actionlog` with stale tests:

```text
actionlog/actionlog_test.go:710:31: not enough arguments in call to SignEntry
actionlog/actionlog_test.go:712:31: not enough arguments in call to SignEntry
actionlog/actionlog_test.go:827:43: not enough arguments in call to VerifyEntry
```

Current examples from source: `SignEntry` and `VerifyEntry` now require context, but the tests still call `SignEntry(entry, secrets)` and `VerifyEntry(entry, nil)` in several places. The implementation direction is right; the tree is simply not release-buildable until all call sites/tests are migrated.

After actionlog is migrated, the broader HMAC/NATS rotation surface still needs the same production shape:

- `httpx/sign.KeyStore.CurrentKeyID() (string, []byte)` has no context and no error, so outbound signing cannot distinguish provider outage from an invalid/empty key.
- `crypto/signing.KeyStore` returns `(secret, bool)` for verification and `(keyID, secret)` for current key, again with no context/error.
- `signedrequest.KeyResolver` can return an error but receives no request context, so it cannot honor request deadlines or cancellation while resolving from Vault/KMS/Key Vault.
- NATS credential providers are `func() (string, string)` and `func() string`, with comments telling providers to avoid blocking but no API-level timeout/error channel.

Failure scenario: the release branch currently cannot build once uncached tests reach `data/actionlog`. Even after fixing that, unmanaged HMAC/NATS resolver APIs can still turn a temporary secret-manager outage into apparent tampering, permanent verification failure, or generic signing failure.

Fix direction: finish the actionlog migration first, including `SignEntry`, helper functions, tests, and any downstream packages. Then apply the same context/error/typed-error provider contract to outbound signing, signed-request verification, and NATS credentials. Preserve static adapters as convenience implementations.

### H2-003 - Prometheus contract is not stable enough to freeze as documented

Files:

- `AGENTS.md:221-224`
- `data/cache/compute_metrics.go:36`
- `observability/redmetrics/redmetrics.go:117-131`
- `observability/redmetrics/redmetrics.go:419-423`
- `infra/sqldb/metrics.go:27`
- `infra/sqldb/pgx/metrics.go:41`
- `security/jwtutil/metrics.go:36`
- `infra/messaging/buffered_publisher_metrics.go:53`
- `httpx/middleware/metrics/metrics.go:26`
- `infra/messaging/amqpbackend/metrics.go:65-109`
- `infra/messaging/amqpbackend/metrics.go:185-191`
- `infra/messaging/natsbackend/metrics.go:47-88`
- `infra/messaging/natsbackend/metrics.go:144-147`

The convention says all Prometheus metrics accept `prometheus.Registerer` via `WithRegisterer()` options. Current public APIs still expose several incompatible shapes:

- Positional constructors: `NewComputeMetrics(reg)`, `NewPoolMetrics(namespace, reg)`, `NewPoolStatsCollector(pool, reg, instance)`, `NewMetricsCollector(provider, reg, instance)`, `NewHTTPMetrics(reg)`.
- Package-prefixed options: `WithHTTPRegisterer`, `MetricsWithRegisterer`, `WithMetricsRegisterer`.
- Messaging-specific constructor names: `NewPrometheusMetrics(reg, publisherName)`, `WithPrometheusMetrics(reg, publisherName)`.
- `redmetrics.NewBatch(reg, name, opts...)` still takes registerer positionally.

There is also a default cardinality caveat in the changed AMQP/NATS metrics. `WithOpaqueRouteLabels` exists, but raw exchange/routing-key labels remain the default for backwards compatibility. The comments explicitly acknowledge the risk when services accidentally embed tenant or resource IDs into routes. For a v2 stable metrics contract, the safer default should be the frozen default, not an opt-in.

Fix direction: decide the v2 metrics shape now. A coherent contract would use `NewMetrics(opts ...MetricsOption)` and `WithRegisterer(reg prometheus.Registerer)` wherever possible, document any intentional exceptions, and default high-risk topology labels to bounded/opaque labels before dashboards are declared stable.

### H2-004 - MCP destructive flag is metadata-only and easy to over-trust

Files:

- `httpx/mcp/mcp.go:120-128`
- `httpx/mcp/mcp.go:656-670`

`WithDestructive(b bool)` records metadata and emits a schema hint, but the server does not enforce approval. The doc says callers must wire approval middleware separately. A positional bool also makes the destructive marker easy to omit or accidentally set false.

Failure scenario: a service registers a destructive MCP tool and relies on the flag for safety. Clients may prompt, but the server still executes the tool if approval middleware or an authorization hook was missed.

Fix direction: use a no-arg `WithDestructive()` marker and add server-side enforcement that refuses destructive calls unless an approval/authorization hook is configured, or rename the option so it is obviously metadata-only.

## MEDIUM Findings

### M2-001 - Memory stores still hide cancellation and bounded-list problems

Files:

- `data/actionlog/memory/memory.go:97`
- `data/actionlog/memory/memory.go:154`
- `data/actionlog/memory/memory.go:171-225`
- `data/actionlog/memory/memory.go:229-257`
- `data/approval/memory/memory.go:123-178`
- `observability/auditlog/memory.go:27`
- `observability/auditlog/memory.go:56`
- `observability/auditlog/memory.go:70`
- `observability/auditlog/memory.go:85`

Several in-memory stores ignore `context.Context` or check it only before allocating/sorting the full result set. The blast radius is lower than a production SQL backend, but these are public backends and tests built on them can mask cancellation bugs.

Fix direction: honor context before and during large scans, and collect bounded pages without sorting/materializing every matching row when the public query contract has a limit.

### M2-002 - WithRequiredMethods() can silently disable idempotency enforcement

Files:

- `httpx/middleware/idempotency/idempotency.go:207-223`
- `httpx/middleware/idempotency/idempotency.go:344-349`

`WithRequiredMethods(methods ...string)` accepts zero methods and replaces the safe default POST/PUT/PATCH map with an empty map. The middleware then bypasses every request because no method is required.

Failure scenario: an empty config slice or copy-pasted `WithRequiredMethods()` call silently disables idempotency for mutating requests.

Fix direction: panic on zero methods and add an explicitly named opt-out for services that intentionally want no required methods.

### M2-003 - Release checklist still mixes stale and current release evidence

Files:

- `docs/release/RC_CHECKLIST_V2.md:22`
- `docs/release/RC_CHECKLIST_V2.md:34-38`

Live release planning reports 73 modules. The checklist still says the operational check covers 67 modules and the tag plan creates 67 module-prefixed tags, while nearby rows already mention 73 modules.

Fix direction: refresh the checklist from current `RELEASE_MODE=all make release-plan` output and make stale module-count drift a release-doc gate.

### M2-004 - Dirty root build outputs and uncommitted release-critical edits block clean evidence

Evidence from `git status --short` during the review:

- Modified source/docs: actionlog, approval, outbox, and auditlog API/default/test changes, messaging docs, threat model docs.
- Untracked root binaries: `kit-bench-gate`, `kit-migrate`, `kit-new`, `kit-verify`.
- Untracked local task/audit files: `task.md`, the two audit markdown files under `docs/audit`.

This is not a source bug, but clean release evidence must come from a clean tree. The current tree contains release-critical behavior changes and root build artifacts that need to be intentionally committed, ignored, or removed before canonical evidence is captured.

## Verified Clean Or Already Fixed In Current Tree

These surfaces were inspected and are not carried as current findings in this artifact:

- Approval tenant resolution no longer trusts `X-Tenant-ID` by default in the current dirty source; it reads `core/tenant` context and uses `WithTenantFromHeader` for explicit header trust. `go test -count=1 ./middleware/approval` passed.
- Transactional outbox atomicity is now enforced by the safe constructor shape in the current dirty source: `NewWriter(store, txCheck)` requires a predicate, and `NewWriterWithoutTransactionCheck` is the explicit opt-out. `go test -count=1 ./...` from `infra/outbox` passed.
- Audit logger shutdown/verification issues are fixed in the current dirty source: `LogE` now treats a zeroed chain-key snapshot as `ErrLoggerClosed`, and `VerifyChain` streams pages instead of materializing the full ledger. `go test -count=1 ./...` from `observability` passed.
- AWS and GCP KMS unwrap now constrain envelope key IDs before decrypt. Azure Key Vault and Vault Transit already constrain key identity before decrypt.
- Envelope encryption AAD now has v3 length-prefixing while keeping v2 read compatibility.
- Redis queue heartbeat permanent failure now cancels the processing context, so local processing does not continue after the heartbeat dies.
- Compute cache foreground singleflight close handling was tightened; followers now observe cache close, and foreground work is drained.
- `redisstream.Message.WithHeader` now returns an error instead of panicking on invalid user-controlled values.
- PASETO/JWT key providers have context-aware refresh/staleness behavior and fail closed on stale verifier state.
- pprof defaults are not public-by-accident: mounting requires loopback/auth or an explicit unsafe opt-out.
- License, SECURITY.md, NOTICE, CODEOWNERS/team verification, dependency allowlist, dependency boundaries, and operational-readiness gates are present and pass in the current tree.
