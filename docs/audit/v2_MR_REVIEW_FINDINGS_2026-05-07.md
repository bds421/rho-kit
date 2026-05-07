# rho-kit v2 MR Critical Review Findings

Review date: 2026-05-07

Scope: working tree and MR delta against `origin/main...HEAD`, with comments treated as non-authoritative. I focused on correctness, security boundaries, operational failure modes, API foot-guns, and places where tests or comments mask behavior.

Verification run:

- `make test`: passed across the workspace.
- `make lint`: failed early in `app` with one staticcheck finding, so the rest of the workspace lint did not run.
- `git diff --check origin/main...HEAD`: clean at the time this document was written.
- Current worktree already had unrelated modifications in `app/builder.go`, `infra/sqldb/pgx/pgx_test.go`, and `go.work.sum`; this review did not revert them.

## app

### High: `Builder.Validate` does not run base config validation

Evidence:

- `app/validate.go:73` starts `func (b *Builder) Validate() error`.
- `app/config.go:95` defines `BaseConfig.ValidateBase()`.
- `rg ValidateBase` only finds the definition.

Impact: invalid public server ports and invalid TLS file paths are not rejected by `Builder.Validate()` or `Run()`. `BaseConfig.ValidateBase()` also only checks `Server.Port`, not `Internal.Port`, so `INTERNAL_PORT` can still be invalid even if base validation is wired in later.

Fix direction: call `b.cfg.ValidateBase()` at the start of `Builder.Validate()` and extend `ValidateBase()` to check `Internal.Port`.

### Medium: pgx is excluded from the migration path

Evidence:

- `app/validate.go:90` rejects `migrationsDir` unless `dbDriver` is configured.
- `app/validate.go:84` treats `WithPgx` and GORM drivers as mutually exclusive.

Impact: a service that uses `WithPgx` cannot use the Builder-managed migration hook, even though pgx is a Postgres driver. That pushes pgx services into custom wiring for a common Postgres workflow.

Fix direction: either support migrations for `pgxCfg` or make the API/docs explicit that Builder migrations are GORM-only.

### Low: lint currently fails in app tests

Evidence:

- `make lint` reports `app/v2_modules_test.go:106:8: QF1011`.

Impact: lint stops at the first module, so later lint findings are currently hidden.

Fix direction: apply the staticcheck simplification and rerun full lint.

## core/randstr

### Medium: exported charsets are mutable global slices

Evidence:

- `core/randstr/randstr.go:14` exports `AlphaNum` as `[]rune`.
- `core/randstr/randstr.go:37` uses caller-provided charsets directly.

Impact: any importer can mutate `randstr.AlphaNum[0]` and silently change token generation globally for the process. That can weaken OTPs, invite codes, or generated secrets in unrelated packages.

Fix direction: expose immutable strings or functions returning defensive copies.

## core/tenant and httpx/middleware/tenant

### High: the default HTTP tenant extractor bypasses tenant validation

Evidence:

- `httpx/middleware/tenant/tenant.go:31` defines `HeaderExtractor`.
- `httpx/middleware/tenant/tenant.go:40` casts the raw header with `coretenant.ID(v)`.
- `core/tenant/tenant.go:73` defines `ValidateID`.
- `core/tenant/tenant.go:41` says length is not bounded in `core/tenant`.

Impact: a remote header can put invalid tenant IDs containing `:`, `/`, control bytes, or very large values into context. Downstream tenant-scoped cache, budget, idempotency, log, or metric keys then inherit malformed tenant material.

Fix direction: have `HeaderExtractor` call `coretenant.NewID`, reject invalid IDs with 400, and enforce a boundary length cap.

## data/cache

### High: `MemoryCache` default "64 MiB" cap is not byte-based

Evidence:

- `data/cache/memory_cache.go:122` comments a default 64 MiB cap.
- `data/cache/memory_cache.go:209` uses `cost := int64(1)` unless a cost function is configured.

Impact: the default permits about 67 million entries, not 64 MiB of values. High-cardinality keys can still drive large memory usage unless every caller remembers `WithByteCost()`.

Fix direction: make `WithByteCost()` the default, or change the default docs and sizing to be entry-count based.

### High: `MemoryCache.SetNX` is not atomic in practice

Evidence:

- `data/cache/memory_cache.go:262` implements `SetNX`.
- `data/cache/memory_cache.go:209` writes through Ristretto's buffered `SetWithTTL`.

Impact: `setNXMu` serializes the code path, but the first write may not be visible until Ristretto flushes its buffer. A second `SetNX` can observe the key as missing and also return true.

Fix direction: call `mc.cache.Wait()` before releasing the `SetNX` mutex, or use a separate synchronous map for NX state.

### Medium: tenant cache wrapper drops bulk/CAS semantics

Evidence:

- `data/cache/tenant/tenant.go:49` returns `cache.Cache`, not `cache.BulkCache`.
- `data/cache/cache.go:135` falls back to racy `Exists` plus `Set` when `BulkCache` is absent.

Impact: wrapping a Redis cache with `cache/tenant.Wrap` loses Redis-native `SetNX`, `MGet`, and `MSet`. Cross-process compute-once can become non-atomic after tenant scoping.

Fix direction: implement `BulkCache` on the tenant wrapper when the inner cache implements it.

### Medium: foreground compute strips caller deadlines

Evidence:

- `data/cache/compute.go:267` implements `computeAndStore`.
- `data/cache/compute.go:272` uses `context.WithoutCancel(ctx)`.

Impact: the first caller's cancellation is isolated from shared singleflight waiters, but deadlines are also stripped. An expensive compute can run past request budgets and continue after the initiating request timed out.

Fix direction: preserve the deadline while detaching cancellation, or derive from a configured compute timeout.

### Medium: Redis `MSet` is documented stronger than it is

Evidence:

- `data/cache/rediscache/cache.go:204` says pipelined SET is "atomic from the client's perspective".
- `data/cache/rediscache/cache.go:224` uses a normal Redis pipeline.

Impact: a pipeline can leave partial writes if the connection fails after Redis processes some commands. Callers reading the `BulkCache.MSet` contract may assume all-or-nothing behavior.

Fix direction: document partial-write semantics, or use a Lua script/MULTI path when atomicity is required.

## data/idempotency and httpx/middleware/idempotency

### High: large fingerprinted request bodies are truncated before reaching handlers

Evidence:

- `httpx/middleware/idempotency/idempotency.go:399` reads only `maxFingerprintBodySize+1`.
- `httpx/middleware/idempotency/idempotency.go:260` replaces `r.Body` with the returned buffer.

Impact: with body fingerprinting enabled, a request body larger than 1 MiB is forwarded to the handler truncated to 1 MiB + 1 byte. This can corrupt writes while returning success from the handler's perspective.

Fix direction: either reject oversized fingerprint bodies, or stream the full body to a temp file/hasher and replay the complete body.

### High: all oversized request bodies share the same fingerprint

Evidence:

- `httpx/middleware/idempotency/idempotency.go:411` hashes only the constant string `rho-kit:idempotency:body-too-large`.

Impact: after fixing truncation, different large request bodies with the same idempotency key would compare as equal. That defeats the body-mismatch protection for large payloads.

Fix direction: hash the full body, or reject bodies that cannot be fully fingerprinted.

### Medium: sub-second TTLs can create immediately expired or invalid records

Evidence:

- `httpx/middleware/idempotency/idempotency.go:124` accepts any positive duration.
- `data/idempotency/pgstore/store.go:48` rounds TTL with `int(d.Seconds())`.
- `data/idempotency/redisstore/store.go:258` sends `ttl.Milliseconds()`.

Impact: a positive TTL like `500ms` becomes `0 seconds` in Postgres. A positive sub-millisecond TTL becomes `0` milliseconds in Redis scripts. Direct store callers and middleware users can get non-portable expiry behavior despite `WithTTL` rejecting only `<= 0`.

Fix direction: round up to the backend's precision and reject durations below the supported minimum.

### Medium: post-handler store calls use `context.Background()` without a timeout

Evidence:

- `httpx/middleware/idempotency/idempotency.go:335` unlocks with `context.Background()`.
- `httpx/middleware/idempotency/idempotency.go:369` stores the response with `context.Background()`.

Impact: a hung Redis/Postgres store can pin request goroutines after the handler has already run. It also ignores service shutdown and observability context.

Fix direction: use a bounded background context, e.g. a configurable 1-5 second timeout, and retain trace/log values where useful.

### Low: response capture does not preserve optional HTTP interfaces

Evidence:

- `httpx/middleware/idempotency/idempotency.go:445` embeds only `http.ResponseWriter`.
- `httpx/middleware/idempotency/idempotency.go:484` exposes `Unwrap`, but does not implement `Flusher`, `Hijacker`, `Pusher`, or `ReaderFrom`.

Impact: handlers behind the middleware can lose streaming, hijacking, or optimized `io.Copy` behavior.

Fix direction: use `httpsnoop` or explicitly forward optional interfaces.

## data/budget

### High: in-memory budget admission can overflow

Evidence:

- `data/budget/memory/memory.go:137` checks `used+amount > b.cap`.

Impact: `used+amount` can overflow `int64` and become negative, allowing a very large charge to be admitted and corrupting remaining budget calculations.

Fix direction: compare as `amount > b.cap-used` after validating `used <= cap`, or use checked arithmetic.

### Medium: memory budget never removes cold keys

Evidence:

- `data/budget/memory/memory.go:35` stores all keys in a `sync.Map`.
- `data/budget/memory/memory.go:5` claims the structure self-compacts on access.

Impact: stale keys are reset only when accessed. A high-cardinality stream of one-off keys grows the map forever.

Fix direction: add a sweeper/LRU cap or document that high-cardinality keys require Redis.

### Medium: Redis budget overflow leaves persistent zero-valued keys

Evidence:

- `data/budget/redis/redis.go:70` performs `INCRBY`.
- `data/budget/redis/redis.go:72` performs `DECRBY` on overflow.
- `data/budget/redis/redis.go:78` sets TTL only on the allowed path.

Impact: a first request over cap creates the key, decrements it back to 0, and returns without expiry. Attackers can create persistent garbage keys.

Fix direction: delete the key when the post-decrement value is 0, or set the TTL on both paths.

### Low: Redis-time retry-after uses local time

Evidence:

- `data/budget/redis/redis.go:261` returns `time.Until(nextStart)`.

Impact: when `WithRedisTime` is configured, the period boundary is based on Redis time, but retry-after is computed against the local clock. Clock skew can produce wrong retry hints.

Fix direction: compute retry-after from `nextStart.Sub(now)` using the same time source already fetched.

## data/ratelimit

### High: GCRA can degenerate when `period / burst` rounds to zero

Evidence:

- `data/ratelimit/gcra/gcra.go:61` sets `rate: period / time.Duration(burst)`.
- `data/ratelimit/redis/redis.go:143` computes the same rate.

Impact: if `burst` exceeds `period` in nanoseconds, the emission interval is zero. The limiter then admits requests without meaningful spacing.

Fix direction: reject configurations where `period / burst <= 0`.

### Medium: high-cardinality in-memory limiters grow without bounds

Evidence:

- `data/ratelimit/gcra/gcra.go:35` stores all keys in `tats`.
- `data/ratelimit/tokenbucket/tokenbucket.go:38` stores all keys in `buckets`.
- `data/ratelimit/tokenbucket/tokenbucket.go:27` references an optional sweeper and says to prefer GCRA with built-in LRU bounds, but GCRA has no LRU.

Impact: per-IP or per-tenant limiters can become an unbounded memory sink.

Fix direction: add expiration/LRU, or clearly document that high-cardinality production use needs Redis.

## crypto/envelope

### High: `Rewrap` does not do what its contract says

Evidence:

- `crypto/envelope/envelope.go:198` implements `Rewrap`.
- `crypto/envelope/envelope.go:214` creates `newWrapped`.
- `crypto/envelope/envelope.go:219` creates `newHeader`.
- `crypto/envelope/envelope.go:234` decrypts with `nil` AAD.
- `crypto/envelope/envelope.go:238` discards `newHeader` and calls `Encrypt`.

Impact: `Rewrap` unwraps and wraps the DEK, but ignores the new wrapped DEK. It decrypts plaintext and writes a fresh envelope instead of rewrapping the embedded DEK. Blobs encrypted with non-nil AAD cannot be rewrapped, and the "without touching plaintext" guarantee is false.

Fix direction: either implement a true rewrap format that preserves AAD binding, or rename/remove this method and expose explicit decrypt/encrypt rotation requiring caller AAD.

### Medium: DEKs are not zeroed on several error paths

Evidence:

- `crypto/envelope/envelope.go:184` returns on auth failure before zeroing.
- `crypto/envelope/envelope.go:188` zeroes only after successful decrypt.
- `crypto/envelope/envelope.go:150` zeroes only at the end of successful encrypt.

Impact: key material can remain in memory after wrap, GCM construction, or authentication errors.

Fix direction: `defer` key zeroing immediately after generating/unwrapping the DEK.

## crypto/passhash

### High: `Verify` trusts attacker-controlled PHC cost parameters

Evidence:

- `crypto/passhash/passhash.go:119` parses the stored PHC string.
- `crypto/passhash/passhash.go:129` passes stored memory/iteration/parallelism directly to `argon2.IDKey`.

Impact: a corrupted database row or maliciously supplied hash can request huge Argon2 memory/time and DoS login workers.

Fix direction: impose maximum memory, iterations, parallelism, salt length, and hash length before calling Argon2.

## crypto/paseto

### High: verification accepts non-expiring tokens

Evidence:

- `crypto/paseto/paseto.go:239` sets expiration only when `Claims.ExpiresAt` is non-zero.
- `crypto/paseto/paseto.go:279` reads expiration only when present.
- `crypto/paseto/paseto.go:301` rejects only when `ExpiresAt` is present and expired.

Impact: signed or sealed tokens without `exp` are accepted indefinitely as long as issuer and audience match.

Fix direction: require `exp` by default, with an explicit opt-out for exceptional token classes.

### High: `aud_alt` makes audience validation non-standard and confusing

Evidence:

- `crypto/paseto/paseto.go:227` stores extra audiences in `aud_alt`.
- `crypto/paseto/paseto.go:272` appends `aud_alt` into accepted audiences.

Impact: a token whose canonical `aud` is service A can be accepted by service B if custom `aud_alt` contains B. That weakens the usual single-audience confused-deputy boundary and may not be understood by external tooling.

Fix direction: validate only the standard `aud` claim, or make multi-audience tokens an explicit verifier option.

### Medium: custom claims can override reserved claims

Evidence:

- `crypto/paseto/paseto.go:245` writes `Claims.Custom` after subject, issuer, audience, and expiry.

Impact: callers can put `iss`, `aud`, `exp`, `nbf`, or `aud_alt` into `Custom` and alter reserved-claim semantics after the typed fields were set.

Fix direction: reject reserved claim names in `Custom`.

### Low: `Provider.Stop` is not concurrency-safe

Evidence:

- `crypto/paseto/provider.go:110` implements `Stop`.
- `crypto/paseto/provider.go:115` closes `p.stop` without `sync.Once`.

Impact: sequential double stop is covered by tests, but two concurrent callers can both enter the default branch and panic on double close.

Fix direction: use `sync.Once`.

## data/actionlog

### High: row signatures are not a tamper-evident log

Evidence:

- `data/actionlog/actionlog.go:415` signs one entry at a time.
- `data/actionlog/actionlog.go:126` allows listing by query without chain verification.

Impact: changing a row is detected, but deleting rows, reordering rows, or truncating a tenant's history is not detected. The package is a signed row store, not an append-only tamper-evident log.

Fix direction: add sequence numbers and hash chaining per tenant, and enforce append-only behavior in durable stores.

### Medium: zero-value queries can leak cross-tenant action history

Evidence:

- `data/actionlog/actionlog.go:126` says zero `Query` returns every entry.
- `data/actionlog/actionlog.go:130` only "strongly recommends" `TenantID`.

Impact: any admin or API layer that exposes `List(Query{})` leaks all tenants' audit entries.

Fix direction: require `TenantID` unless the caller opts into a clearly named cross-tenant query.

### Medium: `StaticSecrets.Resolve` exposes mutable key material

Evidence:

- `data/actionlog/actionlog.go:228` copies keys at construction.
- `data/actionlog/actionlog.go:248` returns the stored slice directly.

Impact: a caller can mutate the returned secret slice and break or forge subsequent verification/signing behavior inside the process.

Fix direction: return a defensive copy from `Resolve`.

## data/approval and httpx/middleware/approval

### High: direct store callers can create approvals that never expire

Evidence:

- `data/approval/approval.go:129` validates required fields but not `ExpiresAt`.
- `data/approval/memory/memory.go:129` expires only if `!r.ExpiresAt.IsZero()`.
- `data/approval/postgres/store.go:171` has the same zero-time bypass.

Impact: middleware-created approvals have expiry, but direct store users can create permanent pending approvals.

Fix direction: require non-zero future `ExpiresAt` in store `Create`.

### Medium: decisions can have an empty approver

Evidence:

- `data/approval/memory/memory.go:116` accepts `decidedBy`.
- `data/approval/memory/memory.go:144` stores it without validation.
- `data/approval/postgres/store.go:185` does the same.

Impact: destructive approvals can be approved or rejected with no accountable approver.

Fix direction: reject empty `decidedBy`.

### Medium: middleware defaults actor to `anonymous`

Evidence:

- `httpx/middleware/approval/approval.go:152` sets the default actor extractor to `anonymous`.
- `httpx/middleware/approval/approval.go:201` stores that actor.

Impact: destructive approval requests are forensically weak unless every service remembers to wire auth-derived actors.

Fix direction: fail closed by default or require `WithActorExtractor` for production constructors.

### Low: `WithExecutor` is stored but never used by the middleware

Evidence:

- `httpx/middleware/approval/approval.go:49` has `executor`.
- `httpx/middleware/approval/approval.go:140` sets it.
- `httpx/middleware/approval/approval.go:169` creates middleware that never calls or exposes it.

Impact: API users can reasonably believe the option wires execution, but it is dead configuration.

Fix direction: remove the option or provide a first-class approver/executor handler that consumes it.

## security/jwtutil

### High: JWT verification does not require `exp`

Evidence:

- `security/jwtutil/jwtutil.go:90` enables jwx validation.
- `security/jwtutil/jwtutil.go:118` copies expiration only when present.
- There is no explicit missing-exp check after parse.

Impact: unless the underlying library requires `exp` implicitly, tokens without expiration can be accepted. The code and tests only cover expired tokens, not missing-exp tokens.

Fix direction: add an explicit `tok.Expiration()` presence check and test it.

### Medium: default JWKS HTTP client drops standard transport defaults

Evidence:

- `security/jwtutil/jwtutil.go:329` constructs `defaultHTTPClient`.
- `security/jwtutil/jwtutil.go:339` installs a bare `http.Transport` with only `MaxResponseHeaderBytes`.

Impact: this loses standard proxy handling, dialer timeouts, TLS handshake timeout, idle connection defaults, and other production HTTP settings. Standalone jwtutil users get a weaker client than kit HTTP clients.

Fix direction: clone `http.DefaultTransport` or use the kit HTTP client builder, then adjust header limits.

## security/netutil

### High: IPv6 unspecified address `::` is not rejected by SSRF filtering

Evidence:

- `security/netutil/ssrf.go:127` implements `IsPrivateIP`.
- `security/netutil/ssrf.go:133` checks loopback/link-local/multicast/private, but not `ip.IsUnspecified()`.
- `security/netutil/ssrf.go:104` custom IPv6 ranges do not include `::/128`.

Impact: a hostname resolving to `::` can pass validation as non-private/reserved. Dialing `::` targets the local host wildcard semantics, defeating the SSRF boundary.

Fix direction: reject `ip.IsUnspecified()` for IPv6 as well as IPv4.

## httpx/sign and httpx/middleware/signedrequest

### High: signed-request HMAC accepts weak secrets

Evidence:

- `httpx/sign/sign.go:76` rejects only empty secrets.
- `httpx/middleware/signedrequest/signedrequest.go:185` rejects only empty resolved secrets.
- `crypto/signing/signing.go:22` uses a 32-byte minimum for HMAC-SHA256 elsewhere.

Impact: an operator can deploy a one-byte HMAC secret and the signing middleware will accept it.

Fix direction: enforce the same 32-byte minimum on both signer and verifier.

### Low: signing verifier options accept invalid timing/body caps

Evidence:

- `httpx/middleware/signedrequest/signedrequest.go:87` accepts any max clock skew.
- `httpx/middleware/signedrequest/signedrequest.go:105` accepts any body max size.
- `httpx/sign/sign.go:52` accepts any body max size.

Impact: negative values can make all requests fail or make body reads behave unexpectedly.

Fix direction: panic or ignore non-positive values consistently.

## httpx/mcp

### High: strict audit does not fail closed on append failure

Evidence:

- `httpx/mcp/actionlog.go:45` prechecks only tenant presence.
- `httpx/mcp/actionlog.go:157` logs append failures and continues.
- `httpx/mcp/server.go:215` runs the tool before `recordActionLog`.

Impact: strict mode prevents unaffiliated execution but does not guarantee that every executed tool call produced a signed entry. If the action log store is down, the tool still succeeds.

Fix direction: in strict mode, append synchronously and fail the response when append fails, or rename the mode to reflect best-effort auditing.

### High: async audit can create unbounded stuck goroutines

Evidence:

- `httpx/mcp/mcp.go:278` enables async audit.
- `httpx/mcp/actionlog.go:129` uses `context.WithoutCancel(ctx)`.
- `httpx/mcp/actionlog.go:138` starts one goroutine per call.

Impact: a slow or hung audit store under load can accumulate unbounded goroutines with no deadline.

Fix direction: use a bounded worker queue and a timeout-scoped context.

### Medium: params decoding accepts trailing JSON

Evidence:

- `httpx/mcp/server.go:254` builds dispatch.
- `httpx/mcp/server.go:260` calls `dec.Decode(&in)` once and does not verify EOF.

Impact: malformed params like `{"x":1} {"y":2}` can be accepted. That is not valid JSON-RPC params.

Fix direction: decode a second token and require `io.EOF`.

### Medium: schema generation panics on anonymous pointer embeds

Evidence:

- `httpx/mcp/schema.go:181` handles anonymous fields.
- `httpx/mcp/schema.go:182` calls `structSchema(f.Type, visiting)` directly.

Impact: for `struct { *Embedded }`, `f.Type.Kind()` is pointer and `structSchema` calls `NumField` on a pointer type, which panics during tool registration.

Fix direction: call `schemaFor(f.Type, visiting)` for embedded fields or unwrap pointers before `structSchema`.

### Medium: `tools/call` result is not MCP-shaped

Evidence:

- `httpx/mcp/server.go:122` handles `tools/call`.
- `httpx/mcp/server.go:232` notes MCP expects `{content: [...]}` but returns raw tool output.

Impact: generic MCP clients expecting the standard `tools/call` response shape may fail or ignore results.

Fix direction: either implement MCP content wrapping for `tools/call` or advertise this as a JSON-RPC tool server rather than MCP-compatible.

### Low: `truncateReason` can split UTF-8

Evidence:

- `httpx/mcp/mcp.go:502` implements `truncateReason`.

Impact: when the cap falls inside a multibyte rune, the function can return invalid UTF-8 because it slices after the leading byte.

Fix direction: use `utf8.ValidString`/`utf8.DecodeLastRuneInString` or range over byte indexes.

## infra/messaging

### High: persisted buffered publisher acknowledges messages even when persistence failed

Evidence:

- `infra/messaging/buffered_publisher.go:242` buffers then calls `saveLocked`.
- `infra/messaging/buffered_publisher.go:253` returns nil.
- `infra/messaging/buffered_publisher.go:441` logs save failures only.

Impact: after a broker outage, `Publish` can return nil although the message was only in memory and failed to persist to the state file. A process crash loses the message despite the caller seeing success.

Fix direction: make persistence failure part of the `Publish` error path when a state file is configured, or add an explicit lossy mode.

### Medium: AMQP consumer does not recover handler panics

Evidence:

- `infra/messaging/amqpbackend/consumer.go:253` enters `handleDelivery`.
- `infra/messaging/amqpbackend/consumer.go:270` calls the handler directly.

Impact: a handler panic can kill the consumer goroutine and leave the AMQP delivery unacked until connection/channel cleanup. NATS has explicit panic recovery; AMQP should match that behavior.

Fix direction: recover in `handleDelivery` and route through retry/dead-letter handling.

### Medium: NATS publish ack wait config is unused

Evidence:

- `infra/messaging/natsbackend/natsbackend.go:55` defines `PublishAckWait`.
- `infra/messaging/natsbackend/natsbackend.go:173` hard-codes `wait: 5 * time.Second`.

Impact: operators cannot tune publish ack wait through `Config` even though the field claims they can.

Fix direction: pass the connection config into `NewPublisher` or expose a publisher option.

### Medium: NATS subject mapping is not reversible for dotted exchange names

Evidence:

- `infra/messaging/natsbackend/natsbackend.go:366` composes `exchange + "." + routingKey`.
- `infra/messaging/natsbackend/natsbackend.go:373` splits at the first dot.

Impact: publishing with exchange `orders.v1` and routing key `created` is consumed as exchange `orders`, routing key `v1.created`. That breaks backend-agnostic delivery semantics.

Fix direction: encode exchange/routing key with an unambiguous delimiter or store them in headers.

### Low: `Connect(ctx, ...)` ignores its context

Evidence:

- `infra/messaging/natsbackend/natsbackend.go:78` accepts `ctx`.

Impact: callers expect dial cancellation, but `nats.Connect` is called without context-derived cancellation.

Fix direction: use NATS timeout options derived from context or remove the context parameter.

## infra/outbox

### High: stale recovery can duplicate long-running publishes

Evidence:

- `infra/outbox/relay.go:16` sets `defaultStaleDuration` to 5 minutes.
- `infra/outbox/relay.go:358` resets stale processing rows.
- `infra/outbox/gormstore/gormstore.go:280` uses `updated_at < cutoff`.

Impact: if a publish legitimately takes more than 5 minutes, another relay can reset and republish the same row while the first publish is still in flight.

Fix direction: heartbeat processing rows, make stale duration configurable per relay, or bound publisher timeouts below stale duration.

### Medium: publish status updates do not check whether a row was actually changed

Evidence:

- `infra/outbox/gormstore/gormstore.go:204` implements `MarkPublished`.
- `infra/outbox/gormstore/gormstore.go:219` implements `MarkFailed`.

Impact: wrong IDs, concurrent stale resets, or missing rows are treated as success. The relay can log success even though no durable state changed.

Fix direction: check `RowsAffected` and return a typed not-found/stale-state error when it is zero.

## infra/sqldb

### High: MySQL `Config.Options["tls"]` is recognized but ignored by the driver

Evidence:

- `infra/sqldb/config.go:58` treats `tls=true` as enabled.
- `infra/sqldb/config.go:150` parses MySQL URLs but does not preserve query options.
- `infra/sqldb/gormdb/gormmysql/driver.go:83` builds the DSN using only charset and loc unless `clientTLS` is non-nil.

Impact: validation/telemetry can say MySQL TLS is enabled while the actual DSN omits `tls=...` and connects without it.

Fix direction: preserve MySQL query options and include validated `tls` in `buildMySQLDSN`.

### Medium: MySQL TLS registry references leak on failed opens

Evidence:

- `infra/sqldb/gormdb/gormmysql/driver.go:42` registers TLS config.
- `infra/sqldb/gormdb/gormmysql/mysql.go:31` increments a registry refcount.
- `infra/sqldb/gormdb/gormmysql/mysql.go:61` requires manual `ReleaseTLS`.

Impact: if `gorm.Open`, pool setup, or ping fails after TLS registration, the refcount is never decremented. Retry loops and rotating TLS configs can leak global driver entries.

Fix direction: release the registered config on every error after registration, and consider tying release to DB close wrappers.

### Low: pgx `Copy` cannot address schema-qualified tables

Evidence:

- `infra/sqldb/pgx/pgx.go:159` implements `Copy`.
- `infra/sqldb/pgx/pgx.go:170` uses `pgx.Identifier{table}`.

Impact: `Copy(ctx, "public.users", ...)` targets the single quoted identifier `"public.users"` rather than `"public"."users"`.

Fix direction: accept `pgx.Identifier` or split/validate schema and table components.

## infra/storage

### High: encrypted storage does not bind ciphertext to key or metadata

Evidence:

- `infra/storage/encryption/encryption.go:156` calls `encrypt.SealBytes(gcm, plaintext)`.
- `infra/storage/encryption/encryption.go:206` calls `encrypt.OpenBytes(gcm, ciphertext)`.

Impact: ciphertext copied from one storage key to another decrypts successfully. A compromised backend or confused copy path can substitute objects across keys/tenants without detection.

Fix direction: use AEAD AAD that includes the storage key and relevant metadata.

### Medium: encrypted storage leaves plaintext/ciphertext buffers uncleared on some errors

Evidence:

- `infra/storage/encryption/encryption.go:143` reads plaintext.
- `infra/storage/encryption/encryption.go:147` returns on oversize before zeroing.
- `infra/storage/encryption/encryption.go:183` reads ciphertext.
- `infra/storage/encryption/encryption.go:206` decrypts after several possible early returns.

Impact: the package tries to scrub sensitive buffers, but oversized plaintext and ciphertext error paths can leave data in memory.

Fix direction: defer zeroing immediately after buffers are allocated/read.

### Medium: `uploadsec.MaxImageDimensions` buffers the entire body

Evidence:

- `infra/storage/storagehttp/uploadsec/uploadsec.go:168` documents header-only dimension checks.
- `infra/storage/storagehttp/uploadsec/uploadsec.go:187` calls `io.ReadAll(body)`.

Impact: a large uploaded image is fully buffered in memory before `image.DecodeConfig`. The comment's decompression-bomb claim is not the same as a memory-use cap.

Fix direction: wrap the body with a bounded reader, or use the existing streaming `storage.ImageDimensions` validator pattern.

### Medium: `ParseAndStore` has no default cap for the actual file part

Evidence:

- `infra/storage/storagehttp/upload.go:54` caps skipped non-file parts.
- `infra/storage/storagehttp/upload.go:143` streams the selected file part to storage.
- `infra/storage/storagehttp/upload.go:157` only applies validators if configured.

Impact: unless callers add `storage.MaxFileSize` or an upstream `MaxBodySize`, a file upload can stream indefinitely into disk/object storage.

Fix direction: require an explicit max file size in `UploadOptions`, or default to a conservative cap.

### Low: encrypted storage constructor does not fail fast on nil dependencies

Evidence:

- `infra/storage/encryption/encryption.go:95` constructs `EncryptedStorage` without nil checks.

Impact: nil backend/key provider panics later on first request instead of at startup.

Fix direction: panic in `New` when `backend` or `keys` is nil.

## runtime/eventbus

### Medium: generic subscribe calls `EventName` on the zero value

Evidence:

- `runtime/eventbus/eventbus.go:216` declares `var zero E`.
- `runtime/eventbus/eventbus.go:217` calls `zero.EventName()`.

Impact: pointer event types or implementations that read receiver fields can panic or register under the wrong event name during subscription.

Fix direction: require an explicit event name at subscription time, or restrict/document event types to value-safe zero receivers and test pointer cases.

### Low: invalid `OnFullPolicy` values silently behave like drop

Evidence:

- `runtime/eventbus/eventbus.go:20` defines `OnFullPolicy` as `int`.
- `runtime/eventbus/eventbus.go:111` stores any value.
- `runtime/eventbus/pool.go:79` handles only known values.

Impact: bad configuration fails open to drop-like behavior instead of failing fast.

Fix direction: validate `WithOnFull`.

### Low: `Bus.Start` does not stop workers if used outside the lifecycle runner

Evidence:

- `runtime/eventbus/eventbus.go:428` starts the worker pool.
- `runtime/eventbus/pool.go:163` returns when context is canceled.
- `runtime/eventbus/pool.go:174` closes workers only from `Stop`.

Impact: the lifecycle runner calls `Stop`, but direct `Start(ctx)` users can cancel `ctx` and leave workers alive.

Fix direction: either call `stop` from `Start` on context cancellation or document that `Stop` is mandatory.

## runtime/batchworker

### Medium: `Stop` ignores shutdown deadline errors

Evidence:

- `runtime/batchworker/batchworker.go:157` implements `Stop`.
- `runtime/batchworker/batchworker.go:166` handles `ctx.Done()` without returning `ctx.Err()`.

Impact: lifecycle shutdown can time out while reporting success for this component.

Fix direction: return `ctx.Err()` when the stop context expires.

## observability

### Medium: RED HTTP middleware breaks optional response writer interfaces

Evidence:

- `observability/redmetrics/redmetrics.go:195` defines `statusRecorder`.
- `observability/redmetrics/redmetrics.go:205` and `:213` forward only `WriteHeader` and `Write`.

Impact: handlers can lose `http.Flusher`, `http.Hijacker`, `http.Pusher`, or `io.ReaderFrom`, affecting streaming, websockets, HTTP/2 push, and copy performance.

Fix direction: use `httpsnoop` or forward optional interfaces.

### Medium: `pprof.IsPprofPath` matches too broadly

Evidence:

- `observability/pprof/pprof.go:76` uses `strings.HasPrefix(p, "/debug/pprof")`.

Impact: middleware using this helper to bypass auth for pprof would also bypass auth for `/debug/pprofevil`.

Fix direction: require exact `/debug/pprof` or prefix `/debug/pprof/`.

### Low: custom histogram buckets are not validated

Evidence:

- `observability/redmetrics/redmetrics.go:82` accepts arbitrary HTTP buckets.
- `observability/redmetrics/redmetrics.go:248` accepts arbitrary batch buckets.
- `observability/redmetrics/redmetrics.go:116` and `:277` pass buckets into Prometheus collectors.

Impact: empty, unsorted, duplicate, or non-positive bucket values can panic or register invalid collectors during startup.

Fix direction: validate bucket slices in the option setters or constructors.

## grpcx

### Medium: service-to-service mTLS identity is pinned to certificate CN

Evidence:

- `grpcx/interceptor/auth.go:322` takes `allowedCNs`.
- `grpcx/interceptor/auth.go:401` logs `Subject.CommonName`.
- `grpcx/interceptor/auth.go:436` authorizes by `Subject.CommonName`.

Impact: Common Name is deprecated as an identity source. Modern certificates should use SAN DNS/URI identities; CN-only auth can miss intended identity constraints and makes SPIFFE-style service IDs awkward.

Fix direction: authorize against SAN URI/DNS names, with CN as an explicit legacy option.

### Low: comments around recovery/deadline order are contradictory

Evidence:

- `grpcx/server.go:185` says deadline goes before recovery.
- `grpcx/server.go:188` prepends deadline, then `grpcx/server.go:194` prepends recovery, making recovery outermost.

Impact: current behavior is probably correct, but the comment is misleading for future interceptor-order changes.

Fix direction: update the comment to describe actual chain order.

## cmd/kit-new

### Medium: service name can escape the output directory

Evidence:

- `cmd/kit-new/scaffold.go:34` uses `cmd/{{.ServiceName}}/main.go`.
- `cmd/kit-new/scaffold.go:76` joins the rendered path to `outDir`.
- `cmd/kit-new/scaffold.go:80` creates the file.

Impact: a service name like `../../outside` can render a destination path that writes outside `outDir`.

Fix direction: validate `ServiceName` as a safe path segment and verify every final path remains under the cleaned output directory.

## cmd/kit-doctor

### Medium: fluent-chain rules can produce broad false negatives

Evidence:

- `cmd/kit-doctor/rules/helpers.go:55` implements `chainHas`.
- `cmd/kit-doctor/rules/helpers.go:64` scans the entire file.
- `cmd/kit-doctor/rules/jwt_missing_claims.go:30` uses that helper for issuer/audience checks.

Impact: if one Builder in a file has `WithJWTAudience`, another Builder in the same file without audience can be missed. The tool is advisory, but this weakens its value for migration reviews.

Fix direction: build parent links or inspect only the actual call chain expression.

## cmd/kit-bench-gate

### Low: regressions from a zero baseline are invisible

Evidence:

- `cmd/kit-bench-gate/compare.go:86` builds diffs.
- `cmd/kit-bench-gate/compare.go:90` computes percentage only when baseline is greater than zero.

Impact: `allocs/op` moving from 0 to 1, or bytes moving from 0 to a positive value, is not marked as a regression because `PctChange` remains 0.

Fix direction: treat zero-baseline positive current values as infinite or explicit regressions for tracked metrics.

