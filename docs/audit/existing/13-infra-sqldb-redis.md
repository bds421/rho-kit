# infra/sqldb + infra/redis — connection wrappers, drivers, test helpers

## Landed

- ✅ **Postgres `sslmode` safer defaults** — default flipped from `disable` to `prefer`; with a TLS bundle present it escalates to `verify-full`; `Validate` rejects empty/`disable` outside dev environments (commit `a8fa6ed`).
- ✅ **MySQL DSN `loc=UTC` default** — was `Local`, which silently corrupted timestamps when dev fixtures landed in UTC databases (commit `a8fa6ed`).
- ✅ **gormmysql TLS registry dedup** — content-hash via SHA-256 over RootCAs.Subjects + leaf cert DER + ServerName; equivalent TLS settings reuse a single registry entry (commit `a8fa6ed`). Note: full *deregister-on-close* is still open below.

## Open

### [HIGH] gormmysql TLS registry never deregistered
**File**: `infra/sqldb/gormdb/gormmysql/driver.go:42-49` + `mysql.go`
**Issue**: Even with the dedup landed in `a8fa6ed`, the driver's TLS registry is a global map with no `mysqldriver.DeregisterTLSConfig` call when a Connection closes. Long-running services that go through many distinct TLS configs (e.g. rotating client certs) accumulate registry entries indefinitely. Dedup only avoids re-registering *equivalent* configs.
**Fix**: Track the registered name on the connection; call `mysqldriver.DeregisterTLSConfig(tlsKey)` on connection close. Reference-count if multiple Open calls share a name.
**Effort**: S
**Phase**: 1

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

- [ ] Phase 1: gormmysql TLS registry deregister on close.
- [ ] Phase 3: `infra/redis.Connection` Sentinel/Cluster support.
- [ ] Phase 3: `redistest` per-test isolation + `FlushDB` helper.
- [ ] Phase 3: `dbtest` IsTLSEnabled assertion helper.

### Related new packages

- [new/14-infra-sqldb-pgx.md](../new/14-infra-sqldb-pgx.md) — pgx-native option for LISTEN/NOTIFY, COPY, batched pipelines.
