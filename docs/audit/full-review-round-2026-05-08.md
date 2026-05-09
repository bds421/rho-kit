# Full Repository Review Round - 2026-05-08

Scope: all tracked code in `github.com/bds421/rho-kit`, not restricted to the latest MR or diff.
Baseline reviewed: `64d9901` (`docs: refresh README, AGENTS, ai-guides, SUPPLY_CHAIN for /v2 path`).

Verification:

- `make test`: passed before this report was written.
- `make lint`: passed, 0 issues across all modules.
- `make vulncheck`: first failed in the sandbox because `proxy.golang.org` was unreachable, then passed after network approval. No known vulnerabilities found.

Severity:

- `HIGH`: can create a security bypass, data loss, broken generated output, or a false CI/audit pass.
- `MED`: reliability, production-safety, multi-tenant, or correctness issue with realistic blast radius.
- `LOW`: smaller correctness/API/documentation mismatch, footgun, or hardening gap.

## Repository / Supply Chain

### FR-001 [HIGH] Tracked generated binaries and scratch artifacts remain in source control

Evidence: `cmd/kit-new/kit-new`, `cmd/kit-verify/kit-verify`, `go.work.backup`, and `V2_REVIEW_FINDINGS.md` are tracked. The two command artifacts are Mach-O arm64 executables, about 4.5 MB and 8.0 MB.

Impact: The repository now carries platform-specific binary output and review scratch state. That creates release bloat, cache pollution, ambiguous provenance, and a path where stale executables can be copied or invoked instead of reproducible builds.

Recommendation: Remove these from Git, add ignore rules for built command artifacts/backups/review scratch files, and add a release or pre-commit check that rejects tracked binaries outside explicitly allowed fixture directories.

## cmd/kit-new

### FR-002 [HIGH] Postgres/sqlc scaffold path is unreachable from the CLI

Evidence: `cmd/kit-new/main.go:40-45` defines flags for module path, dir, MCP, and rho version only. `cmd/kit-new/main.go:65` creates `Params` without setting `Postgres`. The scaffold supports `Params.Postgres` at `cmd/kit-new/scaffold.go:54-59`, and gates sqlc/query/migration generation at `cmd/kit-new/scaffold.go:97-102`.

Impact: The advertised v2 bootstrap path cannot generate the Postgres/sqlc service shape from the public CLI. That path is also not compile-tested by the current CLI tests.

Recommendation: Expose a `-postgres` flag or make the intended database scaffold the default, then add generated-tree build/tidy tests for each scaffold variant.

### FR-003 [HIGH] Generated Postgres migrations are embedded from the wrong directory

Evidence: `cmd/kit-new/templates/wire.go.tmpl:30-37` generates `internal/app/wire.go` with `//go:embed db/migrations/*.sql`. `cmd/kit-new/scaffold.go:100-102` writes migrations under root `db/migrations/...`. `go:embed` paths are relative to the source file directory, so the generated file searches under `internal/app/db/migrations`.

Impact: A generated Postgres service will fail to compile once the Postgres path is enabled.

Recommendation: Move migrations under the generated package that owns the `embed.FS`, or create a small root-level migrations package and pass that FS into `internal/app`.

## cmd/kit-verify

### FR-004 [HIGH] Default exit semantics let failed probes pass CI

Evidence: `cmd/kit-verify/main.go:8-11` says the tool exits 1 if any control failed. The CLI default is different: `cmd/kit-verify/main.go:44` makes `-strict` false, and `cmd/kit-verify/main.go:128-134` exits nonzero only when a failed probe is considered hard. `cmd/kit-verify/main.go:137-143` marks only readiness as hard.

Impact: Security-header, request-ID, JWT, CSRF, and rate-limit failures can be reported but still return exit 0. That is a false positive in CI.

Recommendation: Make failed probes fail by default. If a softer exploratory mode is needed, require an explicit `-soft` or `-warn-only` flag.

### FR-005 [MED] Missing convention routes are encoded as passing probes

Evidence: JWT probing treats 404 as success at `cmd/kit-verify/main.go:214-235`. CSRF probing treats 404 as success at `cmd/kit-verify/main.go:239-257`. The rate-limit probe returns success when 30 requests do not produce a 429 at `cmd/kit-verify/main.go:260-290`.

Impact: Services can pass verification without the route actually existing or without the rate limiter being exercised.

Recommendation: Add explicit `pass`, `fail`, `skipped`, and `unknown` states. Strict mode should require configured probe endpoints for controls that are service-route dependent.

## cmd/kit-migrate

### FR-006 [MED] Migration registry omits packages that publish migrations

Evidence: `cmd/kit-migrate/main.go:22-29` registers only `idempotency`. `data/actionlog/postgres/migrations.go:5-14` and `data/approval/postgres/migrations.go:5-10` expose migrations and describe migration-tool use.

Impact: Services can use action log or approval stores without their kit-managed schemas being discoverable by the kit migration tool.

Recommendation: Add `actionlog` and `approval` to the registry, or document and enforce a separate migration path for those packages.

## cmd/kit-doctor / security/asvs

### FR-007 [HIGH] ASVS scanning trusts comments rather than behavior

Evidence: `cmd/kit-doctor/asvs.go:24-25` calls `asvs.ScanDir`. `security/asvs/scan.go:34-57` walks Go files and comments, and `security/asvs/scan.go:96-118` derives claimed/missing controls from annotations. `security/asvs/asvs.go:14-20` says kit-doctor scans imports and kit-verify probes behavior, but this scanner does not.

Impact: A service can falsely claim ASVS controls by copying comments, and a correctly wired service gets no credit unless comments are present.

Recommendation: Treat comments as package documentation only. Derive service evidence from imports, config, middleware wiring, and runtime probes.

### FR-008 [MED] The ASVS catalog overstates service guarantees

Evidence: `security/asvs/asvs.go:65-118` lists controls such as token revocation, TLS required, and production-safety validation as kit coverage. These are capabilities or Builder checks, not proof that every service has configured and verified them.

Impact: Audit output can imply a running-service guarantee where only a library capability exists.

Recommendation: Split the catalog into `capability available`, `builder-enforced`, and `runtime-verified` evidence classes.

## app

### FR-009 [MED] `WithLogger` is ignored by the default public middleware stack

Evidence: `app/builder.go:839-842` normalizes the configured logger, but `app/builder.go:1057` calls `stack.Default(httpHandler, slog.Default(), b.stackOpts...)`. `app/builder.go:722-726` says `WithLogger` sets the runtime logger.

Impact: Infrastructure setup and request middleware use different loggers unless users know to pass a separate stack option.

Recommendation: Pass the resolved `logger` into `stack.Default`.

### FR-010 [HIGH] Internal ops validation rejects wildcard binds but accepts non-loopback binds without opt-in

Evidence: `app/validate.go:39-62` only detects unspecified hosts. `app/validate.go:156-163` rejects those unless `WithInternalNonLoopback` was called. `app/config.go:21-43` documents that the internal server exposes health, readiness, and metrics, with loopback as the safe default.

Impact: `INTERNAL_HOST=10.0.0.5` or another reachable interface passes without the explicit non-loopback opt-in, despite exposing unauthenticated `/metrics`.

Recommendation: Enforce loopback unless explicitly relaxed. If private-network binds are supported, check private ranges and rename the opt-out so it matches what is actually allowed.

### FR-011 [MED] Shutdown hooks ignore the Runner's shutdown context

Evidence: `runtime/lifecycle/runner.go:216-227` passes `forceCtx` into `BeforeStop`. `app/builder_helpers.go:20` discards that parent context, and `app/builder_helpers.go:31` creates each hook context from `context.Background()`.

Impact: A second shutdown signal or runner-level force cancellation does not cancel the hook context. Multiple hooks can also exceed the overall shutdown budget because each receives a fresh 10 seconds.

Recommendation: Derive hook contexts from the parent context, preserving force cancellation and any remaining global deadline.

### FR-012 [LOW] `OnShutdown(nil)` fails late at shutdown

Evidence: `app/builder.go:786-788` appends hooks without a nil check. `app/builder_helpers.go:36` calls the hook.

Impact: A nil hook registration turns into a shutdown-time panic instead of a startup-time configuration error.

Recommendation: Panic or return a configuration error when registering a nil hook.

### FR-013 [MED] Module initialization has no startup deadline

Evidence: `app/builder.go:944-951` calls `initModules(context.Background(), ...)`.

Impact: A module that hangs during initialization can block startup indefinitely unless that module implements its own timeout.

Recommendation: Add a startup context/deadline option and pass it through module initialization.

## security/netutil

### FR-014 [HIGH] Builder TLS is not mTLS by default

Evidence: `security/netutil/tls.go:53-57` has `WithRequireClientCert`. Without it, `security/netutil/tls.go:61-72` documents verify-if-present behavior, and `security/netutil/tls.go:87-96` sets `tls.VerifyClientCertIfGiven`. `app/builder.go:864` calls `b.cfg.TLS.ServerTLS()` without `WithRequireClientCert`.

Impact: Setting `TLS_CA_CERT`, `TLS_CERT`, and `TLS_KEY` gives HTTPS with optional client certs, not global mTLS authentication. The AGENTS.md convention that setting TLS env enables mTLS globally is false.

Recommendation: Add an explicit Builder option/profile for required client certs, and make service-to-service listeners require client certificates if the kit contract says mTLS.

### FR-015 [MED] Partial TLS config validates as disabled for direct users

Evidence: `security/netutil/tls.go:21-30` returns disabled unless all three paths are set, and `Validate` returns nil when disabled.

Impact: Direct callers can provide a partial TLS configuration and silently run plaintext.

Recommendation: Reject partial TLS configs in `Validate`; keep `Enabled` as a separate convenience method.

### FR-016 [MED] SSRF-safe clients do not set a whole-request timeout

Evidence: `security/netutil/ssrf.go:208-218` and `security/netutil/ssrf.go:331-343` construct clients without `http.Client.Timeout`. The transports have dial/header timeouts, but not a body-read deadline.

Impact: A hostile endpoint can send headers and then stream the body slowly forever unless every caller adds its own context deadline.

Recommendation: Add a default whole-request timeout option, or require an explicit caller timeout.

### FR-017 [LOW] SSRF transports force TLS 1.3 for all external HTTPS

Evidence: `security/netutil/ssrf.go:188-196` and `security/netutil/ssrf.go:308-314` set `MinVersion: tls.VersionTLS13`.

Impact: Legitimate TLS 1.2-only upstreams will fail, increasing the chance callers bypass the SSRF wrapper for compatibility.

Recommendation: Default to TLS 1.2 with an explicit strict TLS 1.3 profile.

## httpx/middleware/stack and secheaders

### FR-018 [MED] Default stack cannot configure HSTS behind trusted TLS-terminating proxies

Evidence: `httpx/middleware/secheaders/secheaders.go:154-186` emits HSTS only for `r.TLS`, trusted proxy `X-Forwarded-Proto`, or force mode. `httpx/middleware/stack/stack.go:129-135` only forwards the frame option to `secheaders.New`.

Impact: Common ingress or service-mesh deployments can miss HSTS even while using the default stack.

Recommendation: Add stack options for trusted proxy CIDRs and force-HSTS, or accept full secheaders options in stack configuration.

### FR-019 [LOW] `secheaders.isTrustedRemote` can panic on nil CIDR entries

Evidence: `httpx/middleware/secheaders/secheaders.go:198-200` calls `cidr.Contains(ip)` without guarding nil entries.

Impact: A nil CIDR in config turns into a request-time panic.

Recommendation: Filter nil CIDRs at construction and skip nil entries defensively.

## httpx/middleware/csrf and security/csrf

### FR-020 [HIGH] CSRF cookie `Secure` defaults to false

Evidence: `httpx/middleware/csrf/csrf.go:61-64` documents `WithSecure` as default false. The default config at `httpx/middleware/csrf/csrf.go:203-209` does not enable secure cookies. Cookies use `Secure: cfg.secure` at `httpx/middleware/csrf/csrf.go:253-260` and `httpx/middleware/csrf/csrf.go:360-367`.

Impact: Production users who only provide a secret can emit CSRF cookies over plaintext same-host requests.

Recommendation: Default to secure cookies and require an explicit local-development opt-out.

### FR-021 [LOW] Configured CSRF origins are not canonicalized or validated

Evidence: `httpx/middleware/csrf/csrf.go:127-136` trims/lowercases configured origins. Request origins are normalized differently at `httpx/middleware/csrf/csrf.go:449-455`, including path stripping.

Impact: A configured origin such as `https://app.example.com/` can fail every state-changing request at runtime instead of being rejected at startup.

Recommendation: Parse and canonicalize configured origins at construction; reject malformed or pathful origins.

### FR-022 [MED] CSRF token verification returns session mismatch before HMAC verification

Evidence: `security/csrf/csrf.go:169-177` compares the session prefix and returns `ErrSessionMismatch` before validating the HMAC.

Impact: If error details or timing leak, the primitive provides a session-prefix oracle.

Recommendation: Verify the HMAC before returning mismatch details, or collapse public verification errors.

## httpx/sign, httpx/reqsign, and signedrequest

### FR-023 [HIGH] `httpx/sign` drains and closes the caller's original request body

Evidence: `httpx/sign/sign.go:123-130` clones the request, but `Request.Clone` shares `Body`. `httpx/sign/sign.go:131` reads the clone body, and `httpx/sign/sign.go:153-168` reads/closes `req.Body` while only replacing the clone.

Impact: Outer retry middleware or callers that reuse the original request see a consumed or closed body.

Recommendation: Use `GetBody`, or read and restore both original and clone bodies. Document one-shot body requirements if restoration is impossible.

### FR-024 [HIGH] `httpx/reqsign` has the same SigningTransport body-drain bug

Evidence: `httpx/reqsign/transport.go:47-68` clones the request, reads the shared body, and replaces only the clone.

Impact: Same as FR-023.

Recommendation: Restore the original request body or require `GetBody`.

### FR-025 [HIGH] Legacy `httpx/reqsign` is replayable within max age

Evidence: `httpx/reqsign/doc.go:27-31` lists signature, timestamp, and key headers. `httpx/reqsign/middleware.go:16-60` verifies signature body and timestamp only; there is no nonce store.

Impact: An intercepted signed request can be replayed until timestamp expiry.

Recommendation: Deprecate `reqsign` in favor of `httpx/middleware/signedrequest`, or add nonce/replay protection.

### FR-026 [MED] Signed-request nonces are not format or length validated

Evidence: `httpx/middleware/signedrequest/signedrequest.go:8-10` says nonces are base64 16 random bytes. `httpx/middleware/signedrequest/signedrequest.go:187-193` only checks non-empty, and `httpx/middleware/signedrequest/signedrequest.go:241-247` stores the nonce. Redis keys concatenate the raw nonce at `httpx/middleware/signedrequest/redis/redis.go:118-119`.

Impact: Malformed or oversized nonce headers can create large Redis keys and high-cardinality memory growth.

Recommendation: Cap nonce header length, base64-decode it, and require exactly 16 bytes.

### FR-027 [LOW] Signed-request Redis prefix and key composition are not bounded

Evidence: `httpx/middleware/signedrequest/redis/redis.go:39-45` accepts any prefix, and `httpx/middleware/signedrequest/redis/redis.go:104-119` concatenates prefix, key ID, and nonce into Redis keys.

Impact: Bad config or malformed input can create key collisions or pathological key sizes.

Recommendation: Validate prefix, key ID, and nonce with a shared bounded key policy.

### FR-028 [LOW] Signing clients accept invalid header names

Evidence: `httpx/sign/sign.go:47-52` lowercases any configured include header. The verifier's required-header option validates names at `httpx/middleware/signedrequest/signedrequest.go:116-126`.

Impact: A client can generate a signature profile that production verifiers will not accept.

Recommendation: Reuse the verifier's header-name validation on the signing side.

## httpx/middleware/idempotency and data/idempotency

### FR-029 [HIGH] Idempotency keys omit query string and semantic headers

Evidence: `httpx/middleware/idempotency/idempotency.go:324` uses `r.URL.Path`. `httpx/middleware/idempotency/idempotency.go:501-512` builds the fingerprint from method, path, raw key, and user ID. The body fingerprint hashes only the body.

Impact: `POST /orders?dry_run=true` and `POST /orders?dry_run=false` can collide when the same idempotency key and body are used.

Recommendation: Include canonical `RequestURI` or an explicit route ID plus canonical query and configured semantic headers in the fingerprint.

### FR-030 [HIGH] PgStore cannot replay empty or no-body responses

Evidence: The middleware stores the recorded body at `httpx/middleware/idempotency/idempotency.go:441-445`, which can be nil for empty responses. PgStore writes `resp.Body` at `data/idempotency/pgstore/store.go:146-157`, but `Get` filters with `response_body IS NOT NULL` at `data/idempotency/pgstore/store.go:87-90`. The migration makes `response_body BYTEA` nullable at `data/idempotency/pgstore/migrations/20260101000001_create_idempotency_keys.sql:4-7`.

Impact: 204 and empty-body successful operations cannot be replayed reliably.

Recommendation: Store an explicit response state or empty bytea marker, and remove the `IS NOT NULL` replay filter.

### FR-031 [LOW] Redis idempotency store accepts unbounded direct keys and prefixes

Evidence: `data/idempotency/redisstore/store.go:69-73` accepts any key prefix, and `data/idempotency/redisstore/store.go:99` concatenates the raw key. Store methods do not validate key length or characters.

Impact: Direct store users can create oversized Redis keys even though the middleware hashes keys first.

Recommendation: Add a shared key validator at the store boundary.

### FR-032 [LOW] Idempotency preserved-header config does not validate header names

Evidence: `httpx/middleware/idempotency/idempotency.go:673-680` accepts any string.

Impact: Invalid header names can be configured and then fail later or produce unusable replay behavior.

Recommendation: Validate with `httpguts.ValidHeaderFieldName`.

## flags

### FR-033 [HIGH] `flags.New` mutates the OpenFeature global provider and ignores the error

Evidence: `flags/flags.go:41-47` calls `openfeature.SetProvider(p)` and ignores the returned error before creating a client.

Impact: Multiple flag providers in the same process or tests overwrite each other process-wide. Provider setup failures are hidden.

Recommendation: Use OpenFeature domains or an explicit provider wrapper, and return or panic on `SetProvider` failure.

### FR-034 [MED] Flag getters silently swallow provider errors

Evidence: `flags/flags.go:49-77` ignores errors from bool, string, int, float, and object evaluations and returns fallback values.

Impact: Provider outages or malformed flag values silently change behavior, including kill switches or billing/security gates.

Recommendation: Add error-returning APIs, details APIs, metrics/hooks, or explicit safe-default policy per flag.

### FR-035 [LOW] MemoryProvider stores and returns object references directly

Evidence: `flags/memory.go:68-72` and `flags/memory.go:115-124` keep object values as-is.

Impact: Callers can mutate shared flag state outside the provider lock.

Recommendation: Deep-copy structured object values, store JSON bytes, or document immutable object requirements.

## authz and authz/openfga

### FR-036 [MED] `authz.Allow` panics on nil deciders instead of failing closed

Evidence: `authz/authz.go:54-55` calls `d.Allow` directly. `app/infrastructure.go:71` says `Infrastructure.Authz` is nil when `WithAuthz` was not configured.

Impact: Handlers using the helper with optional infrastructure get a panic/500 instead of a denial or configuration error.

Recommendation: Return a typed forbidden/configuration error when the decider is nil.

### FR-037 [MED] OpenFGA adapter lacks auth, TLS, custom client, and timeout configuration

Evidence: `authz/openfga/openfga.go:40-44` exposes only API URL, store ID, and model ID. `authz/openfga/openfga.go:55-59` builds the SDK client with only those fields.

Impact: Real OpenFGA deployments often require credentials, custom HTTP clients, TLS/mTLS, and timeouts. The wrapper encourages local unauthenticated deployments or bypassing the kit wrapper.

Recommendation: Accept a prebuilt SDK client or add token, TLS, and HTTP client options.

### FR-038 [LOW] OpenFGA requests are not validated locally

Evidence: `authz/openfga/openfga.go:70-84` passes subject, action, and resource through to OpenFGA without checking for empty values.

Impact: Empty request fields reach the authorization engine instead of failing closed at the adapter boundary.

Recommendation: Validate non-empty subject, action, and resource locally.

## core/config

### FR-039 [MED] Secret `_FILE` loading reads unbounded files into memory

Evidence: `core/config/load.go:191-204` calls `os.ReadFile(filePath)` for secret files.

Impact: A misconfigured secret path can read very large files into memory during startup.

Recommendation: Add a maximum secret file size and fail fast when exceeded.

### FR-040 [LOW] `Load[*Config]` can panic instead of returning an error

Evidence: `core/config/load.go:29-38` accepts any `T`, and `hasEnvTags` handles pointer types. `core/config/load.go:87-89` then assumes the value passed to `loadWithEnvTracking` is a struct and calls `NumField`.

Impact: A pointer generic type can pass the tag precheck and panic during loading.

Recommendation: Reject pointer `T` up front with an error, or allocate and load the pointed-to struct explicitly.

## core/secret and core/randstr

### FR-041 [LOW] `secret.String.Equal` is not constant-time for length mismatches

Evidence: `core/secret/secret.go:145-170` documents constant time relative to max length, but `core/secret/secret.go:170-173` returns immediately when lengths differ.

Impact: Length mismatch is observable by timing, contradicting the primitive's guarantee.

Recommendation: Compare over the maximum length and fold the length equality into the result.

### FR-042 [LOW] `secret.String` does not actually implement `slog.LogValuer`

Evidence: `core/secret/secret.go:210-212` declares `LogValue() any`, while slog's interface requires `LogValue() slog.Value`.

Impact: Redaction currently relies on other formatting and JSON hooks. The stated slog-specific guarantee is not implemented directly.

Recommendation: Change the method to return `slog.Value` and add a compile-time assertion.

### FR-043 [LOW] Random string generation has no maximum length guard

Evidence: `core/randstr/randstr.go:36-60` accepts any non-negative length and allocates `[]rune` of that size.

Impact: User-influenced lengths can allocate large memory and spend significant CPU in `crypto/rand`.

Recommendation: Add a safe maximum or require callers to opt into unbounded generation.

## crypto

### FR-044 [MED] GCP KMS empty AAD can fail wrapping

Evidence: `crypto/envelope/gcpkms/gcpkms.go:102-109` sends AAD and an AAD CRC. `crypto/envelope/gcpkms/gcpkms.go:153-163` returns nil CRC for zero-length AAD. `crypto/envelope/gcpkms/gcpkms.go:120-122` checks AAD verification when `k.aad != nil`.

Impact: `[]byte{}` AAD is semantically equivalent to nil, but can make every wrap fail.

Recommendation: Normalize zero-length AAD to nil, or gate the verification check on `len(k.aad) > 0`.

### FR-045 [MED] Argon2 hash parameters lack upper bounds

Evidence: `crypto/passhash/passhash.go:51-79` defines default verification bounds, but `crypto/passhash/passhash.go:171-179` only rejects zero memory, iterations, and parallelism before `crypto/passhash/passhash.go:181-186` calls Argon2.

Impact: A typo or attacker-influenced config can allocate excessive memory or CPU during hashing.

Recommendation: Enforce maximums for hashing parameters as well as verification parameters, with an explicit override for unusual deployments.

### FR-046 [MED] PASETO provider shutdown can block for the refresh interval

Evidence: `crypto/paseto/provider.go:112-117` closes `p.stop` and waits. `crypto/paseto/provider.go:127-130` creates refresh contexts from `context.Background()` with `p.interval` timeout.

Impact: `Stop` can wait minutes if the source blocks and the interval is long.

Recommendation: Give the provider a root context cancelled by `Stop`, and use a separate short fetch timeout.

### FR-047 [LOW] HMAC signing verification is not constant-time for valid-hex wrong-length input

Evidence: `crypto/signing/signing.go:204-209` assigns decoded hex regardless of length. `crypto/signing/signing.go:211` calls `hmac.Equal`, which returns immediately on length mismatch.

Impact: The implementation contradicts the constant-time comment for valid-hex signatures with the wrong length.

Recommendation: Compare against a fixed-size fallback unless the decoded signature is exactly `sha256.Size`.

## data/cache

### FR-048 [MED] ComputeCache waiters cannot cancel while waiting on singleflight

Evidence: `data/cache/compute.go:288-304` uses blocking `cc.group.Do`. The caller context controls the leader's compute, but followers wait until the shared call returns.

Impact: Short-deadline requests can block behind another caller's long compute.

Recommendation: Use `DoChan` and select on each waiting caller's context.

### FR-049 [LOW] Stale hits can spawn many background refresh waiters

Evidence: `data/cache/compute.go:266-268` triggers a background refresh on each stale hit. `data/cache/compute.go:381-411` starts a goroutine per trigger; singleflight deduplicates compute work, not the waiting goroutines.

Impact: A hot stale key can accumulate many goroutines during a long refresh.

Recommendation: Track refresh-in-flight per key before starting another waiter.

## data/actionlog

### FR-050 [HIGH] Empty signing key IDs can write entries that can never verify

Evidence: `data/actionlog/actionlog.go:296-309` allows `currentKeyID == ""` when the key map has an empty key. `data/actionlog/actionlog.go:397-408` writes that ID into entries. `data/actionlog/actionlog.go:517-519` rejects empty `SignatureKeyID` during verify. `data/actionlog/actionlog.go:543-552` does not validate `SignatureKeyID` before append.

Impact: A misconfigured action log can persist permanently unverifiable audit entries.

Recommendation: Reject empty current key IDs and validate `SignatureKeyID` before append.

### FR-051 [MED] Action log ID validation does not match the Postgres schema

Evidence: `data/actionlog/actionlog.go:543-546` only checks that IDs are non-empty. The Postgres migration uses `id VARCHAR(36)` at `data/actionlog/postgres/migrations/20260507000001_create_action_log_entries.sql:3`.

Impact: Memory and Postgres stores accept different input contracts; invalid IDs fail late in the database.

Recommendation: Validate UUID/length at the package boundary or widen the schema consistently.

### FR-052 [LOW] Action log signature comparison has the same length timing issue

Evidence: `data/actionlog/actionlog.go:528-537` decodes and compares HMAC signatures without fixing length before `hmac.Equal`.

Impact: Wrong-length signatures take a shorter path.

Recommendation: Use the fixed-size fallback approach described in FR-047.

## data/approval

### FR-053 [HIGH] Approval listing defaults to cross-tenant queries

Evidence: `data/approval/approval.go:81-90` has `TenantID` but no explicit `AllTenants` flag. `data/approval/postgres/store.go:106-144` emits no tenant predicate for a zero-value query.

Impact: A handler that forgets to set tenant ID can leak approval requests across tenants.

Recommendation: Require `TenantID` by default, or add an explicit `AllTenants` admin flag like action log queries.

### FR-054 [MED] Approval mutation APIs are not tenant-scoped

Evidence: `data/approval/approval.go:106-139` exposes `Get`, `Decide`, and `MarkExecuted` by ID. Postgres queries use `WHERE id = $1` at `data/approval/postgres/store.go:89-93`, `data/approval/postgres/store.go:186-191`, and `data/approval/postgres/store.go:261-266`.

Impact: Service handlers must remember to perform tenant checks separately, creating an IDOR footgun.

Recommendation: Add tenant-scoped variants or a tenant-aware store wrapper.

### FR-055 [MED] Approval ID validation does not match the Postgres schema

Evidence: `data/approval/approval.go:11-17` allows 1 to 255 characters. `data/approval/postgres/store.go:340-345` mirrors that. The migration uses `id VARCHAR(36)` at `data/approval/postgres/migrations/20260507000001_create_approval_requests.sql:3`.

Impact: IDs can pass package validation but fail in Postgres.

Recommendation: Align validation and schema.

### FR-056 [MED] Approval payloads have no package-level size or redaction guard

Evidence: `data/approval/approval.go:53-59` warns that payloads can contain sensitive data, but `data/approval/approval.go:147-163` does not cap or redact `Payload`.

Impact: Large or sensitive JSON can be persisted indefinitely.

Recommendation: Add size limits, redaction hooks, and optionally field encryption.

## data/budget/redis and data/ratelimit/redis

### FR-057 [LOW] Redis budget keys and prefixes are unbounded

Evidence: `data/budget/redis/redis.go:154-160` accepts any prefix. Redis keys are built with `fmt.Sprintf("%s%s:%d", prefix, key, periodID)` at `data/budget/redis/redis.go:245-247`. Public operations only check non-empty keys at `data/budget/redis/redis.go:256-262`, `data/budget/redis/redis.go:323-329`, and `data/budget/redis/redis.go:348-352`.

Impact: Attacker-controlled or misconfigured keys can create oversized/high-cardinality Redis keys.

Recommendation: Validate prefixes and keys with a shared Redis-key policy.

### FR-058 [LOW] Redis rate-limit keys and prefixes are unbounded

Evidence: `data/ratelimit/redis/redis.go:83-89` accepts any prefix. `data/ratelimit/redis/redis.go:185-187` concatenates prefix and key, and `data/ratelimit/redis/redis.go:172-175` only checks that key is non-empty.

Impact: Same Redis key growth/collision risk as FR-057.

Recommendation: Use the same key validator.

## data/queue

### FR-059 [MED] River queue wrapper does not expose uniqueness/idempotency

Evidence: `data/queue/riverqueue/riverqueue.go:67-80` stores `Message.ID` in the envelope but uses `river.InsertOpts{Queue: queue}` only. `data/queue/riverqueue/riverqueue.go:82-91` uses one constant kind, and `data/queue/riverqueue/riverqueue.go:111-117` does not bridge permanent/transient error classification.

Impact: Callers can assume `Message.ID` is an idempotency key, while duplicates are accepted unless River is configured elsewhere.

Recommendation: Expose River insert options and a default unique key of queue plus message ID; map permanent errors consistently.

### FR-060 [MED] Redis queue consumer IDs are not validated

Evidence: `data/queue/redisqueue/queue.go:370-379` accepts any non-empty consumer ID. Processing and heartbeat keys derive from it at `data/queue/redisqueue/queue.go:591-598`, and the reaper slices it out of keys at `data/queue/redisqueue/helpers.go:476-485`.

Impact: IDs containing delimiters, control characters, or very long strings can create malformed keys and reaper ambiguity.

Recommendation: Validate consumer IDs with the Redis name/key validator and a max length.

### FR-061 [MED] Redis queue retry/DLQ scripts enqueue before proving the original exists

Evidence: `data/queue/redisqueue/helpers.go:99-111` pushes a retry record before scanning/removing the processing item. `data/queue/redisqueue/helpers.go:125-141` does the same for dead letters. The Go path treats a missing original as "already reclaimed" at `data/queue/redisqueue/helpers.go:286-294` and `data/queue/redisqueue/helpers.go:319-324`.

Impact: A race with a reaper can create an extra retry or dead-letter copy for a message that was already reclaimed.

Recommendation: Find and remove or tombstone the original in Lua first, then enqueue retry/DLQ only on success.

## data/stream/redisstream and infra/messaging/redisbackend

### FR-062 [MED] Redis stream producer defaults to unbounded retention

Evidence: `data/stream/redisstream/producer.go:57-65` treats max length and retention zero as unlimited. `data/stream/redisstream/producer.go:128-147` does not set a retention default.

Impact: Default event streams grow forever in long-running services.

Recommendation: Require an explicit retention/max length or provide a conservative default with an explicit unlimited opt-out.

### FR-063 [MED] Redis stream logger options accept nil and can panic later

Evidence: `data/stream/redisstream/producer.go:76-79` and `data/stream/redisstream/consumer.go:172-175` assign the provided logger without nil normalization. Publish and consumer paths later call logger methods.

Impact: Optional logger configuration can become a runtime nil-pointer panic.

Recommendation: Normalize nil to `slog.Default()`.

### FR-064 [MED] Redis messaging backend ignores `Binding.Queue`

Evidence: `infra/messaging/redisbackend/consumer.go:11-13` says `Binding.Queue` maps to the consumer group. `infra/messaging/redisbackend/consumer.go:36-42` uses only `b.Exchange`; group selection is whatever the wrapped stream consumer was constructed with.

Impact: Binding configuration silently drifts from runtime behavior, especially for multi-binding services.

Recommendation: Construct per-binding consumers or remove/validate the Queue mapping.

## grpcx

### FR-065 [MED] mTLS service callers can impersonate any UUID once allow-listed

Evidence: `grpcx/interceptor/auth.go:381-393` documents mTLS plus `x-user-id` as a trusted S2S mode. `grpcx/interceptor/auth.go:477-489` validates only that the provided user ID is a UUID, then sets user ID and trusted-S2S marker. Permission and scope checks bypass trusted S2S at `grpcx/interceptor/auth.go:555-585`.

Impact: Any allow-listed internal service certificate can impersonate any user unless every deployment adds separate policy. This may be intended for fully trusted internal callers, but it is too broad for a kit-level "trusted guarantees" default.

Recommendation: Bind service identities to allowed impersonation scopes, or require a separate authorization callback for S2S user impersonation.

### FR-066 [LOW] mTLS auth interceptors cannot skip health methods

Evidence: JWT-only `AuthUnary` and `AuthStream` accept `AuthOption` and `WithSkipMethods` at `grpcx/interceptor/auth.go:51-58` and `grpcx/interceptor/auth.go:69-111`. `MTLSAuthUnary` and `MTLSAuthStream` accept only `MTLSIdentityOption` at `grpcx/interceptor/auth.go:399-445`.

Impact: Services using the combined JWT/mTLS interceptors need another pattern for unauthenticated health checks.

Recommendation: Let mTLS auth accept skip-method options or provide a unified auth config.

## httpx/middleware/auth

### FR-067 [MED] HTTP mTLS service callers can impersonate any UUID once allow-listed

Evidence: `httpx/middleware/auth/auth.go:53-58` documents mTLS plus `X-User-Id`. `httpx/middleware/auth/auth.go:295-321` extracts and validates only UUID shape. Permission and scope helpers bypass trusted S2S according to `httpx/middleware/auth/auth.go:340-376` and `httpx/middleware/auth/scope.go:10-28`.

Impact: Same impersonation-scope issue as FR-065 for HTTP.

Recommendation: Add a service identity to subject/permission policy hook, or clearly separate "trusted service identity" from "impersonated user identity".

## infra/messaging

### FR-068 [HIGH] BufferedPublisher ignores persistence failure after removing published messages from memory

Evidence: `infra/messaging/buffered_publisher.go:453-461` compacts published messages out of `pending`, then calls `_ = o.saveLocked()`. `infra/messaging/buffered_publisher.go:490-501` shows save failures are meaningful state.

Impact: If saving fails after broker publish, the on-disk buffer can still contain already-published messages. On restart they can be published again.

Recommendation: Treat save failure as a health/error condition, preserve enough state to reconcile, and document duplicate behavior.

### FR-069 [LOW] BufferedPublisher max size treats non-positive values as unlimited

Evidence: `infra/messaging/buffered_publisher.go:106-110` accepts any max size. Capacity checks only apply when `maxSize > 0` at `infra/messaging/buffered_publisher.go:264` and `infra/messaging/buffered_publisher.go:309`.

Impact: A typo can disable backpressure and allow unbounded memory growth during broker outages.

Recommendation: Reject non-positive sizes, or add an explicit `WithUnlimitedBuffer` option.

## infra/messaging/amqpbackend

### FR-070 [MED] AMQP prefetch accepts zero and disables backpressure

Evidence: `infra/messaging/amqpbackend/consumer.go:51-56` accepts any prefetch value. `infra/messaging/amqpbackend/consumer.go:200-202` passes it to `ch.Qos`.

Impact: RabbitMQ interprets prefetch 0 as unlimited, so a typo can remove consumer backpressure.

Recommendation: Panic or return a config error for prefetch <= 0.

### FR-071 [HIGH] AMQP permanent errors are acked and discarded instead of dead-lettered

Evidence: `infra/messaging/amqpbackend/consumer.go:325-340` logs permanent errors as discarded and calls `delivery.Ack(false)`. Other backends dead-letter permanent errors, for example `data/stream/redisstream/consumer.go:601-610` and `data/queue/redisqueue/helpers.go:235-243`.

Impact: Poison or validation-failed messages disappear without a broker-visible DLQ/audit trail, inconsistent with the kit's no-data-loss goal.

Recommendation: Dead-letter permanent errors when a dead exchange is configured, and make discard behavior an explicit option.

### FR-072 [MED] AMQP dead-letter publish ignores handler/shutdown context

Evidence: `infra/messaging/amqpbackend/consumer.go:382-384` uses `context.Background()` for dead-letter publishing.

Impact: Trace values and shutdown deadlines are lost. A force shutdown cannot cancel the DLQ publish until its own timeout path completes.

Recommendation: Derive from the handler context while preserving a bounded deadline.

## infra/messaging/natsbackend

### FR-073 [HIGH] NATS connection config lacks TLS and auth options

Evidence: `infra/messaging/natsbackend/natsbackend.go:61-82` exposes URL, name, ack, and reconnect settings only. `infra/messaging/natsbackend/natsbackend.go:109-117` builds NATS options without TLS, credentials, token, NKey, or custom options.

Impact: Production NATS commonly requires TLS and authentication. The wrapper encourages plaintext/no-auth or forces users to bypass it.

Recommendation: Expose typed TLS/auth options and/or accept raw `nats.Option` values.

### FR-074 [MED] NATS subject comments claim sanitization that is not implemented

Evidence: `infra/messaging/natsbackend/natsbackend.go:262-271` says routing keys are sanitized and dots URL-encoded. `infra/messaging/natsbackend/natsbackend.go:475-487` only concatenates exchange and routing key.

Impact: Dots and wildcard-like tokens can route unexpectedly, and invalid subjects fail late.

Recommendation: Implement token validation/encoding or correct the contract.

### FR-075 [MED] NATS message headers are copied without validation

Evidence: `infra/messaging/natsbackend/natsbackend.go:283-285` copies `msg.Headers` directly to `nats.Header`.

Impact: Invalid or oversized header names/values can fail publish or corrupt metadata.

Recommendation: Validate header names and value sizes at the publisher boundary.

### FR-076 [MED] NATS publish allows empty subjects to fail late

Evidence: `infra/messaging/natsbackend/natsbackend.go:272-300` publishes the subject from `composeSubject`, and `infra/messaging/natsbackend/natsbackend.go:475-487` can return an empty string when exchange is empty.

Impact: Empty or malformed subjects reach the broker instead of being a kit-level configuration error.

Recommendation: Validate non-empty subject components before publish.

## infra/redis

### FR-077 [MED] Redis validation accepts plaintext and passwordless URL configurations

Evidence: `infra/redis/config.go:129-139` only requires a password when using split fields. `infra/redis/config.go:141-154` accepts both `redis` and `rediss` schemes and does not require credentials.

Impact: A service can pass validation with `redis://host:6379/0` and no password/TLS, which is weak for production-safe defaults.

Recommendation: Require authentication and `rediss` unless an explicit local/plaintext opt-out is configured.

### FR-078 [MED] `onReconnect` timeout does not stop callbacks that ignore context

Evidence: `infra/redis/connection.go:356-385` creates a timeout context, but when the timeout fires it waits on `<-done` for the callback goroutine to return.

Impact: A misbehaving reconnect callback can still block that goroutine forever despite the timeout.

Recommendation: Log and return on timeout without waiting, or run callbacks through a managed worker with clear lifecycle semantics.

## infra/sqldb/pgx

### FR-079 [HIGH] pgx TLS policy accepts `sslmode=require`

Evidence: `infra/sqldb/pgx/pgx.go:13-17` documents `require`, `verify-ca`, and `verify-full` as accepted. `infra/sqldb/pgx/pgx.go:350-377` treats non-nil TLS config and no plaintext fallback as acceptable, with comments accepting `require`.

Impact: `sslmode=require` encrypts but does not verify server identity, allowing MITM with arbitrary certificates in many network environments.

Recommendation: Default to `verify-full`; make `require` an explicit trusted-network opt-out.

## infra/storage

### FR-080 [MED] `storage.MaxFileSize` accepts zero and negative limits

Evidence: `infra/storage/validate.go:75-83` constructs the validator without checking the limit. `infra/storage/validate.go:99-103` slices based on `lr.remaining+1`.

Impact: Negative limits can panic, and zero limits can create surprising read behavior under direct use.

Recommendation: Fail fast for max <= 0.

### FR-081 [LOW] Generic storage copy reuses metadata maps by reference

Evidence: `infra/storage/copy.go:77-80` assigns `Custom: meta.Custom`. The migration path deep-copies at `infra/storage/migrate.go:167-180`. Encrypted copy repeats by reference at `infra/storage/encryption/encryption.go:385-388`.

Impact: Destination validators/backends can mutate source metadata maps and make retries order-dependent.

Recommendation: Deep-copy custom metadata in all copy paths.

### FR-082 [LOW] Storage migration does not use decorated lister discovery

Evidence: `infra/storage/migrate.go:55-59` and `infra/storage/migrate.go:143-147` require `src.(Lister)` directly. Capability helpers exist in `infra/storage/unwrap.go:60-64`.

Impact: Decorated backends that expose listing through unwrapping fail migration/count.

Recommendation: Use `storage.AsLister`.

## observability

### FR-083 [MED] `observability/auditlog` silently drops append failures by default

Evidence: `observability/auditlog/auditlog.go:112-138` logs append errors, increments optional counters, and invokes optional callbacks, but `Log` returns no error.

Impact: Compliance-relevant audit events can be lost unless every service configures an on-drop path.

Recommendation: Deprecate this for critical audit trails in favor of `data/actionlog`, or add a strict mode/fallback sink contract.

### FR-084 [LOW] Logging context can store nil loggers and panic later

Evidence: `observability/logging/logging.go:67-79` stores any `*slog.Logger`. If nil is stored, `FromContext` returns nil, and `observability/logging/logging.go:83-85` calls `.With` on it.

Impact: A nil logger in context creates latent request-time panics.

Recommendation: Normalize nil to `slog.Default()` in `WithContext` and `FromContext`.

### FR-085 [LOW] Secret log attributes expose deterministic digests of low-entropy secrets

Evidence: `observability/logattr/logattr.go:115-129` documents digest-based redaction. `observability/logattr/logattr.go:149-155` logs the first 8 hex characters of SHA-256.

Impact: Short OTPs, reset codes, or other low-entropy values can be brute-forced offline from logs.

Recommendation: Do not include digests by default, or use an HMAC with a log-correlation key and warn against low-entropy secrets.

### FR-086 [MED] pprof mount is unsafe by default

Evidence: `observability/pprof/pprof.go:76-82` exposes `Mount`/`MountWith` without loopback or auth by default. The optional guards are `WithRequireLoopback` and `WithAuth` at `observability/pprof/pprof.go:30-44`.

Impact: A caller can mount heap, goroutine, trace, and CPU profile endpoints on a public mux with one call.

Recommendation: Make loopback/auth gating the default, and require an explicit unsafe option for public mounts.

## resilience

### FR-087 [MED] Retry policies accept zero or invalid delay parameters

Evidence: `resilience/retry/retry.go:111-118` sets base delay, max delay, and factor without validation. `resilience/retry/retry.go:385-391` passes them to the backoff implementation.

Impact: Misconfiguration can create zero-delay retry loops or ineffective backoff.

Recommendation: Validate positive base/max delays and a sensible factor, or normalize with explicit warnings.

### FR-088 [LOW] Circuit-breaker error-rate thresholds are not validated

Evidence: `resilience/circuitbreaker/circuitbreaker.go:98-111` accepts any `rate` and `minRequests`.

Impact: `rate > 1` can prevent tripping forever, and `minRequests == 0` can trip on very small samples.

Recommendation: Require `0 < rate <= 1` and `minRequests >= 1`.

## runtime

### FR-089 [MED] Eventbus async default creates unbounded goroutines

Evidence: `runtime/eventbus/eventbus.go:67-70` documents unbounded goroutines without worker pool. `runtime/eventbus/eventbus.go:420-422` starts a goroutine per async handler when no pool exists.

Impact: High-volume events can exhaust goroutines/memory by default.

Recommendation: Use a bounded worker pool by default, and make unbounded async dispatch explicit.

### FR-090 [MED] Eventbus worker pool can silently buffer or drop before `Start`

Evidence: `runtime/eventbus/pool.go:79-84` logs when submit happens before start, but still proceeds. `runtime/eventbus/pool.go:128-145` then enqueues or drops based on buffer state.

Impact: Direct users can lose async events before lifecycle start.

Recommendation: Return an error until the pool is started, or auto-start the pool when configured outside lifecycle.

### FR-091 [MED] Eventbus default saturation policy drops events

Evidence: `runtime/eventbus/eventbus.go:18-25` defines `OnFullDrop` as default. `runtime/eventbus/eventbus.go:337-345` documents that default async saturation drops events silently except metrics/logs.

Impact: Domain events can be lost unless users intentionally switch to block/error semantics.

Recommendation: Default to `OnFullError` for application events, or require choosing a policy when enabling async.

### FR-092 [LOW] Batchworker invalid safety options are silently ignored

Evidence: `runtime/batchworker/batchworker.go:56-61` ignores invalid jitter, and `runtime/batchworker/batchworker.go:67-72` ignores invalid timeout.

Impact: Misconfiguration falls back silently instead of failing fast.

Recommendation: Panic or return a constructor error for invalid options.

### FR-093 [MED] Cron jobs have no default per-job timeout

Evidence: `runtime/cron/cron.go:98-102` says the default is no timeout. `runtime/cron/cron.go:201-205` only creates a timeout when configured.

Impact: A job that blocks forever causes future runs to be skipped by `SkipIfStillRunning` and can continue past shutdown until it returns.

Recommendation: Add a conservative default job timeout with an explicit opt-out.

### FR-094 [LOW] Cron invalid job timeout is silently ignored

Evidence: `runtime/cron/cron.go:106-108` returns when `d <= 0`.

Impact: A bad timeout value can silently leave the job unbounded.

Recommendation: Reject invalid durations.

### FR-095 [LOW] Lifecycle stop budget is not actually a per-component minimum

Evidence: `runtime/lifecycle/runner.go:245-248` describes a per-component minimum budget. `runtime/lifecycle/runner.go:264-267` computes `stopTimeout / componentCount` capped at 5 seconds.

Impact: Many components can receive very small stop windows, contrary to the comment and likely operator expectation.

Recommendation: Update the contract or implement a real minimum plus global budget strategy.

### FR-096 [LOW] Temporal wrapper is too thin to provide kit-level production guarantees

Evidence: `runtime/temporal/temporal.go:24-50` exposes basic host/namespace/identity plus raw options, and `runtime/temporal/temporal.go:103-115` largely passes through to the Temporal client/worker.

Impact: TLS, auth, retry, concurrency, namespace, and worker-safety defaults are left to every service.

Recommendation: Add a hardened Temporal profile or document this package as a thin adapter rather than a safe bootstrap abstraction.

## No Additional Concrete Findings In This Pass

The review pass did not find a concrete issue worth reporting in these package families beyond the cross-cutting items above: `core/apperror`, `core/clock`, `core/contextutil`, `core/safecast`, `core/tenant`, `core/validate`, `data/lock/pgadvisory`, `data/lock/redislock`, `infra/leaderelection/*`, `infra/storage/{azurebackend,gcsbackend,s3backend,sftpbackend,storagetest}`, `io`, `observability/health`, `observability/promutil`, `observability/redmetrics`, `observability/runtimemetrics`, and `observability/slo`.

