# rho-kit v2 review round 3

Date: 2026-05-07
Branch: main
Reviewed HEAD: f26b775

Scope: current landed tree after the round-2 closure commits. I treated comments as non-authoritative and used the current source and command results as evidence.

Verification:

- `make test`: pass.
- `make lint`: fail. `cmd/kit-doctor/rules/helpers.go:139:2` has unused package variable `currentFile`.
- `GOWORK=off go list ./...` from `app`: pass as the main module, because local `replace` directives are honored there.

Only packages with findings are listed.

## app

### HIGH: `WithTenantBudget` can be bypassed on safe methods without a tenant

Evidence:

- `app/builder.go:489` exposes `WithMultiTenant(extractor, required bool)` but has no way to enable `tenant.WithRequiredOnSafeMethods`.
- `app/v2_modules.go:32-37` only passes `WithExtractor` and `WithRequired`.
- `app/builder.go:953-958` builds budget inside tenant, so the budget sees the tenant context only when the tenant middleware puts one there.
- `httpx/middleware/tenant/tenant.go:142-149` passes GET, HEAD, and OPTIONS through when no tenant is present and `requiredOnSafeMethods` is false.
- `httpx/middleware/budget/budget.go:42-49` returns no budget key when no tenant is in context, and `httpx/middleware/budget/budget.go:115-118` then passes the request through uncharged.

Impact:

`WithTenantBudget` does not actually enforce a per-tenant budget on every request mounted behind the builder. Expensive tenant-scoped GET endpoints can omit `X-Tenant-Id` and bypass both tenant enforcement and budget charging.

Fix:

When `WithTenantBudget` is configured with the default tenant key function, force `tenant.WithRequiredOnSafeMethods(true)` or add an explicit builder option that must be chosen. Health and readiness endpoints should be mounted outside the tenant/budget chain instead of relying on a silent safe-method bypass.

### MEDIUM: release module graph relies on local replaces for new modules

Evidence:

- `app/go.mod:12-14` requires `github.com/bds421/rho-kit/httpx v1.5.0` plus new zero-version middleware modules.
- `app/go.mod:136-144` uses relative `replace` directives for `core/tenant`, `httpx`, `httpx/middleware/budget`, and `httpx/middleware/signedrequest`.
- `app/builder.go:24` imports `github.com/bds421/rho-kit/httpx/middleware/tenant`, which is resolved locally by the replace while developing this repository.

Impact:

If `app` is published as a library module, downstream main modules do not inherit these `replace` directives. Consumers will resolve the required versions instead, so the app module can compile against different code than the landed workspace unless all referenced submodules are tagged and required at the matching v2 versions.

Fix:

Before release, remove development-only replaces from published module state or require tags that contain the exact landed code for every imported submodule.

## cmd/kit-doctor

### HIGH: `make lint` currently fails

Evidence:

- `make lint` fails with `cmd/kit-doctor/rules/helpers.go:139:2: var currentFile is unused`.
- `cmd/kit-doctor/rules/helpers.go:135-145` declares `currentFile` and assigns it in `SetCurrentFile`, but `rg currentFile cmd/kit-doctor/rules` finds no read.

Impact:

The landed tree cannot pass the documented `make lint` command. This blocks CI/release quality gates even though unit tests pass.

Fix:

Delete `currentFile` or use it. The parent map already carries the state needed by the chain helpers, so the simplest fix is to remove the unused variable and the assignment.

## core/tenant

### LOW: tenant IDs accept leading, trailing, and embedded spaces

Evidence:

- `core/tenant/tenant.go:73` forbids only `:`, `/`, newline, carriage return, tab, and NUL.
- `core/tenant/tenant.go:81-91` validates empty, length, and the forbidden byte set only.
- `httpx/middleware/tenant/tenant.go:48-57` passes the header value directly to `coretenant.NewID` without trimming or canonicalizing.

Impact:

`acme`, ` acme`, and `acme ` are distinct tenant IDs. That can create visually confusable log entries, Redis keys, metrics labels, and authorization or budget scopes.

Fix:

Either trim and reject if trimming changes the value, or move to a stricter tenant ID grammar such as ASCII slug, UUID, ULID, or another documented canonical format.

## crypto/envelope

### HIGH: KEK rotation can produce undecryptable envelopes

Evidence:

- `crypto/envelope/envelope.go:125-130` calls `e.kek.Wrap(ctx, dek)` and then separately reads `e.kek.KeyID()`.
- `crypto/envelope/envelope.go:227-232` has the same pattern in `Rewrap`.
- `crypto/envelope/kekstatic/kekstatic.go:111-128` captures the active key ID inside `Wrap` and binds it as AEAD AAD, but returns only the wrapped bytes.

Impact:

If the active KEK rotates between `Wrap` and the later `KeyID` call, the envelope header can record the new key ID while the wrapped DEK was authenticated under the old key ID. Decrypt then looks up the header key ID and fails authentication.

Fix:

Make wrapping return the key ID atomically with the wrapped DEK, for example `Wrap(ctx, dek) (keyID string, wrapped []byte, err error)`, or add an equivalent interface method that captures both values under the same lock.

## crypto/paseto

### MEDIUM: custom claim write errors are ignored

Evidence:

- `crypto/paseto/paseto.go:324-325` loops through `Claims.Custom` and discards the error from `t.Set(k, v)`.
- `crypto/paseto/paseto.go:327` returns the token even if a custom claim failed to encode.

Impact:

Authorization or session claims supplied through `Custom` can be silently absent from a minted token. The caller has no way to know the token does not contain the intended claim set.

Fix:

Return the `t.Set` error with the claim key in context.

## data/budget

### LOW: `Refund` accepts invalid arguments when the backend has no refund capability

Evidence:

- `data/budget/budget.go:106-110` says `amount` must be non-negative.
- `data/budget/budget.go:110-115` only validates by delegating to backends that implement `Refunder`; a non-refunder returns `(0, false, nil)` even for negative amounts or an empty key.

Impact:

Callers get different validation behavior depending on optional backend capability. A bad refund request can look like a harmless unsupported refund rather than a programming error.

Fix:

Validate key and amount in the top-level `Refund` helper before the type assertion.

## data/budget/memory

### LOW: retry-after uses wall clock instead of the injected clock

Evidence:

- `data/budget/memory/memory.go:174-175` computes `now` and `nextStart` from the budget clock.
- `data/budget/memory/memory.go:190-198` returns `time.Until(nextStart)` on denial paths.
- `data/budget/redis/redis.go:276-280` correctly uses `nextStart.Sub(now)` and floors at zero.

Impact:

Tests or deployments using a custom clock get retry-after values based on the process wall clock, not the configured budget clock. Production wall-clock jumps can also skew the result.

Fix:

Return `nextStart.Sub(now)` floored at zero, matching the Redis backend.

## data/budget/redis

### MEDIUM: `WithClock(nil)` creates a latent panic

Evidence:

- `data/budget/redis/redis.go:162-165` wraps `now()` without checking for nil.
- `data/budget/redis/redis.go:247`, `data/budget/redis/redis.go:293`, and `data/budget/redis/redis.go:316` call `b.now(ctx)`.

Impact:

A bad test or configuration hook constructs successfully and panics later on `Consume`, `Refund`, or `Peek`. The memory backend validates the same option at construction time.

Fix:

Panic immediately on `WithClock(nil)` or normalize to the default clock.

### MEDIUM: Redis admission can overflow before checking the cap

Evidence:

- `data/budget/redis/redis.go:70-71` runs `INCRBY` before comparing `newUsed > cap`.
- `data/budget/redis/redis.go:240-260` accepts any non-negative `int64` amount and wraps script errors as backend errors.
- `data/budget/memory/memory.go:193-198` uses an overflow-safe `amount > cap-used` check before mutating state.

Impact:

Large amounts near `math.MaxInt64` can make Redis return an integer overflow script error instead of a clean `allowed=false` denial. That gives different behavior across memory and Redis backends and can surface as a 500-style dependency error.

Fix:

Reject `amount > cap` before the script, and change the Lua script to compare available headroom before `INCRBY`.

## httpx/budget

### HIGH: actual-cost reconciliation is not enforcing the budget

Evidence:

- `httpx/budget/budget.go:129-139` only gates the request on the pre-charge estimate.
- `httpx/budget/budget.go:184-215` computes the actual-cost delta after the upstream response returns.
- `httpx/budget/budget.go:205-214` logs failed or rejected delta charges but still returns the response at `httpx/budget/budget.go:154`.

Impact:

When actual cost exceeds the estimate, a caller can receive upstream data even if charging the delta fails or exceeds the remaining budget. For spend-control use cases this is audit-only behavior, not enforcement.

Fix:

Use a trusted upper-bound precharge and refund the difference, or reject/close the response when the actual delta cannot be charged. If the current behavior is intentional, the API name and docs should make clear that only the estimate is enforced.

### MEDIUM: canceled request contexts can prevent accounting cleanup

Evidence:

- `httpx/budget/budget.go:142-148` refunds the optimistic charge with `req.Context()` after transport errors.
- `httpx/budget/budget.go:151-152` reconciles the actual header with `req.Context()`.
- `httpx/budget/budget.go:227-240` then calls `budget.Refund` with that same context.

Impact:

If the client context is canceled or its deadline expires, the request-side accounting call can fail even though the budget mutation is cleanup work that should still run. That can leak optimistic charges or miss delta reconciliation.

Fix:

Use `context.WithoutCancel(req.Context())` plus a short bounded timeout for refund and reconciliation operations.

### MEDIUM: `WithLogger(nil)` can panic on warning paths

Evidence:

- `httpx/budget/budget.go:87-90` stores the provided logger without nil validation.
- `httpx/budget/budget.go:191-192`, `httpx/budget/budget.go:207-213`, and `httpx/budget/budget.go:233-239` call methods on `t.cfg.logger`.

Impact:

Normal requests may work, but malformed actual headers, failed delta charges, and refund failures can panic the transport.

Fix:

Reject nil in `WithLogger`, or ignore nil and keep `slog.Default()`.

## httpx/mcp

### MEDIUM: async audit enqueue can race shutdown and lose a job

Evidence:

- `httpx/mcp/actionlog.go:149-159` first checks `auditDone`.
- `httpx/mcp/actionlog.go:160-168` then selects between sending to `auditQueue` and receiving from `auditDone`.
- `httpx/mcp/mcp.go:424-440` workers drain the queue after seeing `auditDone`, and `httpx/mcp/mcp.go:468-486` closes `auditDone` during `Stop`.

Impact:

If `Stop` closes `auditDone` after the first check but before the second select, and the buffered queue send is ready, Go can choose the send case even though shutdown is already signaled. A worker may already have drained and exited, leaving the late job unprocessed and not counted as dropped.

Fix:

Guard enqueue and stop with a mutex or atomic stopped flag in a critical section that prevents any send after shutdown is visible. Do not rely on a select containing both a ready send and a closed done channel.

## httpx/middleware/idempotency

### HIGH: body fingerprinting is still off by default

Evidence:

- `httpx/middleware/idempotency/idempotency.go:216-228` default config does not enable `fingerprintBody`.
- `httpx/middleware/idempotency/idempotency.go:307-326` only computes a body fingerprint when `cfg.fingerprintBody` is true.
- `data/idempotency/idempotency.go:41-42` says the HTTP middleware always passes a fingerprint, but current code does not.
- `data/idempotency/pgstore/store.go:104-106` and `data/idempotency/redisstore/store.go:190-191` only detect mismatches when the provided fingerprint is non-nil.

Impact:

The default middleware allows the same user and idempotency key to be reused with a different POST, PUT, or PATCH body and still hit the previous cache or lock semantics. That is the main corruption case idempotency middleware is supposed to prevent.

Fix:

Enable body fingerprinting by default for methods that require an idempotency key. Offer an explicit opt-out for large-body routes that knowingly accept the risk.

## httpx/middleware/signedrequest

### MEDIUM: `WithClock(nil)` creates a latent panic

Evidence:

- `httpx/middleware/signedrequest/signedrequest.go:139-142` stores the provided clock without nil validation.
- `httpx/middleware/signedrequest/signedrequest.go:197` calls `cfg.now()`.
- `httpx/sign/sign.go:71-75` already rejects the same bad option shape on the outbound signer.

Impact:

A bad test hook or configuration constructs successfully and panics on the first signed request.

Fix:

Reject nil in `WithClock` or normalize to `time.Now`.

## httpx/middleware/signedrequest/redis

### LOW: non-positive call timeouts make every Redis nonce operation fail

Evidence:

- `httpx/middleware/signedrequest/redis/redis.go:51-56` accepts any duration and builds `context.WithTimeout(context.Background(), d)`.
- `httpx/middleware/signedrequest/redis/redis.go:101-104` uses that context for `SetNX`.

Impact:

`WithCallTimeout(0)` or a negative duration creates an immediately expired context. Every request then fails closed with a backend error, which is good for security but bad for fail-fast configuration.

Fix:

Panic on `d <= 0`, matching the package convention for invalid fixed timing options.

## httpx/middleware/tenant

### MEDIUM: required tenant is not actually required for safe methods by default

Evidence:

- `httpx/middleware/tenant/tenant.go:75-78` presents `WithRequired` as the missing-tenant switch.
- `httpx/middleware/tenant/tenant.go:81-90` adds a second required-on-safe-methods switch that defaults to false.
- `httpx/middleware/tenant/tenant.go:137-149` passes GET, HEAD, and OPTIONS through without a tenant.

Impact:

Services that mount tenant middleware in front of tenant-scoped reads can run handlers without tenant context unless they know to opt in to the second option. That is easy to miss because `WithRequired(true)` sounds complete.

Fix:

Make missing-tenant rejection apply to all methods by default, or rename the current behavior into an explicit `WithAllowMissingTenantOnSafeMethods` opt-out.

## httpx/resilient

### MEDIUM: `WithCBShouldTrip(nil)` creates a runtime panic

Evidence:

- `httpx/resilient.go:54-55` stores the supplied predicate directly.
- `httpx/resilient.go:138-142` installs it into the transport.
- `httpx/resilient.go:167-173` calls `t.shouldTrip(resp, rtErr)` on every request.

Impact:

The client constructs successfully and crashes only on the first outbound request through the circuit-breaker transport.

Fix:

Reject nil in `WithCBShouldTrip`, or treat nil as "keep the default predicate".

## infra/redis

### LOW: `WithLogger(nil)` can panic during normal lifecycle logging

Evidence:

- `infra/redis/connection.go:50-51` stores the provided logger directly.
- `infra/redis/connection.go:145-153` sets `slog.Default()` before options are applied.
- `infra/redis/connection.go:234`, `infra/redis/connection.go:281`, and `infra/redis/connection.go:296-305` call methods on `c.logger`.

Impact:

Passing nil to a public option overwrites the default logger and can panic on close, reconnect, or health failure paths.

Fix:

Ignore nil and keep the default logger, or panic immediately on `WithLogger(nil)`.

## infra/storage/azurebackend

### MEDIUM: `NewWithClient` accepts a nil client

Evidence:

- `infra/storage/azurebackend/azure.go:101-110` constructs an `AzureBackend` with the provided `BlobClient` and only validates the container name.
- `infra/storage/azurebackend/azure.go:148`, `infra/storage/azurebackend/azure.go:169`, `infra/storage/azurebackend/azure.go:210`, and `infra/storage/azurebackend/azure.go:275` call methods on `b.client`.

Impact:

Tests or service wiring can construct a backend that panics later on the first storage operation instead of failing at construction.

Fix:

Panic or return an error when `client` is nil.

## infra/storage/gcsbackend

### MEDIUM: `NewWithClient` dereferences a nil client instead of validating it

Evidence:

- `infra/storage/gcsbackend/gcs.go:83-91` only validates the bucket and then calls `client.Bucket(cfg.Bucket)`.

Impact:

Passing nil to the test constructor panics with a nil dereference instead of the package's explicit fail-fast error style.

Fix:

Validate `client != nil` before using it.

## infra/storage/s3backend

### MEDIUM: `NewWithClient` accepts nil client and presigner

Evidence:

- `infra/storage/s3backend/s3.go:134-140` validates only the bucket.
- `infra/storage/s3backend/s3.go:207`, `infra/storage/s3backend/s3.go:231`, `infra/storage/s3backend/s3.go:275`, and `infra/storage/s3backend/presign.go:31` call methods on the injected client or presigner.

Impact:

Invalid storage wiring constructs successfully and then panics when a storage or presign method is used.

Fix:

Validate both injected interfaces in `NewWithClient`.

## observability/auditlog

### LOW: `WithLogger(nil)` can panic on append failure

Evidence:

- `observability/auditlog/auditlog.go:56-57` stores the provided logger directly.
- `observability/auditlog/auditlog.go:65-70` sets the default logger before options are applied.
- `observability/auditlog/auditlog.go:88-94` calls `l.logger.Error` when the store append fails.

Impact:

The package turns an audit append failure into a panic if a caller passed a nil logger option. Since `Log` has no return value, this is especially surprising.

Fix:

Reject nil or ignore nil and keep the default logger.

## security/jwtutil

### MEDIUM: `NewProviderWithKeySet` bypasses issuer and audience guardrails

Evidence:

- `security/jwtutil/jwtutil.go:319-345` makes `NewProvider` require expected issuer and audience unless explicit opt-outs are supplied.
- `security/jwtutil/jwtutil.go:354-356` returns a provider from a preloaded keyset without options or equivalent validation.
- `security/jwtutil/jwtutil.go:111-116` validates issuer and audience only when the `KeySet` fields are non-empty.
- `security/jwtutil/jwtutil.go:70-80` parses a `KeySet` with those fields empty.

Impact:

Callers using static or preloaded keysets can still verify any correctly signed token regardless of issuer or audience. That reopens the confused-deputy risk the new `NewProvider` validation is meant to close.

Fix:

Add provider options to `NewProviderWithKeySet` and enforce the same issuer/audience checks, or add constructors for `KeySet` that require the validation policy.
