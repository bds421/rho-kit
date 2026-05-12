# Deep Library Review - 2026-05-12

Snippet status: shell commands are executable from the repository root.

This pass reviewed the v2.0.0 surface as a library-quality gate: do the APIs
behave according to their contracts under cancellation, missing identity,
provider errors, high cardinality, and other production edge cases? The target
bar is "boring under pressure": fail closed where security matters, make
callback lifecycles explicit, avoid misleading metrics, and keep hot helpers
from adding unnecessary tail latency.

## Reviewed Areas

- Leader election: public callback contract, app Builder integration,
  Postgres advisory-lock elector, Redis-lock elector, lost-lock cleanup, and
  shutdown semantics.
- KMS adapters: Azure Key Vault and Vault Transit KEK implementations,
  key-ID validation, context/AAD handling, redaction, nil-context handling,
  versioned unwrap checks, and provider-error mapping.
- Auth/security defaults: HTTP S2S auth, authz adapters, MCP strict audit
  actor/tenant checks, signed-request fail-closed paths, and missing-scope or
  missing-permission behavior.
- Approval/audit workflows: idempotent decisions, tenant-scoped listing,
  terminal-state transitions, execution markers, and audit metadata
  preservation.
- Observability and release evidence: storage provider metrics, Prometheus
  metric contracts, provider dashboards, benchmark baseline source metadata,
  and static anti-pattern scans for direct HTTP clients/servers or insecure
  transport defaults.
- Contended runtime helpers: keyed rate limiter cleanup under key-spray traffic.

## Findings Closed In This Pass

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| DLR-001 | High | `app.WithLeaderElection` acquired leadership and immediately returned from `OnAcquired`. The elector contract treats callback return as the end of a leadership term, so services using Builder-level leader election could repeatedly acquire and release leadership instead of holding it for the process lifetime. | The app leader module now blocks `OnAcquired` until the leader context is cancelled. `app` tests prove the callback does not return before cancellation. |
| DLR-002 | High | The Postgres and Redis electors could retry a new term after renewal/liveness loss before the previous `OnAcquired` work had exited. That permitted overlapping leader work inside one process after lock loss or reacquisition. | Both electors now cancel the term context, wait for `OnAcquired` to drain, then call `OnLost` synchronously before release/retry. Tests cover lost-handle cancellation and callback drain. |
| DLR-003 | Medium | `OnLost` was implemented as a `Run` defer, so it fired even when leadership was never acquired and did not model per-term cleanup. The public contract implied cleanup after leadership loss, but the code made it a final `Run` cleanup hook. | `OnLost` now runs once per acquired term only, after `OnAcquired` exits. Panics are redacted and surfaced as errors. Contract docs and tests were updated. |
| DLR-004 | Low | Keyed rate-limiter cleanup held the shard mutex while `Keys()` allocated a snapshot. Under high-cardinality key spray, cleanup could stall `AllowKey` longer than necessary. | Cleanup now snapshots keys outside the shard lock and re-locks only for bounded `Peek`/`Remove` work, matching the IP limiter pattern. |

## Explicit No-Finding Results

- S2S auth did not reproduce the v1 fail-open class. Permission and scope
  checks deny by default unless the verified S2S marker is present; mTLS
  identity requires an allowlist, verified chains, client-auth EKU checks, and
  the impersonation guard.
- `httpx/authz` denies invalid or missing subject/action/resource triples,
  nil deciders, memory-store misses, OpenFGA false responses, and policy
  errors. Panics from subject/resource extraction and policy evaluation are
  recovered and denied or failed as server errors rather than allowed.
- `httpx/mcp` strict audit is fail-closed for missing tenant attribution and
  missing or invalid actor attribution. Anonymous actors require the explicit
  `WithAllowAnonymousActor()` option.
- Azure Key Vault KEK support validates configured IDs and key URL segments,
  rejects deprecated RSA wrapping algorithms, redacts provider values in logs,
  requires versioned unwrap IDs, and checks host/key/version consistency when a
  full key ID is configured.
- Vault Transit KEK support validates mount/key configuration, uses exact
  key-ID matching on unwrap, copies context/AAD before use, base64-encodes
  context for Transit, and redacts provider values.
- Approval memory and Postgres stores preserve original decision metadata for
  same-direction idempotency, scope list queries by tenant, reject invalid
  create/decide/execute inputs, and treat terminal states consistently.
- Storage provider metrics now normalize expected not-found outcomes before
  recording operation errors. Benchmark baseline manifests now point at clean
  current source metadata instead of stale dirty-tree evidence.
- Static scans of production code did not find new direct `http.DefaultClient`,
  raw `ListenAndServe`, `InsecureSkipVerify`, or direct server-entrypoint
  violations outside intentional tests, fixtures, or verifier detections.

## Residual Risks And Follow-Ups

- Leader callbacks must still respect context cancellation. The electors now
  wait for callback drain to prevent overlap, which means a callback that
  ignores its context can block shutdown or re-election. That is the safer
  failure mode for v2.0.0; a future option could add an operator-visible drain
  timeout, but it would need very careful contract wording.
- Azure Key Vault and Vault Transit coverage is fake-client/unit coverage.
  Release confidence would improve with credential-gated live smoke tests in a
  private environment, but the public library should not require those secrets
  for normal CI.
- Provider dashboards and Prometheus metric names are stable enough for v2.0.0.
  Keep dashboard JSON, alert rules, and metric documentation in the same change
  whenever a metric label or bucket contract changes.

## Verification Recorded

```bash
go test ./...
go test -race ./...
```

The focused package checks above were run from:

- `app`
- `infra/leaderelection`
- `infra/leaderelection/pgadvisory`
- `infra/leaderelection/redislock`
- `httpx/middleware/ratelimit`

Additional reviewed-area package tests were run from:

- `crypto/envelope/azurekeyvault`
- `crypto/envelope/vaulttransit`
- `httpx/mcp`
- `httpx/middleware/auth`
- `httpx/authz`
- `data/approval`

Additional verification:

```bash
git diff --check
```
