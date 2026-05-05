# infra/sqldb + infra/redis — connection wrappers, drivers, test helpers

### [CRITICAL] Postgres `sslmode` defaults to `disable`
**File**: `infra/sqldb/gormdb/gormpostgres/driver.go:90` + `infra/sqldb/config.go:294-303`
**Issue**: `buildPostgresDSN` defaults `sslmode=disable`. `Validate` allows empty sslmode in any environment, including production. A service that doesn't set `DB_SSL_MODE` ships with TLS off — credentials and queries on the wire.
**Fix**: Default to `require` in non-development environments. Reject empty/`disable` in `Validate` for non-dev. At minimum bump default in `buildPostgresDSN` from `disable` to `prefer`.
**Effort**: S
**Phase**: 1
**Migration**: Existing dev environments unaffected. Existing prod environments that relied on the insecure default will fail-loud at startup; document the migration before release.

### [HIGH] MySQL DSN hardcodes `loc=Local` (TZ-skew bug)
**File**: `infra/sqldb/gormdb/gormmysql/driver.go:78-89`
**Issue**: `time.Time` values interpreted in pod-local TZ. UTC pods interpret as UTC; dev laptops as local time. Silently corrupts timestamps when dev fixtures are loaded into UTC databases.
**Fix**: Default to `loc=UTC`. Allow `Options["loc"]` override.
**Effort**: S

### [HIGH] gormmysql TLS config registry leak per Open
**File**: `infra/sqldb/gormdb/gormmysql/driver.go:42-49`
**Issue**: Every Open with TLS calls `mysqldriver.RegisterTLSConfig("custom-N", ...)`. The driver's TLS registry is a global map with no `DeregisterTLSConfig` here. Long-running services that re-Open after connection failure (or tests creating many connections) leak indefinitely.
**Fix**: Track registered name on the connection; call `mysqldriver.DeregisterTLSConfig(tlsKey)` on connection close. Or hash-dedupe by TLS config fingerprint and reuse.
**Effort**: S

### [MEDIUM] `dbtest.StartPostgres` pins `sslmode=disable` → masks production default
**File**: `infra/sqldb/dbtest/postgres.go:46-53`
**Issue**: Tests pin sslmode=disable explicitly (fine for tests). Production defaults to the same. Test suites pass with "secure-looking" config (explicit disable) while prod silently inherits the same insecure default.
**Fix**: Ties to the production fix above. Add a `sqldb.Config.IsTLSEnabled()` helper used by tests to assert TLS in non-dev environments.

### [MEDIUM] `infra/redis.Connection` uses `NewClient` not `NewUniversalClient` despite typed as `UniversalClient`
**File**: `infra/redis/connection.go:128-167`
**Issue**: Caller field typed `UniversalClient` but constructed via `redis.NewClient(opts)` — single-node only. Production usage with Sentinel or Cluster cannot use this Connection wrapper.
**Fix**: Accept `*redis.UniversalOptions` (or both); use `redis.NewUniversalClient`.

### [MEDIUM] `redistest.Start` returns one shared URL via `sync.Once`; tests share key namespaces
**File**: `infra/redis/redistest/redis.go:14-50`
**Issue**: Lock/queue/idempotency tests all use predictable names. Tests share Redis state. Lock tests fail nondeterministically with `-shuffle=on`.
**Fix**: Per-test isolation (rotating `SELECT N` DB index or random key prefixes). Expose a `FlushDB(t)` helper.

### Migration checklist

- [ ] Phase 1: Postgres `sslmode=require` default + `Validate` rejection in non-dev.
- [ ] Phase 1: MySQL DSN `loc=UTC` default.
- [ ] Phase 1: gormmysql TLS registry deregister on close.
- [ ] Phase 3: `infra/redis.Connection` Sentinel/Cluster support.
- [ ] Phase 3: `redistest` per-test isolation + `FlushDB` helper.

### Related new packages

- [new/14-infra-sqldb-pgx.md](../new/14-infra-sqldb-pgx.md) — pgx-native option for LISTEN/NOTIFY, COPY, batched pipelines.
