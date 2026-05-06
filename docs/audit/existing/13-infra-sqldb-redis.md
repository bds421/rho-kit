# infra/sqldb + infra/redis — connection wrappers, drivers, test helpers

## Landed

- ✅ **Postgres `sslmode` safer defaults** — default flipped from `disable` to `prefer`; with a TLS bundle present it escalates to `verify-full`; `Validate` rejects empty/`disable` outside dev environments (commit `a8fa6ed`).
- ✅ **MySQL DSN `loc=UTC` default** — was `Local`, which silently corrupted timestamps when dev fixtures landed in UTC databases (commit `a8fa6ed`).
- ✅ **gormmysql TLS registry dedup** — content-hash via SHA-256 over RootCAs.Subjects + leaf cert DER + ServerName; equivalent TLS settings reuse a single registry entry (commit `a8fa6ed`).
- ✅ **gormmysql TLS registry refcounted with `ReleaseTLS`** — registry entries now have refcounts; `ReleaseTLS(*tls.Config)` decrements and deregisters when count hits zero, equivalence by content fingerprint so callers don't have to retain the original `*tls.Config` (commit `af39f9c`). Closes the long-running-service / rotation leak path.

## Open

### [MEDIUM] `dbtest.StartPostgres` pins `sslmode=disable` → masks production default
**File**: `infra/sqldb/dbtest/postgres.go:46-53`
**Issue**: Tests pin sslmode=disable explicitly (fine for tests). Production previously defaulted to the same, masking the issue. Now that production defaults are `prefer`/`verify-full`, the test pin is just noise — but a `sqldb.Config.IsTLSEnabled()` helper would let tests assert TLS in non-dev environments.
**Fix**: Add a `sqldb.Config.IsTLSEnabled()` helper used by tests.

### [MEDIUM] `infra/redis.Connection` uses `NewClient` not `NewUniversalClient` despite typed as `UniversalClient`
**File**: `infra/redis/connection.go:128-167`
**Issue**: Caller field typed `UniversalClient` but constructed via `redis.NewClient(opts)` — single-node only. Production usage with Sentinel or Cluster cannot use this Connection wrapper.
**Fix**: Accept `*redis.UniversalOptions` (or both); use `redis.NewUniversalClient`.

### [MEDIUM] `redistest.Start` returns one shared URL via `sync.Once`; tests share key namespaces
**File**: `infra/redis/redistest/redis.go:14-50`
**Issue**: Lock/queue/idempotency tests all use predictable names. Tests share Redis state. Lock tests fail nondeterministically with `-shuffle=on`.
**Fix**: Per-test isolation (rotating `SELECT N` DB index or random key prefixes). Expose a `FlushDB(t)` helper.

### Migration checklist

- [x] Phase 1: gormmysql TLS registry deregister on close. ✅ `af39f9c`
- [ ] Phase 3: `infra/redis.Connection` Sentinel/Cluster support.
- [ ] Phase 3: `redistest` per-test isolation + `FlushDB` helper.
- [ ] Phase 3: `dbtest` IsTLSEnabled assertion helper.

### Related new packages

- [new/14-infra-sqldb-pgx.md](../new/14-infra-sqldb-pgx.md) — pgx-native option for LISTEN/NOTIFY, COPY, batched pipelines.
