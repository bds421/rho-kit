# Semantic Invariant Review - 2026-05-11

Snippet status: shell commands are executable from the repository root.

This pass checked whether security-sensitive code behaves according to the
intended contract, not only whether APIs exist or tests pass. The emphasis was
on fail-open defaults, idempotency semantics, misleading legacy APIs, and docs
that could cause operators to wire the wrong thing before the v2.0.0 freeze.

## Reviewed Areas

- HTTP and gRPC auth middleware: service-to-service marker handling, required
  permissions/scopes, JWT claim parsing, and mTLS peer validation.
- Authz stores and adapters: missing deciders, absent tuples, false OpenFGA
  responses, invalid triples, and deny-by-default behavior.
- Tenant, budget, CSRF, CORS, signed-request, and rate-limit middleware:
  missing context, missing nonce stores, unsafe origin defaults, degradation
  policies, and duplicate/malformed header handling.
- Approval and action/audit workflows: idempotency, terminal states, tenant
  scoping, execution boundaries, and audit metadata integrity.
- Redis health/degradation helpers and release docs: whether omitted policy or
  contradictory docs make a service appear safer than it is.

## Findings Closed In This Pass

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| SI-001 | High | `data/approval.Store.Decide` treated same-direction idempotency as "latest decider wins". A replay or second operator could overwrite `DecidedBy` and `Reason` without changing the approval/rejection state, corrupting the audit record. | Same-direction `Decide` is now a pure no-op in memory and Postgres stores. The Store contract and tests preserve original `DecidedBy`, `Reason`, and `DecidedAt`. |
| SI-002 | Medium | `infra/redis.PerFeatureHealthChecks` accepted a nil `FeatureCheck.Policy`, which was not `FailFastPolicy` and therefore reported degraded/non-critical. Missing wiring silently became a permissive default. | Nil feature policy now panics at construction. Callers must choose `PassthroughPolicy`, `FailFastPolicy`, or an explicit `CustomPolicy`. |
| SI-003 | Medium | Release/audit docs contradicted the frozen API surface: AWS/GCP KMS modules are included in `API_FREEZE_V2.md`, while roadmap and release notes still said cloud KMS subpackages were deferred. | Docs now state that AWS/GCP/Azure/Vault Transit KMS adapters ship as frozen modules and only other provider adapters are deferred. |
| SI-004 | Low | `httpx/reqsign` and `httpx/sign`/`signedrequest` are both request-signing APIs but intentionally use different wire formats and key-ID header spellings. Without an explicit legacy note, consumers could assume interop. | `httpx/reqsign` is documented as the legacy self-contained format; new services are steered to `httpx/sign` plus `httpx/middleware/signedrequest`. |
| SI-005 | High | `httpx/mcp` strict audit required tenant attribution before dispatch but still defaulted missing or invalid actors to `anonymous`, so audited tool calls could execute without a real actor. | Strict MCP audit now refuses dispatch when actor attribution is missing or invalid. `WithAllowAnonymousActor()` is the explicit local/demo opt-out. |

## Explicit No-Finding Results

- S2S auth did not reproduce the old v1 fail-open pattern. The HTTP and gRPC
  permission/scope middleware bypasses only when the explicit S2S marker is in
  context; otherwise missing permissions or scopes deny.
- JWT verification is fail-closed for missing issuer/audience config unless the
  Builder opt-out is explicit. Malformed permissions/scopes are rejected rather
  than converted to an empty allow set.
- Authz denies on nil deciders, missing memory tuples, false OpenFGA responses,
  invalid relation triples, and adapter errors.
- Signed-request verification requires a nonce store and rejects missing,
  duplicate, oversized, or malformed signature headers before accepting a
  request.
- Tenant and budget middleware reject missing required context by default; their
  allow-missing paths require explicit opt-in.

## Follow-Up Policy

For v2.0.0, treat additional findings in this class as release blockers when
they meet either condition:

- A missing or nil dependency silently becomes allow, pass-through, degraded, or
  non-critical without an explicit caller option.
- An idempotent or retry-safe path mutates audit/security metadata in a way that
  can hide who made the original decision.
