# Review backlog status
## Policy
**Fix-first.** Docs/typos/naming/consistency/perf/tradeoffs are fixed in code or docs,
not refuted as "working as designed." Breaking (v3) API changes need explicit user go-ahead.

The previous mass-refute approach was wrong and has been reversed: only findings with
audited **FIXED** evidence (code + tests) are removed from the review trackers. All other
findings remain **OPEN**.

## Cleanup (LOW batch 2026-07-20 night — lifecycle/app/data/httpx/debughttp)

- Cleared **27** tracker LOWs this session (code + tests; no refutes).
- Focus packages: `runtime/{lifecycle,eventbus}`, `app` (+ postgres/nats/grpc/httpclient),
  `data/{idempotency/pgstore,stream/redisstream,cache/rediscache,lock/redislock,budget/redis}`,
  `infra/{outbox/postgres,sqldb,redis,messaging/amqpbackend/debughttp}`,
  `httpx/{sign,openapi,pagination}`, `observability/health`, `grpcx/interceptor`.

### Fixes landed (code + tests where behavior changed)
- **lifecycle**: second-signal force-quit abandons Start-blocked components after stopTimeout
  (no more hang until SIGKILL despite "forcing immediate shutdown").
- **eventbus**: shared `publishAny`; non-generic `Bus.Publish(ctx, Event)` (context-first);
  free generic Publish documents v3 reorder intent.
- **pgstore (idempotency)**: atomic TryLock via ON CONFLICT CASE + RETURNING (no fingerprint
  TOCTOU SELECT window).
- **redisstream**: `purgeStaleConsumers` in claim loop (empty PEL + idle > 10m).
- **budget/redis**: default prefix is config-scoped (`budget:c{cap}:p{periodNs}:`).
- **rediscache**: default key prefix is `name:`; optional `WithKeyPrefix`.
- **redislock/redlock**: default key prefix `lock:`; optional `WithKeyPrefix`.
- **debughttp**: `ConsumeHandler`/`PublishHandler` always apply `Guard` (env+auth required);
  `Unguarded*` for tests; docs updated.
- **outbox ResetPending**: batched VALUES update; always forgetClaim before Exec.
- **sqldb ParseDSN**: copies all non-empty query params into Options (reject repeats).
- **app**: reloading TLS source closed on early RunContext failure; postgres.Stop bounds
  pool.Close by ctx; nats HealthChecks; grpc plaintext Warn; `WithHTTPClientTimeout`;
  unique TracingProvider enforcement.
- **health**: non-critical Connecting folds to overall Connecting (503), not Degraded.
- **httpx**: sign zeroes HMAC secret before RoundTrip; openapi sanitizes/uniques operationIds;
  cursor Encode refuses empty key inside `Use`.
- **grpcx**: `AdoptIncomingIdentity` adopts subject/actor/kind under trusted-S2S.
- **redis**: READONLY already fail-soft (verified); reconnecting flag clears on timeout/close.
- **examples/nats tests**: fingerprint already wired; NATS nack test uses readiness probe.

## Cleanup (LOW batch 2026-07-20 late — streams/queues/pg/redis/grpcx/httpx)

- Cleared **48** tracker LOWs prior session (code + tests; no refutes).
- Focus packages: `data/stream/redisstream`, `data/queue/redisqueue`,
  `data/idempotency/{pgstore,redisstore}`, `data/{actionlog,apikey,cron}/postgres`,
  `data/{budget/redis,cache/rediscache,lock/redislock}`, `grpcx` (+ client/interceptor),
  `httpx` typed handlers.

## Cleanup (this pass)

- Remaining findings (`review-01` … `review-26`): **160**
  - CRITICAL **0**
  - HIGH **0**
  - MEDIUM **5**
  - LOW **155**

## Remaining counts per review file

| File | Crit | High | Med | Low | Total |
|---|---:|---:|---:|---:|---:|
| `review-01-core-io.md` | 0 | 0 | 0 | 0 | 0 |
| `review-02-runtime-resilience.md` | 0 | 0 | 0 | 0 | 0 |
| `review-03-app-wiring.md` | 0 | 0 | 0 | 4 | 4 |
| `review-04-crypto.md` | 0 | 0 | 0 | 8 | 8 |
| `review-05-security.md` | 0 | 0 | 0 | 15 | 15 |
| `review-06-auth-authz.md` | 0 | 0 | 0 | 6 | 6 |
| `review-07-httpx-core.md` | 0 | 0 | 0 | 10 | 10 |
| `review-08-httpx-middleware.md` | 0 | 0 | 0 | 14 | 14 |
| `review-09-websocket-realtime.md` | 0 | 0 | 1 | 14 | 15 |
| `review-10-grpcx.md` | 0 | 0 | 0 | 0 | 0 |
| `review-11-data-core-a.md` | 0 | 0 | 1 | 17 | 18 |
| `review-12-data-core-b.md` | 0 | 0 | 1 | 16 | 17 |
| `review-13-data-pg-stores.md` | 0 | 0 | 0 | 1 | 1 |
| `review-14-data-redis-stores.md` | 0 | 0 | 0 | 1 | 1 |
| `review-15-queues-streams.md` | 0 | 0 | 0 | 0 | 0 |
| `review-16-messaging-core.md` | 0 | 0 | 0 | 1 | 1 |
| `review-17-messaging-backends.md` | 0 | 0 | 0 | 8 | 8 |
| `review-18-storage-core.md` | 0 | 0 | 1 | 13 | 14 |
| `review-19-storage-backends.md` | 0 | 0 | 1 | 10 | 11 |
| `review-20-sqldb-outbox.md` | 0 | 0 | 0 | 1 | 1 |
| `review-21-redis-leader.md` | 0 | 0 | 0 | 1 | 1 |
| `review-22-secrets.md` | 0 | 0 | 0 | 4 | 4 |
| `review-23-observability-flags.md` | 0 | 0 | 0 | 4 | 4 |
| `review-24-cmd-clis.md` | 0 | 0 | 0 | 7 | 7 |
| `review-25-examples.md` | 0 | 0 | 0 | 0 | 0 |
| `review-26-testing-kits.md` | 0 | 0 | 0 | 0 | 0 |
| **TOTAL** | **0** | **0** | **5** | **155** | **160** |

## Notes

- Before this session: **187** (0 crit / 0 high / 5 med / 182 low).
- After this session: **160** (0 crit / 0 high / 5 med / 155 low). Cleared **27** LOWs from trackers.
- Keep OPEN MEDIUM (v3 / larger): review-09 heartbeat defaults; review-11 TenantStore non-atomic fallback;
  review-12 forgeable tenant key namespace; review-18 optional capability APIs; review-19 SFTP reconnect lease.
- Helper script: `tools/_cleanup_fixed_reviews.py` (matchers can be extended with this batch's titles).
