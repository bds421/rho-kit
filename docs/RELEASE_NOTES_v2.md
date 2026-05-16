# rho-kit v2.0.0 — agentic-AI service backend

The v2.0.0 release reframes the kit around **building services that AI
agents call (or that are AI agents themselves)**. The Phase 1–6 audit
shipped the security and operational guardrails that any production
service needs; v2.0.0 adds the agentic-specific primitives that every
agentic service was hand-rolling and getting wrong.

## v2.0.0 lazy-adapter architecture

Heavy adapter wiring (Postgres, Redis, RabbitMQ, NATS, OTel tracing,
public gRPC) moved out of `app/v2` into per-adapter sub-modules under
`app/`:

- `github.com/bds421/rho-kit/app/postgres/v2`
- `github.com/bds421/rho-kit/app/redis/v2`
- `github.com/bds421/rho-kit/app/amqp/v2` (non-loopback `amqp://` panics; mirrors FR-077)
- `github.com/bds421/rho-kit/app/nats/v2`
- `github.com/bds421/rho-kit/app/tracing/v2`
- `github.com/bds421/rho-kit/app/grpc/v2`

`app/v2` no longer transitively pulls pgx, go-redis, amqp091, nats.go,
otelgrpc, or grpc-go. Services declare each adapter they need via
`Builder.With(<adapter>.Module(...))`. See `docs/release/MIGRATION_V2.md`
§8 for the complete `Before → After` migration table.

Snippet status: code blocks in these release notes are illustrative migration
fragments unless explicitly introduced as commands. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## TL;DR

| Theme | What landed | Why |
|---|---|---|
| 1 | Tenant-aware cache, idempotency, ratelimit, label cardinality guard | SaaS substrate that every other agentic primitive layers on |
| 2 | Per-tenant cost budgets (memory + Redis backends, inbound middleware, outbound RoundTripper) | The thing that prevents a misbehaving tenant's LLM bill from blowing past five figures overnight |
| 3 | Append-only signed action log + approval workflow | "What did the agent do this hour against tenant X" becomes a SQL query, not a log grep |
| 4 | MCP helpers — typed handlers as JSON-RPC tools, schema auto-generation | Expose any kit handler as an MCP tool with the kit's full middleware stack reused |
| 5 | SBOM (CycloneDX), `govulncheck` + `osv-scanner` CI, direct dependency allowlist, heavy SDK boundary gate, `THREAT_MODEL.md`, `SUPPLY_CHAIN.md` | "Trusted library" claim is auditable, not marketing |
| 6 | Builder integrations for every new primitive | The kit's golden path (`app.Builder`) reaches the new primitives without each consumer wiring middleware by hand |
| 7 | gRPC RED, DB pool, Redis, Outbox, AMQP, NATS JetStream, Redis Streams, Rate-limit, Storage overview, S3, GCS, Azure, and SFTP Grafana dashboards + 11 runbooks + `promtool` CI | Operations teams stop rebuilding the same panels per service |
| 8 | AWS KMS, Azure Key Vault, GCP KMS, and HashiCorp Vault Transit envelope KEK adapters | Production encryption surfaces are concrete before the API freeze |

Plus: `WithDefaultTimeout` for gRPC (closes threat-model GAP-03),
`httpx.SafeRedirect` (closes GAP-02), JWT revocation checks (closes
GAP-06), internal-only gRPC health (closes GAP-04), outbox self-managed
retention cleanup (closes GAP-09), cross-backend message-size limits
with route overrides (closes GAP-07), direct dependency source
allowlist CI (closes GAP-10), heavy optional SDK boundary CI, and the
`examples/agentic-service` reference binary that wires the entire v2
stack in one file.

Release-candidate artifacts are maintained under `docs/release/`: the
module-level public API freeze, the operational migration guide, and the
RC evidence checklist.

Downstream onboarding (minimum `go.mod`, the smallest compilable program,
and the common first-mistake checklist) lives in
[ai/adoption.md](ai/adoption.md). v2.0.0 ships the lazy-adapter split:
`app/v2` itself imports no heavy SDKs, and services pull in
`app/postgres/v2`, `app/redis/v2`, `app/amqp/v2`, `app/nats/v2`,
`app/tracing/v2`, and `app/grpc/v2` only for the adapters they actually
use, registered via `Builder.With(<adapter>.Module(cfg))`. See
[release/MIGRATION_V2.md §8](release/MIGRATION_V2.md#8-adopter-onboarding-and-the-lazy-adapter-architecture-shipped-in-v200)
for the full mechanical mapping from the removed Builder shortcuts.

## Breaking changes

Runtime and API behaviour changes include the `httpx/middleware/auth`
fail-closed fix below, authtest-only auth context injection helpers, typed
`crypto/signing.Secret` parameters, strict leader-election callback draining,
explicit CORS origins, lifecycle HTTP server fail-fast checks, one-shot
background component starts, and the removal of development mode (next
section). Most remaining v2.0.0 surface is additive over v1.x. New
`app.Builder` methods (`WithPASETO`, `WithLeaderElection`,
`WithSignedRequests`, `MultiTenant`, `TenantBudget`,
`ActionLogger`, `ApprovalStore`) don't change existing
signatures. The v1.x adapter shortcuts `WithPostgres`, `WithRedis`,
`WithRabbitMQ`, and `WithNATS` are removed; their replacement is
`Builder.With(<adapter>.Module(cfg))` from the `app/postgres`,
`app/redis`, `app/amqp`, and `app/nats` adapter modules — see
[Lazy adapter migration](release/MIGRATION_V2.md#8-adopter-onboarding-and-the-lazy-adapter-architecture-shipped-in-v200).

### Background components are one-shot

Background components that own watchers, pollers, sweepers, cleanup loops, or
security-key refresh loops now reject duplicate starts instead of silently
launching competing goroutines against shared state. This includes config
file/env watchers, outbox relays, in-memory idempotency sweepers, HTTP rate
limiter cleanup loops, buffered publisher drain loops, runtime workers/event
buses, cron schedulers, and JWT JWKS providers. Construct a fresh component if
a stopped background service must be started again.

`security/jwtutil.Provider.Run` now returns `error`; callers that manually
start a provider should return or log that error from their lifecycle goroutine.
`app.Builder.WithJWT` already wires this through the lifecycle runner.
`httpx/middleware/ratelimit.Limiter` and `KeyedLimiter` expose
`Start(ctx) error` / `Stop(ctx) error` (renamed from `Run`) and satisfy
`lifecycle.Component`; `app.Builder` wires both methods automatically. Manual
wiring should call `Start` on a background goroutine and `Stop` during
shutdown, propagating any returned error.
`infra/messaging.BufferedPublisher.Run` now returns `error`; manual wiring
should log or return it from the goroutine that owns the drain loop.

### Manual lifecycle HTTP servers must be safe

`runtime/lifecycle.HTTPServer` now panics when the supplied `*http.Server`
has an empty `Addr`, nil `Handler`, or zero `ReadHeaderTimeout`. Manual wiring
must use the same explicit-address, explicit-handler, slowloris-resistant
posture as `httpx.NewServer`; a zero-value server would otherwise bind with
surprising defaults or expose the process-global default mux.

### Redis health checks are critical by default

`infra/redis.HealthCheck` returns a critical dependency check. Services that can
operate in degraded mode without Redis, such as cache-only users, should wire
`infra/redis.NonCriticalHealthCheck` instead. `CriticalHealthCheck` remains an
explicit alias for the default critical behavior.
Per-feature Redis health checks also require an explicit degradation policy;
omitting it now panics at construction instead of silently reporting the feature
as non-critical degraded work.

### ASVS registries are accessors

`security/asvs.Catalog` and `security/asvs.PackageRegistry` are now functions
that return detached copies instead of exported mutable package variables.
Call `asvs.Catalog()` or `asvs.PackageRegistry()` when inspecting the
kit-doctor security manifest. This prevents importers from mutating the ASVS
control catalog or package-evidence registry at runtime.

### RED metric default buckets are accessors

`observability/redmetrics.HTTPLatencyBuckets` and `BatchDurationBuckets` are
now functions that return detached slices. `WithHTTPBuckets` and
`WithBatchBuckets` also clone caller-supplied slices, so later caller mutation
cannot change histogram construction.

### Retry default policies are accessors

`resilience/retry.DefaultPolicy` and `WorkerPolicy` are now functions that
return fresh `Policy` values. Use `retry.DefaultPolicy()` or
`retry.WorkerPolicy()` before applying options or calling `DoWith`; process-wide
retry defaults can no longer be mutated by importers.

### CORS requires explicit origins (functional Option shape)

`httpx/middleware/cors` adopts the kit-canonical functional-Option API.
`cors.New()` with no origins panics. Construct the middleware with
`cors.New(cors.WithAllowedOrigins("https://app.example.com"))` —
or `cors.WithAllowedOrigins("*")` when the wildcard is intentional —
plus `WithAllowedMethods`, `WithAllowedHeaders`, `WithExposedHeaders`,
`WithMaxAge`, and `WithCredentials` for further tuning. Option slices are
copied during middleware construction, so later caller mutation cannot
change the active CORS policy. The previous `cors.Options` struct is no
longer exported.

### Browser policy headers reject padded values

`httpx/middleware/secheaders` and `httpx/middleware/cspnonce` now reject
security-header values with leading or trailing whitespace. Pass `""` to
disable an optional header where supported; whitespace-only strings are no
longer treated as an accidental opt-out or malformed policy.

### Signing key IDs are validated

`crypto/signing.NewStaticKeyStore` now rejects empty, overlong, whitespace,
control-character, invalid-UTF-8, and comma-bearing key IDs. These IDs are
used as request-signature header values, so malformed IDs now fail during
store construction instead of producing requests the verifier later rejects.
Validation errors no longer echo the configured key ID.

### HMAC signing secrets are typed

`crypto/signing.Sign`, `Verify`, `SignContext`, and `VerifyContext` now take a
named `signing.Secret` for the HMAC key argument. Use `signing.NewSecret` or an
explicit `signing.Secret(...)` conversion at the boundary where bytes become a
webhook secret. This keeps the v2 `(secret, body)` order while making accidental
body/secret swaps a compile-time error.

### Credential rotation hooks are explicit

Postgres pgx pools can now use `Config.PasswordProvider` and `Pool.Reset()`;
RabbitMQ can use `amqpbackend.WithURLProvider` (wired through
`app/amqp.Module` + `amqp.WithURLProvider`); NATS exposes username/password
and token providers; S3 supports AWS default-chain or explicit SDK credential
providers; Azure Blob supports `NewWithTokenCredential`; GCS accepts advanced
client options; SFTP accepts a password provider; CSRF accepts secret rings
through `WithSecrets`; and outbound signed requests can use
`sign.WrapKeyStore`. AMQP URL providers and SFTP password providers receive
bounded contexts so secret-manager stalls do not silently stretch startup or
reconnect paths.

### Operational readiness review is explicit

`docs/release/OPERATIONAL_READINESS_V2.md` records the v2 operational review
across every module in `go.work`: credential rotation, TLS material rotation
contracts, startup/configuration bounds, shutdown/draining, bounded work,
observability, health/readiness, migrations, and dependency/runtime gates.
`make check-operational-readiness` fails if a workspace module is missing from
that matrix.

### Key-management errors avoid reflecting key IDs

Action-log signing and envelope-encryption failures no longer echo secret-store
key IDs when the active key is missing, too short, duplicated, or unknown.
Callers still receive typed/sentinel errors where available, but logs do not
copy key handles from config or ciphertext. `data/actionlog.NewStaticSecrets`
also uses a stable short-secret panic that does not echo the configured key ID.
AWS, GCP, and Vault Transit KMS config `LogValue` renderers now report only
whether a key is configured instead of logging full key IDs, aliases, resource
paths, Vault mount paths, or Transit key names.
AWS KMS unwrap validates envelope key identifiers before the SDK call and, for
alias-configured KEKs, decrypts through the configured alias instead of passing
an attacker-controlled envelope key ARN to `Decrypt`.
GCP KMS CRC32C mismatch errors no longer print observed/expected checksum
values for ciphertext or decrypted plaintext responses.

### SSRF-safe transports are host-bound

`security/netutil.SSRFSafeTransport` now refuses to dial a request host other
than the host resolved at construction. Create a fresh transport/client per
user-supplied target; reusing a pinned transport for a different host now fails
closed instead of sending an arbitrary Host header to the pinned IP.

### URL path joining keeps encoded separators opaque

`httpx/urlutil.AppendPaths` now re-encodes percent-encoded `/` and `\` inside
path parts. Existing safe escapes such as `%20` are preserved, but encoded path
separators and dot-segment encodings are kept opaque so a dynamic part cannot
become additional path levels after one downstream decode.

### Problem Details instances are path-only

`httpx/problemdetails.WithInstance` now accepts only path-only URI values
beginning with `/`, with no query, fragment, backslash, whitespace, control
characters, or malformed escapes. Use `r.URL.EscapedPath()` for request-derived
instances so RFC 7807 bodies do not reflect OAuth codes, reset tokens, or
presigned query parameters.

### HTTP URL parse errors avoid reflecting malformed values

`httpx.SafeRedirect` and `httpx/problemdetails.WithBaseURL` now report URL
parse failures without echoing the raw malformed target/base URL. Bad escapes
next to query tokens no longer leak through redirect validation errors or
problem-details option panics.
Unsupported redirect schemes, disallowed redirect hosts, and unsupported
problem-details base URL schemes also use stable error text. Redirect URL host
validation errors no longer wrap lower-level config diagnostics.

### Upload metadata validation errors avoid reflecting request values

Storage object metadata validation and upload-security validators now reject
malformed `Content-Type`, custom metadata keys, disallowed MIME types, and
disallowed filename extensions without echoing the raw request-supplied value.
The typed sentinels are preserved so handlers can still map failures to the
right 4xx status.
Metadata size and count validators also avoid printing rejected lengths or
limits.
Storage key and prefix traversal validators also avoid echoing the rejected
path segment.

### Storage helper errors avoid reflecting object keys

Shared storage copy, batch delete/copy, and migration helpers now return stable
operation errors without copying source or destination object keys into error
strings. Per-key migration result maps still use object keys as map keys for
programmatic recovery, but the stored error values avoid reflecting those keys.
Storage batch helpers also reject batches above `storage.MaxBatchKeys` before
calling backend batch APIs or sequential fallbacks.
Storage migrations now reject nil source/destination backends explicitly and
cap retained per-key errors at `storage.MaxMigrationErrors` while preserving
the total failed count and progress callbacks.
The local-filesystem and in-memory backends use the same stable pattern for
direct get, copy, symlink, and filesystem-operation failures.
Encrypted-storage copy wrappers and S3 direct operation, list, copy, and
presign failures also avoid adding object keys to their own error context.
Azure and GCS direct operation wrappers follow the same rule.
S3 and SFTP operation error metrics now treat expected missing-object outcomes
from `Delete` and `Exists` as successful control flow, matching Azure/GCS, so
cleanup and existence probes do not increment provider error counters.
SFTP operation, list, remote-path containment, and symlink-safety errors no
longer add object keys or derived remote object paths to their own messages.
Checksum mismatch and endpoint scheme validation errors now keep provided
checksum material and unsupported configured schemes out of error strings.
SFTP connect/key-file setup, storage manager close failures, multipart missing
file-part errors, and ClamAV protocol errors now avoid reflecting configured
paths, disk names, form-field names, or raw scanner response text.
SFTP eager/lazy connection, client setup, private-key parse, and temporary
suffix generation failures now use stable messages while retaining causes where
useful.
Multipart upload parsing, skipped-part budget failures, key derivation errors,
and backend store failures now use stable public error strings while preserving
wrapped causes for programmatic checks.
Structured `StorageError` values also keep their operation, key, and wrapped
cause available through fields and `errors.Is/As` without rendering those
values in `Error()`.
Storage and upload-security validator chains now report nil or malformed
validator elements without exposing the failing slice index.
Multipart upload validators are detached before execution, so caller-owned
validator slices cannot be mutated mid-request to alter the remaining chain.
S3, Azure Blob, and GCS backend SDK failures now render stable operation
messages and keep the raw SDK cause only in the unwrap chain, avoiding object
keys, request URLs, and provider diagnostics in public error text and trace
statuses.
Storage backends now route span error descriptions through a shared redaction
helper so SDK and callback errors do not leak runtime values into tracing
exporters.
The in-memory backend applies the same stable wrapping to caller-reader
failures during `Put`.
Encrypted storage now applies stable wrapping to key-provider, plaintext read,
ciphertext read, backend delegation, and forwarded list errors while preserving
causes for `errors.Is/As`.
TLS certificate validation and load errors now avoid reflecting configured
certificate or key file paths.
Atomic state-file load/save helpers also avoid reflecting configured state
paths, symlink ancestors, temp-file names, or destination paths in returned
filesystem errors.

### Idempotency backend errors avoid reflecting keys

Redis and Postgres idempotency stores no longer include the raw idempotency key
in backend, cache-unmarshal, lock, set, or unlock error strings. The HTTP
middleware hashes client-supplied keys, but direct store callers can pass
header-derived keys, so backend diagnostics avoid echoing those values.
HTTP idempotency post-handler cache writes and unlocks now use a bounded
detached request context, so cleanup can survive caller cancellation while still
preserving tenant, trace, logger, and other context values for downstream stores.
Postgres table-name validation also uses a stable startup panic instead of
copying the rejected table name.
Redis cache value-size errors now avoid reflecting cache keys or byte counts.
Idempotency cached-response validators, raw key validators, and Redis key
prefix options now avoid rendering exact rejected lengths.
Cache key, prefix, and combined prefix/key length errors follow the same
stable diagnostic pattern.
Signing key IDs, flag keys/user keys, `_FILE` secret-size failures, JWKS body
caps, and random-string length validation now avoid rendering exact rejected
lengths.
Malformed JWT `permissions` and `scopes` claims now reject tokens with stable
errors/log attributes instead of wrapping caller-controlled claim parser detail.
Crypto key-size, envelope DEK, and password-hash cost validation, CSRF
SameSite/prefetch/buffer-size option panics, and buffered-publisher full-buffer
errors now avoid echoing supplied numeric values. CSRF random-secret/token
generation panics now use stable text instead of concatenating RNG error detail.

### Redis lock errors avoid reflecting keys

`data/lock/redislock.WithLock` and `LockerWithValue` now return stable
contention errors and omit raw lock keys from release-failure logs.
Their deferred unlock path now uses a timeout-bounded detached caller context,
preserving tenant, trace, logger, and other context values while still releasing
after caller cancellation.

### Pagination helpers fail closed on invalid inputs

`httpx/pagination.ParseCursorParams` and `ParseOffset` now return
`ErrInvalidRequest` instead of panicking when called with a nil request or nil
URL, and reject duplicate pagination query parameters with
`ErrAmbiguousQueryParam` without echoing the duplicated query key. `CursorSigner`
also rejects nil or zero-value signers at use time: construction through
`NewCursorSigner` remains the supported path, and miswired signed cursor
handlers now return 500 instead of issuing HMAC cursors under an empty key.
Short cursor-signer secrets now return a stable construction error without
rendering the supplied secret length. UUID cursor validation no longer wraps
parser text that can contain rejected cursor details.

### HTTP parameter helpers treat ambiguity as invalid

`httpx.ParseID` now returns `(0, false)` for nil requests instead of panicking.
`httpx.ParseBoolParam` now returns nil for nil requests, nil URLs, empty keys,
missing keys, and duplicate query keys, so callers do not accidentally accept
conflicting boolean values.
`httpx.DecodeJSON` now returns `false` and writes a 400 error for nil requests
or nil request bodies instead of panicking before the normal decode-failure
path can run.

### Database URL parsing rejects repeated TLS mode

`infra/sqldb.ParseDSN` now rejects `DATABASE_URL` values with repeated
`sslmode` query parameters. TLS mode is a security boundary; the kit no longer
normalizes an ambiguous URL by silently selecting one value.
Unsupported `DATABASE_URL` schemes and invalid `DB_SSL_MODE` values now use
stable validation text instead of echoing the provided value.
Invalid `DB_HOST` character errors now avoid echoing the offending host data.
pgx COPY table, LISTEN channel, and loopback-host validation errors also avoid
reflecting caller-provided identifiers or host names.
`pgx.Copy` now validates table and column identifiers as portable PostgreSQL
identifiers and rejects row/column batches above `pgx.MaxCopyRows` and
`pgx.MaxCopyColumns` before starting `COPY`.

### Redis URLs cannot disable TLS verification

`infra/redis.RedisConfig.Options` and `ValidateRedisURL` now reject
`skip_verify` in Redis URLs. The upstream Redis client accepts that query
parameter for `rediss://` URLs, but the kit treats certificate verification as
non-optional.
Unsupported Redis URL schemes now avoid reflecting the provided scheme value.
`infra/redis.Connect`, `ConnectUniversal`, and the `app/redis.Module`
adapter (which replaced the removed `Builder.WithRedis` shortcut) now
snapshot caller-owned option structs and clone embedded TLS configs,
applying the same TLS 1.2 floor even when callers bypass
`RedisConfig.Options`.

### NATS configs snapshot caller-owned TLS/options

`infra/messaging/natsbackend.Config.Clone`, `Connect`, and the
`app/nats.Module` adapter (which replaced the removed `Builder.WithNATS`
shortcut) now snapshot caller-owned config before storage or dial.
Custom TLS configs are cloned and raised to the kit TLS 1.2 floor, and the raw
`ExtraOptions` slice is copied so later caller slice mutation cannot alter
Builder/module runtime wiring.

### Messaging bindings snapshot retry policies

`messaging.ComputeBindings`, `FindBinding`, and `amqpbackend.DeclareAll` now
detach returned bindings from caller-owned `*RetryPolicy` values. The helpers
also avoid applying default retry policies by mutating the caller's binding
slice, so consumer retry decisions stay aligned with the topology declared
during setup.

### HTTP TLS options snapshot caller config

`httpx.WithTLSConfig` and `httpx.WithResilientTLS` now clone caller-owned
`*tls.Config` values when the option is created. Reusing an option after
mutating the original TLS config cannot alter server or resilient-client
runtime wiring.
The shared `core/tlsclone` helper also detaches mutable TLS slices and maps
such as ALPN protocols, cipher suites, certificate chains, CA pools, and ECH
config bytes before enforcing each package's TLS floor.

### JWKS URLs cannot carry query secrets

`security/jwtutil.NewProvider` now rejects JWKS URLs with query strings or
fragments and validates the host with the shared URL-host checks. Put JWKS
authentication in a configured HTTP client rather than embedding secrets in a
URL. JWKS URL syntax and scheme errors avoid echoing raw URL components.
JWKS fetch errors for unexpected `Content-Type` headers now avoid echoing the
upstream header value as well.
Background JWKS refresh failures now log only whether a JWKS endpoint is
configured plus a coarse failure kind; the full endpoint URL and raw transport
error are not emitted.
Custom JWKS HTTP clients with `Timeout == 0` are now shallow-copied and given
the default JWKS timeout while preserving caller transports and redirect
policies.
The `app` JWT module no longer logs JWKS URLs, issuer URLs, or audiences; it
logs configured booleans so identity realm paths and service identifiers do not
appear in routine startup logs.

### OpenFGA config logging redacts URLs defensively

`authz/openfga.Config.LogValue` now avoids rendering API URLs, store IDs,
model IDs, user agents, and default-header names or values. It reports only
configured booleans and URL parse/host presence so logging remains safe even
when invalid config is inspected before construction.
Unsupported OpenFGA API URL schemes now use stable validation text instead of
echoing the provided scheme.
Custom OpenFGA HTTP clients with `Timeout == 0` are now shallow-copied and
given the kit default timeout, preserving caller transports and redirect
policies without allowing unbounded authorization checks.
Default OpenFGA HTTP transports now use the shared deep TLS clone helper:
mutable TLS config fields are detached from `http.DefaultTransport`, the TLS
1.2 floor is enforced, and impossible `MaxVersion` settings fail fast.

### Connection URL logging drops query and fragment

Redis, NATS, AMQP, sqldb, and pgx config log renderers now avoid printing URL
userinfo, query strings, fragments, hosts, users, database names, vhosts, and
DSNs. The AMQP reconnect log sanitizer still strips URL secrets from operational
reconnect logs, avoiding accidental token leakage from invalid or legacy URLs.
Storage endpoint config logs now use the same pattern for S3, Azure, and GCS
endpoint overrides.
S3, GCS, Azure Blob, and SFTP config `LogValue` renderers also avoid printing
bucket/container/project/account names, SFTP users, root paths, access-key IDs,
and endpoint hosts; they report only configured booleans for those resource
handles.
Runtime queue, stream, AMQP/NATS, storage-health, storage HTTP, and buffered
publisher logs now use the shared `core/redact` runtime attribute helpers for
message IDs, route names, queue/stream names, storage keys, backend hosts,
state-file paths, and backend/user-callback errors. This keeps operational log
shape without copying tenant-controlled identifiers or SDK diagnostics.
Built-in Redis queue-depth, S3, and SFTP health checks now use
`observability/health.OpaqueCheckName` so `/ready` exposes stable, distinct
check keys without embedding queue names, bucket names, backend hosts, or ports.
`SafeCheckName` remains available for non-sensitive static name parts.
Redis queue and Redis Stream Prometheus labels now use
`observability/promutil.OpaqueLabelValue` for queue, stream, and consumer-group
values, preserving stable series keys without copying Redis key topology into
metric labels.
Cache-compute write-failure logs, outbound budget reconciliation/refund logs,
idempotency post-handler logs, request-signature verification logs, approval
middleware logs, authz deny/error logs, SSRF private-IP opt-in warnings, and
leader-election backend logs now use the shared redaction helpers for keys,
subjects, resources, hosts, leader keys, and backend/callback errors.
Outbox relay store logs, Redis reconnect health/callback logs, and audit-log
drop logs now apply the same runtime redaction to backend errors and
caller-provided audit identifiers.
Config file-watcher and environment-reload diagnostics now redact watched file
paths and loader errors while still reporting reload outcomes.
App module lifecycle, tracing shutdown/init, broker close, internal-server
shutdown, and top-level application error logs now redact runtime errors via
`redact.Error`. The fatal-exit log from `app.Main` additionally records the
unwrap chain of concrete Go error types via `redact.ErrorChain` so operators
keep enough triage information to identify the failing subsystem.
Operator-visible identifiers — module names (for log correlation),
gRPC/internal listener bind addresses (for ops visibility) — are
deliberately NOT redacted; tenant- or attacker-controlled identifiers
(gRPC mTLS user IDs, client identities, request paths) ARE redacted to
match the HTTP middleware path.
Low-level helper diagnostics in config secrets, request ID fallback, HTTP JSON
writes, health response encoding, cursor pagination, cron shutdown, Redis-lock
release, and MCP internal/action-log paths now use redacted runtime errors and
identifiers.
`observability/logattr.Error` now redacts error strings by default, so shared
runtime/lifecycle/error-handler logs keep error type without copying backend
diagnostics.
The shared `observability/logattr.URL` helper now validates URL-shaped values
but redacts the full runtime string by default.
Generic HTTP path log attributes now redact request paths by default across
access logs, request-scoped loggers, panic recovery, and service-error logs.
`httpx.RequestPath` remains available for non-log metadata that must preserve
escaped delimiters.
`observability/logattr` runtime-identifier helpers for addresses, user IDs,
instances, operations, queues, topics, and streams now redact values by
default; use explicit application attributes only for intentionally
non-sensitive static labels.
HTTP and gRPC request/correlation ID adoption now requires a singleton safe
correlation token. Free-form values such as emails, URL paths, comma-joined
lists, and `key=value` strings are regenerated rather than echoed into
response headers, propagated downstream, or copied into logs.
HTTP and gRPC auth permission helpers now clone permission slices when storing
or returning context values, so caller-side slice mutation cannot change
audit/logging-visible auth context after authentication.
`core/contextutil.Key.Set` and `core/tenant.WithID` now normalize nil contexts
to `context.Background()`, matching the nil-safe read helpers and preventing
test scaffolds or optional helper paths from leaking nil contexts downstream.

### Connection URL parse errors avoid reflecting malformed values

Redis, NATS, AMQP, PostgreSQL DSNs, storage endpoint overrides, OpenFGA API
URLs, and JWKS URLs now report parse failures without wrapping Go's raw
`url.Parse` error string. Malformed config values with query tokens or
credentials no longer get copied into startup errors.
Shared config numeric validators also avoid echoing rejected port and positive
integer values while keeping the config field label in the error.
`core/config.Load` also uses stable messages for invalid generic type
parameters and malformed `env` struct-tag options instead of reflecting type
names, field names, or rejected tag options.

### JSON marshal fallback responses are not cacheable

`httpx.WriteJSON` now sets `Cache-Control: no-store` on its internal 500
fallback when JSON marshaling fails for a nominal success response. Generated
error bodies keep the same cache policy as ordinary error responses.

### Operation failures no longer expose messages

`httpx.WriteServiceError` and RFC 7807 problem-details mapping now return a
generic `internal error` body for `apperror.OperationFailedError`. The full
operation-failure message remains available to logs and programmatic error
inspection, but a mistaken `apperror.NewOperationFailed(err.Error())` can no
longer leak SQL, hosts, DSNs, or tokens through the shared HTTP adapters.

### Debug AMQP errors avoid reflecting request values

`infra/messaging/amqpbackend/debughttp.ConsumeHandler` now returns a generic
unknown-type response instead of echoing the submitted message type. The
registered-type listing endpoint remains available for guarded operator use.

### Messaging metadata errors avoid reflecting values

Redis queue message-ID validation, shared binding validation, routing-key
lookup failures, and versioned-handler dispatch misses now return stable error
text instead of copying message IDs, message types, queue names, routing keys,
or unsupported exchange-type values into error strings.
Shared queue, Redis Stream, and messaging metadata validators also avoid
rendering exact rejected identifier/header lengths.
Action-log list, chain-verification, and in-memory duplicate-ID failures now
also avoid copying entry IDs or tenant IDs into error strings.
Action-log entry ID, audit-log token/metadata, outbox message metadata, health
check name, and Prometheus static-label length errors now avoid exact rejected
sizes as well.
`core/tenant` validation errors no longer echo the offending whitespace or
forbidden byte from request-derived tenant IDs.
Tenant ID/key-part, CSRF session ID, and authz request-part length errors now
avoid rendering exact rejected sizes.
Approval duplicate-ID and invalid-transition errors use the same stable-message
pattern across memory and Postgres stores.
Outbox header decode errors, relay logs, and persisted `LastError` values no
longer copy outbox entry IDs, message IDs, routes, or raw publisher error text.
Messaging schema registration, schema lookup, payload decode, validating
handler, and AMQP publish confirmation errors now avoid adding message IDs,
message types, schema versions, exchanges, or routing keys to error strings.
NATS and AMQP URL scheme validation, NATS stream setup, and NATS publish errors
also avoid adding configured schemes, stream names, or subjects to returned
errors.
Redis queue command wrappers and Redis stream publish wrappers also avoid
adding queue or stream names to backend error strings.
AMQP topology declaration and binding wrappers now follow the same rule for
configured exchanges and queues.
Redis consumer binding/group mismatch errors and AMQP closed-delivery-channel
errors also avoid adding configured queue or group names.
Message-size rejection errors preserve size and limit details but no longer
copy exchange or routing-key values.

### Middleware errors avoid reflecting configured header names

CSRF and idempotency middleware now return stable error text for missing,
duplicated, invalid, or body-mismatched headers instead of echoing configured
custom header names into public responses.
Idempotency fingerprint-header and semantic-header validation errors now use
the same stable text in internal error paths.
Idempotency duration option panics also avoid echoing the invalid duration.
Client-IP trusted-proxy parsing now avoids echoing malformed configured CIDR/IP
entries.

### URL helper panics avoid reflecting values

`httpx/urlutil.MustJoin` and `ParseRequestURIOrPanic` now panic with stable
messages when their static URL input is malformed, rather than copying raw URLs
or query tokens into panic output.

### Middleware option panics avoid reflecting values

CSRF and idempotency option validators now panic with stable messages for
invalid custom cookie names, header names, HTTP methods, and cookie paths
instead of echoing the configured value.
Request signing, budget, and security-header option validators follow the
same pattern for invalid included/required header names, budget scopes, and
frame options.
Storage MIME/extension allowlist validators also avoid reflecting malformed
configured MIME types, wildcards, or filename extensions in panic text.
`infra/sqldb.ValidateColumn` now uses a stable panic message for unsafe SQL
identifier input instead of copying the rejected name.
Redis queue consumer-ID validation and buffered-publisher state-load panics
also avoid echoing configured identifiers, active queue names, or state file
paths.

### Runtime duration and cache TTL errors avoid reflecting values

Retry policy validation, cron job timeouts, batchworker jitter/timeouts, CSRF
issuer TTL validation, circuit-breaker error-rate thresholds, AMQP retry-delay
validation, RED metric histogram bucket validation, compute-cache TTL
validation, Redis queue heartbeat validation, and memory/Redis cache
negative-TTL errors now use stable text instead of echoing the invalid duration
or numeric configuration value.

### MCP argument decode errors avoid reflecting values

`httpx/mcp` now returns a stable `invalid arguments` message when JSON argument
decoding fails. Custom `UnmarshalJSON` errors, such as `time.Time` parse
failures, no longer echo caller-supplied argument strings in JSON-RPC responses.
Malformed JSON, unknown method/tool names, and oversized request bodies also
use stable JSON-RPC error messages without echoing request values or configured
body-size limits.
Unknown JSON-RPC argument fields are also logged with stable text rather than
the decoder's caller-controlled field name.
Message-only validation errors and not-found errors returned by MCP handlers
now map to stable JSON-RPC text; structured field-validation details remain
available for schema-backed argument correction.

### Unexpected validator errors are server errors

`core/validate.Struct` still returns structured field validation errors for
ordinary bad input, but unexpected validator failures no longer become
client-facing validation messages. They are mapped to operation failures with
the original cause retained for logs.

### Budget middleware uses the standard HTTP error envelope

`httpx/middleware/budget` now emits the same JSON error envelope and `no-store`
cache policy as `httpx.WriteError` for missing keys, invalid keys, key-function
panics, and backend outages. The budget-exceeded response keeps its structured
429 body and advisory budget headers.
`httpx/middleware/cors` and `httpx/middleware/cspnonce` now use the same
envelope for request/header validation and nonce-generation failures.
`httpx/middleware/signedrequest` now does the same for malformed signatures,
replays, oversized bodies, and nonce-store/operator failures while preserving
its existing status-code mapping.
`httpx/middleware/ratelimit/tenant` also now uses the standard envelope for
missing tenant IDs, limiter failures, and tenant-scope 429s.
`httpx/slohttp`, observability health marshal fallbacks, and the agentic
example's tenant-demo endpoints now keep JSON error envelopes instead of
falling back to `http.Error` text bodies. `httpx.WriteError` also maps
405, 408, and 415 to specific machine-readable codes rather than the generic
`INTERNAL` code.
HTTP-facing startup panics in CORS, CSRF origin allowlists, and `httpx/authz`
action/resource helpers now use stable text instead of wrapping lower-level
validation errors that can contain rejected option values.
The same stable-panic pattern now covers cron job names, batch-worker names,
eventbus handler/event names, compute-cache names, message-size route overrides,
in-memory authz grants, Redis queue/stream names, and NATS URL validation.
It also covers panic-on-error convenience constructors and option validators in
`core/config`, `core/randstr`, `core/tenant`, `crypto/signing`,
`security/csrf`, `httpx/sign`, `httpx/pagination`, `httpx/problemdetails`,
client-IP and security-header middleware, messaging headers/subscriptions,
memory cache, flags, SQL/HTTP/gRPC metrics, `observability/promutil`,
`observability/redmetrics`, and storage backend instance labels.
Formatted startup panics no longer print numeric caps, option indexes, schema
versions, or key lengths; callers get fixed diagnostic text and can use the
error-returning APIs where runtime detail is needed.
The SSRF redirect-following validating transport now fails closed when
misconstructed without its guarded inner transport instead of falling back to
the process-global `http.DefaultTransport`.
`security/jwtutil.LoadJWTFields` no longer defaults unset `JWKS_URL` to an
identity-provider-specific endpoint; services must provide their issuer's JWKS
URL explicitly. JWT/auth and recipe docs now describe generic JWKS issuers
instead of naming a specific identity proxy.

### Lock-token generation returns errors

`data/idempotency.GenerateToken` now returns `(string, error)`, and the memory,
Redis, and Postgres idempotency stores propagate crypto/RNG failures through
`TryLock`. `data/lock/redislock.Locker.Acquire` now returns token-generation
failures as errors instead of panicking on the request path.

### Signed-request nonce generation returns errors

`httpx/sign.SignRequest` now returns crypto/RNG nonce generation failures as
ordinary signing errors. The signer still never falls back to a predictable
nonce, but it no longer crashes the process on a request path when the OS RNG
is unavailable.

### Signed-request validation errors avoid reflecting request values

`httpx/sign` and `httpx/middleware/signedrequest` now report invalid request
methods, hosts, and malformed signature timestamps without echoing the raw
request value. This keeps attacker-controlled request metadata out of debug
logs and returned signing errors while preserving sentinel errors for callers.
Signed-request canonical header validation also no longer echoes required
header names. Signed-request body-buffer read, close, and size errors now use
stable text instead of echoing caller reader diagnostics or configured/request
byte counts, while preserving wrapped causes and body-size sentinels for
callers. The legacy `httpx/reqsign` package was removed in v2.0.0 — services
should use `httpx/sign` with `httpx/middleware/signedrequest`. Signed-request
verifier header-length errors and Redis nonce-store length checks follow the
same stable-diagnostic pattern.

### Header validation errors avoid reflecting metadata

`data/idempotency`, `data/stream/redisstream`, and `infra/messaging` now keep
cached-response, stream, and broker header validation errors stable instead of
copying header names into error text. `authz/openfga` applies the same rule to
configured default headers, and `observability/tracing` applies it to OTLP
exporter headers. Sentinel errors and size diagnostics are preserved.
Tracing compression and endpoint-character validation now also avoid echoing
the configured value.
`httpx/mcp` registration and schema-generation errors now avoid echoing tool
names or reflected Go type names while preserving sentinel errors for callers.
Prometheus metric name-part validation now avoids echoing invalid namespace or
subsystem values.

### PASETO parse errors avoid reflecting token material

`crypto/paseto` now returns stable `ErrTokenInvalid` authentication failures
when v4.public or v4.local parsing fails, instead of wrapping parser detail
from attacker-supplied token strings.
Issuer/audience mismatch, reserved custom-claim, and custom-claim encoding
errors also avoid reflecting token claim values or configured expectations.

### CSRF token mint failures return 500

`httpx/middleware/csrf` now returns a safe 500 response when request-time
double-submit token generation fails, rather than panicking while serving the
request. Startup-time development secret generation still fails fast.

### CSRF origin allowlist panics avoid reflecting values

`security/csrf.NewOriginAllowlist` and `httpx/middleware/csrf.WithAllowedOrigins`
now reject malformed configured origins without echoing the full origin string.
Bad escapes next to token-like query strings cannot leak through startup panic
messages.

### mTLS identity allowlist panics avoid reflecting values

`httpx/middleware/auth.WithAllowedSANs`, `WithAllowedCNs`, and the matching
`grpcx/interceptor` options now reject malformed configured identities without
echoing the full SAN/CN string. URI-shaped SANs with malformed escapes or query
tokens cannot leak through startup panic messages. Both transports share the
new `security/mtlsidentity` normalizer so SAN/CN parsing semantics stay aligned.

### Storage upload key generation returns errors

`infra/storage/storagehttp.UUIDKeyFunc` now returns an error if UUID generation
fails instead of panicking while deriving an upload object key. Invalid
configured prefixes now panic with stable text rather than wrapping validator
details.

### Audit event ID generation returns errors

`observability/auditlog.Logger.LogE` now returns UUID generation failures and
records them through the existing drop counters/hooks instead of panicking while
auto-populating an event ID.

### Action log ID generation returns errors

`data/actionlog` now returns UUID generation failures from `Append` instead of
panicking while auto-populating an entry ID. `WithIDFunc` remains available for
deterministic tests, and `WithIDFuncE` supports error-returning ID sources.

### Approval ID generation returns errors

`httpx/middleware/approval` now returns a safe 500 response when approval ID
generation fails instead of relying on UUID panics and recovery on the request
path. `WithIDFunc` remains source-compatible, and `WithIDFuncE` supports
error-returning ID sources.

### Approval idempotency preserves audit metadata

`data/approval.Store.Decide` treats a repeated same-direction decision as a
pure no-op. The original `DecidedBy`, `Reason`, and `DecidedAt` are preserved
so a replay or second operator cannot rewrite who approved or rejected the
request.

### Redis queue consumer ID generation can return errors

`data/queue/redisqueue.WithConsumerID` skips auto-generation entirely for
operators that provide stable consumer identities. `NewQueue` panics on
construction failures (including the pathological case of consumer-ID
generation failing — see wave 109 for the rationale).

### Startup panics avoid reflecting configured names

`flags/memory`, `runtime/cron`, `observability/slo`, and
`infra/storage.Manager` now reject invalid flag keys, non-marshallable
in-memory flag objects, invalid cron schedules, duplicate SLO names, and
invalid disk registrations with stable panic messages that do not echo the
configured key, schedule, job name, SLO name, or disk name.
`core/contextutil.Key.MustGet` also avoids echoing the configured diagnostic
key name when required context is absent. `app.Builder` and
`app.ModuleContext` duplicate/missing-name startup panics now avoid echoing
module and keyed-rate-limiter names.
`cmd/kit-new` also avoids echoing internal template gate names if a scaffold
table row is miswired.
Health HTTP/gRPC readiness constructors now panic with stable text for invalid
checker wiring instead of wrapping dependency-check names from `ValidateChecker`.
The underlying `observability/health` check-name and dependency-check
validators also avoid echoing rejected check names.
`resilience/retry` policy validation panics also use stable text rather than
combining a call-site label with validation details.
Redis connection/metrics/degradation options and HTTP rate-limit trusted-proxy
options now do the same for invalid instance, feature, policy, and proxy names.
Rate-limit key and Redis prefix length diagnostics now use stable text while
preserving invalid-key sentinels for callers.
Outbound HTTP and AMQP TLS-floor helper panics no longer include caller labels.
Temporal dial failures now avoid reflecting configured `HostPort` values.
App module init, module health-check, gRPC listen, keyed rate-limiter, and
internal-host validation errors now avoid reflecting configured module names,
listener addresses, limiter names, or bind hosts.
Lifecycle runner panic and shutdown-deadline errors now avoid reflecting
configured component names.
Event bus dispatch and handler type-mismatch errors now avoid reflecting
configured handler names or Go type names.

### Approval resources preserve encoded path delimiters

`httpx/middleware/approval` now uses `URL.EscapedPath()` for default
action/resource metadata. Approval records no longer collapse paths such as
`/files/a%2Fb` and `/files/a/b` into the same resource string.

### Request path metadata preserves encoded delimiters

`httpx.RequestPath` is now the shared default for request-derived path
metadata. Problem-details instances, tracing attributes, audit-log
filters/resources, and approval metadata preserve encoded path delimiters
consistently. Generic log attributes derived from request paths redact the raw
value before writing to sinks.

### Audit-log default skips avoid prefix collisions

`httpx/middleware/auditlog` now skips only `/health`, `/ready`, `/metrics`,
and their slash subpaths by default. Application routes such as
`/health-delete-user` are no longer accidentally excluded from audit capture.

### SSRF URL errors avoid reflecting malformed URLs

`security/netutil` no longer echoes raw URL, host, port, or dial-address values
when rejecting malformed, empty-host, or unsafe SSRF targets. Malformed
user-supplied URLs such as `http://?token=...` cannot leak query secrets
through validation error strings, and token-like host values are not reflected
from host validation or DNS failure paths.

### Config URL errors avoid reflecting malformed values

`core/config.Load` no longer echoes malformed `*url.URL` values when parsing
fails or a URL is missing a scheme or host. Startup validation errors keep the
env var name and reason without copying possible tokens from the configured
value.

### Secret config parse errors redact values

`core/config.Load` now redacts `secret:"true"` values in typed parse errors for
durations, integers, booleans, and floats. Secret-tagged fields keep the env var
name and expected type in the error without copying the secret value itself.
Non-secret typed scalar parse errors now follow the same no-value pattern.
The standalone `core/config.GetInt`, `GetBool`, and `GetFloat64` helpers now
follow the same no-value-in-errors pattern for all parse failures. Secret
`_FILE` read failures keep the env var name but no longer echo the configured
file path.
`core/randstr` duplicate-character validation now avoids echoing caller-supplied
charset contents.
`core/validate.RegisterValidation` late-registration errors now avoid echoing
custom validation tag names.

### Tracing endpoint logging hides invalid URL-shaped values

`observability/tracing.Config.LogValue` now renders invalid endpoint shapes as
`[INVALID ENDPOINT]` instead of echoing a URL-shaped value that may contain
credentials or query tokens. Valid `host[:port]` endpoints still render
normally. Runtime tracing fallback logs and the `app` tracing module startup
log no longer print even valid collector endpoints; they report configured
booleans and coarse error kinds instead. OTLP exporter header names and values
are also omitted from `Config.LogValue`; it now reports only whether headers
are configured, and `Init` snapshots the header map before validation/exporter
setup.

### Tracing endpoint validation avoids echoing invalid syntax

`observability/tracing.Config.Validate` now reports malformed endpoint syntax
without echoing the full endpoint string. Invalid collector endpoints cannot
leak embedded token-like values through startup validation errors.

## Breaking change: no development mode

The kit no longer has a development mode. Production-safe defaults are
the only mode, and the `app.Builder` runs the production-safety validator
unconditionally at `Build()` time. There is no `KIT_ENV` (or `APP_ENV`)
escape hatch in any kit code path — the runtime no longer reads those
env vars to weaken safety checks.

### Removed: `WithProductionDefaults()`

`Builder.WithProductionDefaults()` is gone. Its checks (JWT issuer/
audience pinning, TLS-required, internal-host loopback, postgres sslmode,
tracing sample-rate cap, `TenantBudget` + `MultiTenant` pairing)
now run unconditionally in `Builder.Build()`. Migration is to delete
the `WithProductionDefaults()` call from your chain.

### Renamed: opt-out methods

The four explicit opt-outs lost their `Production` prefix and now read
as deliberate "I know what I'm doing" declarations. Behaviour is
identical, only names changed:

| Old | New |
|---|---|
| `WithProductionAllowPlaintext` | `WithoutTLS` |
| `WithProductionInternalExposed` | `AllowInternalNonLoopback` |
| `WithJWTAllowAnyIssuer` | `WithoutJWTIssuer` |
| `WithJWTAllowAnyAudience` | `WithoutJWTAudience` |

Each opt-out remains documented as a per-relaxation declaration the
operator must apply consciously (network isolation confirmed,
external TLS terminator in place, etc.).

### Removed: `KIT_ENV` runtime checks

The kit no longer reads `KIT_ENV` (or `APP_ENV`) to decide whether to
enforce a check. The following call-site checks went unconditional:

- `app/jwt_module.go` — issuer enforcement was previously gated on
  `KIT_ENV != development`; the Builder validator now rejects missing
  issuer upstream.
- `security/jwtutil.NewProvider` — used to panic on missing issuer
  outside development; now accepts any configuration the caller hands
  it (Builder validates upstream; standalone callers should still
  pair `WithExpectedIssuer` or `WithAllowAnyIssuer`).
- `infra/sqldb/pgx.Connect` — TLS check is unconditional; loopback-only tests
  can pass `Config{AllowPlaintextLoopbackForTests: true}` to opt out.
- `infra/messaging.NewBufferedPublisher` — state-file requirement is
  unconditional; the existing `WithEphemeralBuffer()` is the only
  opt-out.
- `httpx/middleware/csrf.New` — HMAC secret requirement is
  unconditional; the existing `WithDevSecret()` is the only opt-out.

The `examples/agentic-service` reference binary's "refuse to run when
KIT_ENV looks like production" guard was removed — the example is now
documented as illustrative-only via package comments, and consumers
that want production wiring use `app.Builder` whose always-on validator
catches missing TLS/auth/sslmode at startup.

`core/config.IsDevelopment` and `BaseConfig.Environment` are
preserved for downstream consumers' own logic; the kit's core path
simply stops calling them.

### Migration

```go
// Before
b := app.New("my-service", "v2.0.0", cfg).
    WithProductionDefaults().
    WithProductionAllowPlaintext().
    WithProductionInternalExposed().
    WithJWTAllowAnyIssuer().
    WithJWTAllowAnyAudience().
    Run()

// After
b := app.New("my-service", "v2.0.0", cfg).
    // The validator runs automatically; remove WithProductionDefaults().
    WithoutTLS().                  // was WithProductionAllowPlaintext
    AllowInternalNonLoopback().     // was WithProductionInternalExposed
    WithoutJWTIssuer().            // was WithJWTAllowAnyIssuer
    WithoutJWTAudience().          // was WithJWTAllowAnyAudience
    Run()
```

Most services don't pass any opt-outs; the migration in that case is
just to delete `WithProductionDefaults()`.

### Auth middleware now fails closed (security fix)

`auth.RequirePermission`, `auth.PermissionByMethod`, and
`auth.RequireScope` previously fell through to the next handler when
the request carried no permissions/scopes claim — the rationale being
that mTLS-authenticated internal services don't carry one. The
implementation hooked that bypass to *absence of the claim* rather
than to the verified-mTLS path, so a route mounted without any auth
middleware in front silently granted full access, and a JWT issued
without the permissions claim implicitly bypassed RBAC.

**v2.0.0**: `RequireS2SAuth`'s mTLS branch now stamps a trusted-S2S
marker onto the context; the three middlewares bypass ONLY when that
marker is present. Any other condition (no claim, no marker, no auth
middleware) returns 403.

Migration for downstream services that relied on the implicit bypass:

1. Wire `RequireS2SAuth` properly so verified-mTLS callers get the
   marker (preferred — preserves the original intent).
2. Issue JWTs with explicit permissions claims for the routes that
   need them.
3. Use `httpx/authz.RequirePermission` with a `Policy` if the bypass
   condition is more nuanced than "verified internal cert".

`RequireScopeStrict` is unchanged — it intentionally does NOT honor
the marker.

### Other soft incompatibilities

1. **`BufferedPublisher` requires a state file in non-dev**
   environments. Set `WithStateFile(path)` or pass
   `WithEphemeralBuffer()` for an explicit opt-out backed by an
   upstream outbox. Dev environments warn but allow ephemeral
   buffering.

2. **JWT issuer pinning is enforced unconditionally**. Calling
   `WithJWT(...)` without `WithJWTIssuer(...)` (or the explicit
   `WithoutJWTIssuer()` opt-out) fails `Builder.Build()` validation.
   Migration: chain the issuer call.

3. **MCP server doesn't implement JSON-RPC batch**. This is a
   permanent design choice for v2: single-call semantics keep the
   action-log entry per-call rather than per-batch (forensics is
   cleaner) and avoid the partial-failure footgun where a batch with
   one denied call must still return success for the others. Bodies
   starting with `[` are rejected with `-32600 Invalid request`. SDK
   consumers should call tools sequentially or in parallel HTTP
   requests, not via JSON-RPC batches.

4. **Feature flag keys and user targeting keys are validated at the
   kit boundary**. `flags.Client` error-returning getters now reject
   nil clients, nil contexts, empty/overlong flag keys, invalid UTF-8,
   whitespace, and control characters before calling OpenFeature. The
   convenience getters still return fallback. `flags.MemoryProvider`
   panics on invalid setup keys and returns OpenFeature parse errors
   for invalid direct provider evaluation calls.

5. **Authorization triples are validated before backend dispatch**.
   `authz.Allow`, `authz.Memory`, `httpx/authz.RequirePermission`,
   and the OpenFGA adapter now reject empty/overlong subject-action-
   resource parts, invalid UTF-8, whitespace, and control characters
   before asking a policy engine. Malformed triples wrap both
   `authz.ErrInvalidRequest` and `authz.ErrDenied` so callers can audit
   the validation failure while still failing closed.

6. **Storage decorators validate before side effects**. `storage.WithHooks`,
   `storage/retry`, and `storage/circuitbreaker` now validate keys and
   list prefixes/options before running hooks, retry policies, or
   circuit-breaker state transitions. Hooks receive cloned metadata, so
   callback code can observe an operation without mutating the metadata sent
   to the backend.

## New Builder methods (the migration guide)

```go
import (
    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/data/v2/actionlog"
    budgetredis "github.com/bds421/rho-kit/data/budget/redis/v2"
    actionlogpg "github.com/bds421/rho-kit/data/actionlog/postgres/v2"
    approvalpg "github.com/bds421/rho-kit/data/approval/postgres/v2"
    httpxtenant "github.com/bds421/rho-kit/httpx/v2/middleware/tenant"
    leaderpg "github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2"
    natsbackend "github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

b := app.New("my-service", "v2.0.0", cfg).
    // Production-safety validator runs automatically — no opt-in needed.

    // Auth (pick one or both — they coexist for migration)
    WithJWT(cfg.JWKSURL).WithJWTIssuer(cfg.Issuer).WithJWTAudience(cfg.Audience).
    WithPASETO(pasetoProvider).

    // Multi-tenant — extracts X-Tenant-Id by default; required on
    // state-changing methods, GET/HEAD/OPTIONS pass through.
    MultiTenant(httpxtenant.HeaderExtractor("X-Tenant-Id")).

    // Per-tenant cost budgets. Default key = tenant.FromContext.
    TenantBudget(budgetredis.New(redisClient, 1_000_000, time.Hour)).

    // Append-only signed action log
    ActionLogger(actionlog.New(actionlogpg.New(db), secrets)).

    // Approval workflow for destructive routes
    ApprovalStore(approvalpg.New(db)).

    // pgx-native Postgres pool for queries, LISTEN/NOTIFY, and COPY
    // (lazy adapter — pulls pgx in only when this Module is registered)
    With(postgres.Module(pgxbackend.Config{DSN: cfg.PgxDSN})).

    // Leader election for cron jobs
    WithLeaderElection(leaderpg.New(db, "my-service")).

    // Service-to-service request signing
    WithSignedRequests(keyResolver, signedrequest.NewMemoryNonceStore(time.Minute)).

    // NATS JetStream (independent of the amqp adapter; both can coexist)
    With(nats.Module(natsbackend.Config{URL: cfg.NATSURL})).

    RunContext(ctx)
```

## What composes with what (middleware chain order)

When the Builder assembles the public mux, middleware is composed
**outermost to innermost**:

```
signedrequest → tenant → budget → recovery → logging → tracing → router
```

- `signedrequest` is outermost so unsigned requests reject before any
  tenant or budget work runs.
- `tenant` runs before `budget` because the budget middleware reads
  tenant ID from ctx.
- `recovery`, `logging`, `tracing` are part of `stack.Default`.

## New primitives (skim of what shipped)

### Multi-tenant
- `core/tenant` — type-distinct `tenant.ID`, ctx helpers, `Required`, and length-prefixed `Key(ctx, parts...)`
- `data/cache/tenant.Wrap(c)` — namespaces every key with the shared tenant key encoder; cache bulk helpers and memory/Redis/tenant bulk paths reject batches above `cache.MaxBulkKeys` before allocating maps or sending backend command batches
- Redis queue and stream batch publish/enqueue helpers reject oversized batches before building command pipelines; Redis stream consumer batch size is capped by the same `redisstream.MaxBatchMessages` limit
- `data/idempotency/tenant.Wrap(s)` — namespaces idempotency keys, not the body fingerprint
- `cmd/kit-new -tenant` — scaffolds strict `X-Tenant-Id`, Redis, tenant cache, and tenant idempotency wiring
- `httpx/middleware/ratelimit/tenant` — per-tenant limit on top of IP limit (both must pass)
- `observability/promutil/labelguard` — drops + counts disallowed label values to prevent cardinality explosion

### Cost budgets
- `data/budget` — `Budget` interface with `Consume`/`Peek` plus optional `Refunder` capability
- `data/budget/memory` — single-process backend
- `data/budget/redis` — atomic Lua, cross-instance, `WithRedisTime` for clock-skew-free fairness; caps must fit Lua's exact integer range
- `httpx/middleware/budget` — inbound charge per request, fails closed on missing/invalid keys, rejects exhausted budgets with `429 + X-Budget-Remaining + Retry-After`
- `httpx/budget` — outbound `RoundTripper` with two-phase reconciliation (estimate → actual)

### Action audit + approval
- `data/actionlog` — append-only signed entries; HMAC rotation via `SignatureKeyID` carried per entry
- `data/actionlog/{memory,postgres}` — backends; postgres ships a migration
- `data/approval` — pending → approved/rejected → executed lifecycle
- `data/approval/{memory,postgres}` — backends with `SELECT FOR UPDATE` in `Decide`
- `httpx/middleware/approval` — wraps a destructive route, returns 202 + approval ID

### MCP
- `httpx/mcp` — typed `Handler[In, Out]`; auto-generates JSON-Schema from struct tags
- Reuses kit's middleware stack (auth, tenant, ratelimit, budget, approval, action log)
- Strict MCP audit now requires actor attribution when an action logger is
  configured. The default no longer records `anonymous` unless the service
  explicitly opts in with `WithAllowAnonymousActor()`.
- `cmd/kit-new --mcp` flag scaffolds a sample tool registration

### Trust signals
- `.github/workflows/sbom.yml` — CycloneDX SBOM on tag push
- `.github/workflows/vuln.yml` — `govulncheck` + `osv-scanner` on PR / push / weekly
- `docs/audit/dependency-allowlist.txt` + `make check-dependency-allowlist` — exact source ledger for direct external Go dependencies
- `make check-dependency-boundaries` — keeps Redis, pgx, cloud, messaging, KMS, OpenFGA, Temporal, River, and Testcontainers deps behind adapter/test boundaries
- `make check-operational-readiness` — verifies the release operational review covers every workspace module
- `docs/audit/THREAT_MODEL.md` — STRIDE threat ledger tracking shipped mitigations. GAP-01 through GAP-10 are closed in v2.0.0; three LOW residual doc-fidelity follow-ups (GAP-11 typed auditlog tenant field, GAP-12 messaging buffer-full sentinel, GAP-13 binary marshaler on secret.String) remain open and are tracked in §8 of the threat model.
- `docs/audit/SUPPLY_CHAIN.md` — pinning policy, direct dependency allowlist, heavy SDK boundary guard, release provenance, key rotation, vuln SLO
- `security/asvs.Lookup` uses stable unknown-control errors instead of echoing
  the rejected control ID.

### Command tooling diagnostics
- `kit-new`, `kit-migrate`, `kit-verify`, and `kit-doctor`
  now use stable validation and filesystem-diagnostic text for rejected
  arguments, probe URLs/paths, migration targets, scaffold destinations, and
  threshold/metric options instead of echoing raw command-line values or local
  paths.
- ASVS scanners, S3 presign TTL validation, problem-details extension and
  instance validation, feature-flag provider install errors, and messaging
  consumer binding validation now avoid echoing rejected paths, TTLs,
  extension keys, instance strings, provider domains, or routing keys in error
  text.
- `infra/storage/localbackend` now normalizes filesystem failures across root
  setup, put/get/delete/exists/copy/list, preserving common `os` sentinels
  without leaking local absolute paths.
- `infra/storage/sftpbackend` now normalizes remote operation failures and
  tracing status text so backend errors cannot echo remote paths or object
  keys while preserving missing-object behavior.
- `core/config` secret-file reads now use stable read failures for `_FILE`
  sources, including directory/special-file read errors, without echoing
  mounted secret paths.
- Storage backend instance-name validation no longer reflects rejected label
  lengths through `WithInstance` panic paths.
- `storage.StorageError.Error()` no longer renders object keys; callers still
  retain the structured `Key` field for programmatic handling.
- Storage upload validators and `storagehttp/uploadsec` now use stable public
  errors for MIME reads, size checks, image header parsing/dimension failures,
  malware findings, and scanner failures instead of reflecting raw parser,
  scanner, threat, size, or image dimension details.

### Dashboards & runbooks
- 13 new Grafana dashboards (gRPC RED, DB pool, Redis, Outbox, AMQP, NATS JetStream, Redis Streams, Rate-limit, Storage overview, S3, GCS, Azure, SFTP)
- New Prometheus rules (recording, storage-provider latency, saturation, messaging, rate-limit)
- 11 runbooks under `docs/ai/runbooks/` matching every alert's `runbook_url`
- `promtool check rules` in CI
- The v2.0.0 Prometheus contract freeze covers the dashboarded families above,
  including direct NATS JetStream and Redis Streams messaging metrics.

### gRPC hardening
- `grpcx.WithDefaultTimeout(d)` — per-RPC default deadline; closes threat-model GAP-03 (streaming-RPC exhaustion)
- `app.Builder` serves gRPC health on the internal ops listener over h2c by default; `WithPublicGRPCHealth()` is the explicit opt-in for public gRPC health; closes threat-model GAP-04

### HTTP hardening
- `httpx.SafeRedirect(target, allowedHosts...)` — validates untrusted `next` / return-url parameters before `http.Redirect`; closes threat-model GAP-02 (open redirect)
- `httpx/middleware/tenant` returns a stable invalid-tenant error without reflecting tenant validator details or tenant IDs.
- `httpx/middleware/ratelimit.NewMetrics` plus `WithMetrics` /
  `WithKeyedMetrics` freeze rate-limit Prometheus contracts for allowed,
  limited, invalid-key/client-IP, unavailable, and degradation outcomes without
  exposing raw keys, IPs, tenants, users, or paths as labels.

### JWT hardening
- `security/jwtutil/revocation` — cache-backed `jti` revocation checker wired into `jwtutil.Provider`; closes threat-model GAP-06

### Outbox hardening
- `infra/outbox.Relay` — cleans old published and failed rows on startup and periodic ticks, with `WithRetention` and `WithFailedRetention`; closes threat-model GAP-09

### Messaging hardening
- `messaging.MessageSizeLimiter` — shared 1 MiB default with exact route overrides, wired into AMQP, NATS, Redis Streams, `membroker`, `BufferedPublisher`, and the `app/amqp` / `app/nats` adapter Modules via `amqp.WithMessageSizeLimiter` / `nats.WithMessageSizeLimiter`; closes threat-model GAP-07
- `infra/messaging/amqpbackend.NewMetrics` plus `WithPublisherMetrics` and
  `WithConsumerMetrics` freeze direct AMQP Prometheus contracts for publish
  outcomes, consume outcomes, publish duration, and handler duration.
- `infra/messaging/natsbackend.NewMetrics` plus `WithPublisherMetrics` and
  `WithConsumerMetrics` freeze direct NATS JetStream Prometheus contracts for
  publish outcomes, consume outcomes, publish duration, and handler duration.
- `infra/messaging/kafkabackend` ships as the fourth messaging backend (wave
  130), wrapping `github.com/segmentio/kafka-go` so services can satisfy the
  kit's `messaging.Publisher` / `messaging.Consumer` contracts against Apache
  Kafka. The publisher defaults to Snappy compression and `RequireAll`
  durability; the subscriber pins one Reader per (group, topic) and commits
  offsets on `nil` handler returns, holds the offset on errors (forcing
  redelivery on rebalance/restart), and discards poison pills via
  `apperror.IsPermanent`. `Binding.Retry` from the shared interface is NOT
  honoured — Kafka has no native per-message redelivery; wrap handlers in
  `resilience/retry` or implement a dead-letter topic at the producer level.
  `NewMetrics` plus `WithPublisherMetrics` / `WithSubscriberMetrics` freeze
  the direct Kafka Prometheus contracts. The heavy `segmentio/kafka-go`
  and `testcontainers-go/modules/kafka` deps stay inside the new modules —
  `make check-dependency-boundaries` enforces the isolation.
- `data/stream/redisstream.NewProducerMetrics`, `NewConsumerMetrics`,
  `WithProducerRegisterer`, and `WithConsumerRegisterer` freeze direct Redis
  Stream Prometheus contracts for produce, consume, failure, dead-letter,
  processing-duration, and pending-depth metrics. `infra/messaging/redisbackend`
  inherits those metrics through the stream wrapper.
- Redis queue and Redis Stream handler grace windows, ACKs, retries, and
  dead-letter cleanup now use bounded detached contexts. Cleanup survives
  shutdown/caller cancellation without dropping tenant, trace, logger, or other
  parent context values needed by wrappers and observability.
- `infra/messaging.BufferedPublisher` final drain and Redis/Postgres
  leader-election lock release paths follow the same bounded-detach pattern, so
  shutdown cleanup can finish without losing parent context values.
- Redis/Postgres leader-election `OnAcquired` callbacks now drain strictly after
  lost leadership or parent cancellation. The previous callback drain timeout
  option was removed so a callback that ignores its context stalls the elector
  instead of allowing same-process leader work to overlap on retry.
- `infra/leaderelection/k8slease` ships as the third leader-election backend
  (wave 127), wrapping `k8s.io/client-go/tools/leaderelection` so Kubernetes-
  native deployments can elect leaders against a `coordination.k8s.io/v1` Lease
  object without a side-car Postgres or Redis. The adapter mirrors the kit's
  Callbacks contract on top of client-go's `OnStartedLeading` /
  `OnStoppedLeading` and reuses the same drain watchdog as the pgadvisory /
  redislock adapters. The heavy `k8s.io/client-go` dep stays inside the new
  module — `make check-dependency-boundaries` enforces the isolation.
- Builder module cleanup, tracing shutdown, internal-server shutdown, MCP async
  audit appends, and pgx LISTEN cleanup also use bounded detached contexts for
  after-cancellation cleanup without losing tenant, trace, logger, or other
  context values.

### Storage hardening
- `storagehttp/uploadsec.ScanWith` — generic malware scanner validator contract
- `storage.Validator` and `storage.ApplyValidators` are context-aware, so upload scanners and custom validator I/O honor request/backend cancellation instead of running on `context.Background()`.
- `infra/storage/storagehttp/uploadsec/clamav` — ClamAV `clamd` INSTREAM adapter plus a `storage.Validator` bridge for `storagehttp.ParseAndStore`; closes threat-model GAP-08 without adding third-party scanner SDK dependencies

## Post-wave-156 additions (waves 157–169)

After the core v2.0.0 review work, the kit absorbed thirteen more
waves before the tag — closing every documented-deferred item and
filling the architectural gaps that had been tagged as "v2.x
candidates" in earlier drafts of these notes. Everything below is
in v2.0.0.

### Transport hardening
- **Wave 157 (BREAKING):** `httpx/websocket` production hardening — `WithWriteTimeout`
  (slow-consumer DoS protection), `WithPingInterval` + `WithPongTimeout` (idle
  keepalive heartbeat with `httpx_websocket_pings_total` metric), `WithMaxConnections`
  (CAS-limiter + 503 + `Retry-After` rejection + `httpx_websocket_rejected_total`),
  `WithAnyOriginUnsafe` (audit-grep-able opt-out from same-origin), `Conn.Ping(ctx)`.
  `WithCompression()` default changed from `CompressionContextTakeover` to
  `CompressionNoContextTakeover` — saves ~32 KiB per direction per connection.
- **Wave 166:** `grpcx/interceptor` stream resource discipline — `MaxConcurrentStreamsServer`
  (server-wide cap above gRPC's per-connection limit) + `StreamIdleTimeout`
  (cancels streams with no SendMsg/RecvMsg activity) + `grpc_server_active_streams`
  / `grpc_server_streams_rejected_total` / `grpc_server_streams_idle_closed_total`
  metrics. Mirrors the wave 157 websocket vocabulary.

### Real-time
- **Wave 164:** `realtime/centrifuge` — new module wrapping `github.com/centrifugal/centrifuge`
  with `lifecycle.Component`, JWT auth via `jwtutil.Provider`, bounded-cardinality
  channel-class metrics, and structured log bridging. Fills the gap between
  `httpx/websocket` (raw transport) and `infra/messaging/*` (backend pub/sub)
  for browser-facing real-time with channels, presence, and history.

### Leader election
- **Wave 160:** `infra/leaderelection/etcd` — new module implementing
  `leaderelection.Elector` on top of etcd lease + `concurrency.Election`.
  Recommended for bare-metal / VM deployments that already run etcd. The
  fourth leader-election backend after pgadvisory, redislock, and k8slease.

### Distributed locking
- **Wave 159:** `data/lock/redislock/redlock` — new sub-package implementing
  Antirez's Redlock multi-master quorum algorithm via `redsync` multi-pool.
  Drop-in `lock.Locker` for deployments that need single-instance failure
  tolerance.

### Messaging primitives
- **Wave 165:** `messaging.Subscription`, `messaging.TypedSubscription[T]`,
  `messaging.SubscriptionGroup` — mid-level abstraction wrapping
  `(Consumer, Binding, Handler)` as a `lifecycle.Component`. Typed variant
  decodes JSON payloads to `T` and validates via `validate.Struct` before
  dispatch, mirroring `httpx`'s typed handler contract. Group runs N
  subscriptions concurrently as one lifecycle row.
- **Wave 156:** `outbox.MessagingPublisher` — bridge that adapts any
  `messaging.Publisher` to `outbox.Publisher` so the wave-149 Multiplex
  dispatcher routes to AMQP / Kafka / NATS / Redis backends without per-
  backend reinvention.
- **Wave 158:** `WithOpaqueConsumeLabels` confirmed shipped in wave 140
  (the migration doc's deferral note was stale).

### Contract / spec generation
- **Wave 161:** `openapigen` multiple content types per status —
  `WithResponseContentT[T]` / `WithResponseContent` for additive content
  registration, `WithResponseHeader` for response-side headers.
- **Wave 162:** `openapigen` auto-discovers path parameters from the OAS
  `{name}` template at registration. Explicit `WithParameter` declarations
  override the auto-entry; `WithSkipPathParamDiscovery` opts out entirely.
- **Wave 163:** `openapigen` per-operation `externalDocs` link
  (`WithExternalDocs`), per-(status, mediaType) examples
  (`WithResponseExample`), request examples (`WithRequestExample`), parameter
  examples (`WithParameterExample`), and `Tag.ExternalDocs` emission. Closes
  every scope-limit item the wave 128 introduction flagged as deferred.

### Observability
- **Wave 167:** Messaging trace propagation — `kafkatracing`, `natstracing`,
  `redistracing` sub-packages mirror the existing `amqpbackend/amqptracing`
  helper shape (Carrier over `map[string]string` headers, Inject / Extract
  for W3C trace context, `StartConsumerSpan` / `StartPublisherSpan` with
  backend-specific OTel semconv attributes).
- **Wave 168:** Per-call OTel client spans on every data adapter that
  previously had none — `data/cache/rediscache`, `data/idempotency/pgstore`,
  `data/idempotency/redisstore`, `data/lock/redislock` (covers the redlock
  sub-package via shared types), and `data/lock/pgadvisory`. Keys are
  never attached as span attributes (PII). Cache misses and lock-lost
  conditions surface as attributes, not error statuses.
- **Wave 169:** Per-call OTel spans on `runtime/lifecycle/Runner` (Component
  start/stop), `resilience/retry` (Do / DoWith), and `resilience/circuitbreaker`
  (Execute / ExecuteCtx). Span lifetimes are the full operation so per-attempt
  retry detail is folded into the parent span — tight retry loops do not
  inflate exporter load. `http.ErrServerClosed` and `ErrCircuitOpen` are
  normal control flow and do not light up trace-error dashboards.

### Documentation cleanup
- `MIGRATION_V2.md` §9 ("Things Not Migrated") now lists only one item:
  KMS adapters beyond the four shipped. The previously-deferred etcd
  leader-election and messaging consume-labels entries were resolved
  in waves 160 and 140 respectively.

### Post-implementation gates (waves 170–173)
- **Wave 170:** Per-package `AGENTS.md` sweep — coding agents picking
  the kit see "when to use / when to use something else / common
  mistakes / observability" at every consumer-facing package, in the
  same format. 28 packages covered.
- **Wave 171:** `check-doc-rot` CI gate — every "wave N" reference
  under `docs/` is validated against `git log` subjects so stale
  unanchored deferral claims fail CI rather than ship to
  release notes. Six findings caught on first run; all resolved.
- **Wave 172:** Dashboards + runbooks for waves 157–169. New Grafana
  dashboards for leader election, realtime centrifuge, and gRPC stream
  limits; new `alerts-coordination.yaml` group; new runbooks for
  leader-election callback-drain, centrifuge auth / connect errors,
  gRPC stream cap exhaustion, plus a non-alert OTel tracing
  reference that documents every span the kit emits.
- **Wave 173:** `kit-doctor` rule additions for the wave 157 / 164
  surface — `websocket-any-origin-unsafe` (HIGH, flags
  cross-site WebSocket hijacking risk), `websocket-missing-max-connections`
  (WARNING, goroutine / fd DoS exposure), `centrifuge-missing-jwt-auth`
  (CRITICAL, unauthenticated realtime broadcast). All honour the
  standard `// kit-doctor:allow <rule>` suppression marker.
- **Wave 174:** Five new reference services demonstrate canonical
  kit compositions — `examples/webhook-receiver` (signedrequest →
  idempotency → typed handler), `examples/background-worker`
  (TypedSubscription → circuitbreaker → retry → typed handler),
  `examples/api-gateway` (ratelimit → JWT-auth → downstream
  fan-out with circuitbreaker + retry), `examples/realtime-broadcast`
  (centrifuge.NewNode + WithJWTAuth + WithChannelClassifier with
  an ECDSA self-signing demo so the smoke test stands up without
  an external IDP), and `examples/saga-coordinator` (saga.Run +
  idempotency cache + per-key exclusive section; demonstrates
  roll-forward, automatic compensation, idempotent retry safety,
  and the 422 vs 500 error-routing distinction). All five ship
  as compileable Go modules with smoke tests; all pass
  `kit-doctor -strict warning`. The `examples/README.md` catalog
  is the full set — no remaining recipe-only patterns.
- **Wave 175:** Per-hot-path benchmark regression gate. Six
  benchmarks cover the kit's most-amplified-cost helpers
  (`redact.WrapError`, `promutil.OpaqueLabelValue`,
  `promutil.ValidateStaticLabelValue`,
  `websocket.connLimiter`); the
  `tools/check-bench-regression` runner compares to a checked-in
  baseline (`benchmarks-baseline.txt`) and fails when ns/op
  exceeds the baseline tolerance (default 25%) or allocs/op
  doubles. Exposed via `make bench`, `make check-bench-regression`,
  `make update-bench-baseline`. NOT wired into `make ci` by
  default — benchmark noise across CI runners is too high; the
  gate is intended for local pre-PR + dedicated nightly runners.

## What's deliberately NOT shipped

The kit intentionally does not include these. None are on a roadmap;
consumers that need them should reach for a third-party library or
file an issue with a concrete use case before assuming we will add them.

| Item | Why |
|---|---|
| KMS adapters beyond AWS KMS, Azure Key Vault, Google Cloud KMS, and HashiCorp Vault Transit | The four shipped adapters cover the production estate the kit was designed for; additional provider SDKs are not in scope. |

## Release surface

- New `app/<adapter>` modules (`postgres`, `redis`, `amqp`, `nats`,
  `tracing`, `grpc`) replace the v1 Builder shortcuts and keep heavy SDKs
  out of `app/v2` itself.
- New top-level packages under `data/`, `httpx/`, and `observability/` —
  see the per-package sections above and the API freeze in
  [`docs/release/API_FREEZE_V2.md`](release/API_FREEZE_V2.md) for the
  authoritative inventory.
- Package-relevant docs cover threat modeling, supply-chain policy,
  release notes, migration, dashboards, and runbooks. See
  [`docs/ai/`](ai/) for the AI-agent recipe set and
  [`docs/audit/`](audit/) for the audit ledger.

## Upgrading from v1.x

v2 is a breaking release. v1.x services MUST update import paths
(`/v2` suffix) and migrate Builder calls before upgrading. See
[docs/release/MIGRATION_V2.md](release/MIGRATION_V2.md) for the
full mapping table.

1. **Update import paths.** Every kit import now carries the `/v2`
   module-path suffix (e.g. `github.com/bds421/rho-kit/httpx/v2`).
2. **Migrate removed Builder methods.** Adapter modules now wrap
   what used to be Builder shortcuts:
   - `WithPostgres(cfg)` → `With(postgres.Module(cfg))`
   - `WithRedis(opts, ...)` → `With(redis.Module(opts, ...))`
   - `WithRabbitMQ(url)` → `With(amqp.Module(url))`
   - `WithNATS(cfg)` → `With(nats.Module(cfg))`
   - `WithTracing(cfg)` → `With(tracing.Module(cfg))`
3. To adopt v2 primitives incrementally:
   - Start with `core/tenant` + the `httpx/middleware/tenant`
     middleware in the public mux.
   - Then add `data/cache/tenant.Wrap(...)` around any cache the
     service constructs.
   - Then `MultiTenant` on the Builder.
   - Cost budgets and action log come once tenant ID is reliably
     on ctx.
   - For new services, `kit-new -tenant` emits the Redis cache and
     idempotency wrappers from the start.
4. Run `kit-doctor ./...` against the upgraded service to surface
   any drift from the kit's secure defaults. The v2 doctor includes
   a `tenant-key-prefix` rule for hand-written `tenant:` cache/
   idempotency keys; use `core/tenant.Key` or tenant wrappers
   instead.

## Acknowledgements

This release was assembled with five concurrent subagents working in
parallel git worktrees; each owned one theme end-to-end and reported
back commits + design trade-offs that the orchestrator integrated.
The pattern worked well for genuinely-independent themes; future
releases will use the same model.
