# Review backlog status
## Policy
**Fix-first.** Docs/typos/naming/consistency/perf/tradeoffs are fixed in code or docs,
not refuted as "working as designed." Breaking (v3) API changes need explicit user go-ahead.

The previous mass-refute approach was wrong and has been reversed: only findings with
audited **FIXED** evidence (code + tests) are removed from the review trackers. All other
findings remain **OPEN**.

## Cleanup (LOW batch 2026-07-21 — crypto/oauth/sftp/clamav/observability/idempotency)

- Cleared **17** tracker LOWs (code + docs/tests; no refutes; MEDIUMs left; god-file/v3 skip list honored).
- Focus packages: `crypto/{paseto,secretcrypt}`, `security/jwtutil`, `auth/oauth2`, `authz`,
  `infra/storage/{sftpbackend,localbackend,storagehttp/uploadsec/clamav}`,
  `observability/{auditlog,slo}`, `infra/leaderelection`, `data/{actionlog,idempotency/redisstore}`.

### Fixes landed (code + tests where behavior changed)
- **paseto**: `WithClock` for `WithDefaultLifetime` / `buildToken` (no bare `time.Now`).
- **jwtutil**: precompute `stringClaimNames` once at Provider construction for `populateStringClaims`.
- **secretcrypt**: bounded per-identity AEAD cache (HKDF/GCM rebuild on miss only); cleared on Close.
- **oauth2 memory stores**: `RWMutex` + lazy delete + budgeted sweep (no full O(n) Put sweep).
- **authz scopes**: exported `Registry` / `NewRegistry` / `DefaultRegistry` / `ResetScopes`.
- **sftpbackend**: `NewContext`, context-preserving `sftpRemoteError`, lazy `Healthy` connect attempt.
- **auditlog**: document `LastHMAC` as required SPI; `List` trusts Store ownership (no double clone).
- **slo**: `DependencyCheck` observes ctx before Evaluate; document uncancellable Gather.
- **localbackend**: open-then-Lstat/fstat for symlink-object refusal (closes Lstat-then-Open TOCTOU).
- **clamav**: non-loopback TCP requires `WithDialer` or `WithAllowInsecurePlaintext`; scanTimeout bounds body stream.
- **leaderelection**: document divergent callback-drain metric labels across backends.
- **actionlog**: Logger Get/List no longer re-clone (Store contract); test memStore clones.
- **idempotency/redisstore**: Get/Set/Unlock folded into single-RTT Lua scripts.

## Cleanup (LOW batch 2026-07-21 — httpx/websocket/centrifuge/data-core-a)

- Cleared **~51** tracker LOWs from target reviews (code + tests; no refutes; MEDIUMs left).
- Focus packages: `httpx/{budget,logger,webhook,mcp}`, `httpx/middleware/{timeout,signedrequest,stack,csrf,ratelimit,recover,requestid,correlationid}`,
  `httpx/websocket`, `realtime/centrifuge`, `data/{actionlog,approval,cache}`, `observability/logging`.

### Fixes landed (code + tests where behavior changed)
- **httpx/budget**: `WithMaxActual` clamps inflated actual-cost headers during reconcile.
- **logging / httpx.Logger**: `FromContextOK` presence check (no identity-vs-Default race).
- **httpx/mcp**: thread audit identity through context; `WithServerInfo`; AddTool panic rollback;
  validation errors reflect field names only (not free-form messages); default Implementation version `v2`.
- **httpx/webhook**: permanent transport errors (redirect blocked, TLS/x509, open circuit);
  explicit 3xx branch; default host SSRF check via `ResolveAndValidate` + `AllowPrivateDestinations`.
- **timeout**: original panic stack capture + recover prefers `StackTrace()`; client cancel ≠ TIMEOUT 503;
  drop Content-Length when response buffer truncated.
- **signedrequest**: pre-sort/dedupe required headers; `WithSpoolDir`; redis `WithCallTimeout` canonical /
  `WithNonceTimeout` deprecated alias.
- **stack**: `WithMetricsOptions`, `WithMaxBody` + docs that body is uncapped by default.
- **csrf**: bare double-submit startup warn; issued-at + 24h TTL on legacy signed tokens.
- **ratelimit**: `WithMaxPerShard` / `WithKeyedMaxPerShard`.
- **requestid/correlationid**: document caller-controlled ID forensic risk.
- **websocket**: ReadJSON/WriteJSON via Message APIs; Ping records close on non-deadline errors + writeTimeout;
  origin pattern fail-fast; teardown uses `Conn.Close`; WithMetrics godoc fixed.
- **centrifuge**: `jwtutil.NewProvider` quick-start; empty subject rejected; Token-only bearer extract;
  anonymous path requires `WithAnonymousConnectionsUnsafe`.
- **actionlog**: metadata string length cap; sortedAny depth guard; Sign/Verify run validMetadata;
  cursor MAC domain separation; Append skips re-validating metadata under tenant lock.
- **approval**: payload must be JSON; far-future CreatedAt rejected; dead `validate()` removed.
- **cache**: `WithStaleTTL` construction max; SetNX shards + claim drop on eviction; TypedCache Codec;
  key prefixes must end with `:`.

## Cleanup (LOW batch 2026-07-20 night — lifecycle/app/data/httpx/debughttp)

- Cleared **27** tracker LOWs this session (code + tests; no refutes).
- Focus packages: `runtime/{lifecycle,eventbus}`, `app` (+ postgres/nats/grpc/httpclient),
  `data/{idempotency/pgstore,stream/redisstream,cache/rediscache,lock/redislock,budget/redis}`,
  `infra/{outbox/postgres,sqldb,redis,messaging/amqpbackend/debughttp}`,
  `httpx/{sign,openapi,pagination}`, `observability/health`, `grpcx/interceptor`.

## Cleanup (LOW batch 2026-07-20 late — streams/queues/pg/redis/grpcx/httpx)

- Cleared **48** tracker LOWs prior session (code + tests; no refutes).
- Focus packages: `data/stream/redisstream`, `data/queue/redisqueue`,
  `data/idempotency/{pgstore,redisstore}`, `data/{actionlog,apikey,cron}/postgres`,
  `data/{budget/redis,cache/rediscache,lock/redislock}`, `grpcx` (+ client/interceptor),
  `httpx` typed handlers.

## Cleanup (this pass)

- Remaining findings (`review-01` … `review-26`): **20**
  - CRITICAL **0**
  - HIGH **0**
  - MEDIUM **5**
  - LOW **15**

## Remaining counts per review file

| File | Crit | High | Med | Low | Total |
|---|---:|---:|---:|---:|---:|
| `review-01-core-io.md` | 0 | 0 | 0 | 0 | 0 |
| `review-02-runtime-resilience.md` | 0 | 0 | 0 | 0 | 0 |
| `review-03-app-wiring.md` | 0 | 0 | 0 | 0 | 0 |
| `review-04-crypto.md` | 0 | 0 | 0 | 2 | 2 |
| `review-05-security.md` | 0 | 0 | 0 | 2 | 2 |
| `review-06-auth-authz.md` | 0 | 0 | 0 | 0 | 0 |
| `review-07-httpx-core.md` | 0 | 0 | 0 | 1 | 1 |
| `review-08-httpx-middleware.md` | 0 | 0 | 0 | 2 | 2 |
| `review-09-websocket-realtime.md` | 0 | 0 | 1 | 0 | 1 |
| `review-10-grpcx.md` | 0 | 0 | 0 | 0 | 0 |
| `review-11-data-core-a.md` | 0 | 0 | 1 | 0 | 1 |
| `review-12-data-core-b.md` | 0 | 0 | 1 | 2 | 3 |
| `review-13-data-pg-stores.md` | 0 | 0 | 0 | 0 | 0 |
| `review-14-data-redis-stores.md` | 0 | 0 | 0 | 0 | 0 |
| `review-15-queues-streams.md` | 0 | 0 | 0 | 0 | 0 |
| `review-16-messaging-core.md` | 0 | 0 | 0 | 0 | 0 |
| `review-17-messaging-backends.md` | 0 | 0 | 0 | 0 | 0 |
| `review-18-storage-core.md` | 0 | 0 | 1 | 3 | 4 |
| `review-19-storage-backends.md` | 0 | 0 | 1 | 2 | 3 |
| `review-20-sqldb-outbox.md` | 0 | 0 | 0 | 0 | 0 |
| `review-21-redis-leader.md` | 0 | 0 | 0 | 0 | 0 |
| `review-22-secrets.md` | 0 | 0 | 0 | 0 | 0 |
| `review-23-observability-flags.md` | 0 | 0 | 0 | 0 | 0 |
| `review-24-cmd-clis.md` | 0 | 0 | 0 | 1 | 1 |
| `review-25-examples.md` | 0 | 0 | 0 | 0 | 0 |
| `review-26-testing-kits.md` | 0 | 0 | 0 | 0 | 0 |
| **TOTAL** | **0** | **0** | **5** | **15** | **20** |

## Notes

- Before this session: **37** (0/0/5/32).
- After this session: **20** (0/0/5/15). Cleared 17 easy/medium LOWs.
- Keep OPEN MEDIUM (v3 / larger): review-09 heartbeat defaults; review-11 TenantStore non-atomic fallback;
  review-12 forgeable tenant key namespace; review-18 optional capability APIs; review-19 SFTP reconnect lease.
- Intentionally skipped (god-file / v3 / large):
  - review-04: KEK metrics parity; Provider/SigningProvider DRY
  - review-05: jwtutil.go god-file; KeySet mutable policy fields
  - review-07: mcp.go god-file split
  - review-08: idempotency.go god-file; Limiter/KeyedLimiter dedup
  - review-12: Consumer.Consume error return (queue + stream)
  - review-18: hooks/capability combinator god-files
  - review-19: Listing parity; Put temp files
  - review-24: kit-doctor package globals
- Helper script: `tools/_cleanup_fixed_reviews.py` (matchers can be extended with this batch's titles).
