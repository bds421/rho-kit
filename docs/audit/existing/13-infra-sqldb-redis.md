# infra/sqldb + infra/redis — connection wrappers, drivers, test helpers

## Landed

- ✅ **Postgres `sslmode` safer defaults** — default flipped from `disable` to `prefer`; with a TLS bundle present it escalates to `verify-full`; `Validate` rejects empty/`disable` outside dev environments (commit `a8fa6ed`).
- ✅ **MySQL DSN `loc=UTC` default** — was `Local`, which silently corrupted timestamps when dev fixtures landed in UTC databases (commit `a8fa6ed`).
- ✅ **gormmysql TLS registry dedup** — content-hash via SHA-256 over RootCAs.Subjects + leaf cert DER + ServerName; equivalent TLS settings reuse a single registry entry (commit `a8fa6ed`).
- ✅ **gormmysql TLS registry refcounted with `ReleaseTLS`** — registry entries now have refcounts; `ReleaseTLS(*tls.Config)` decrements and deregisters when count hits zero, equivalence by content fingerprint so callers don't have to retain the original `*tls.Config` (commit `af39f9c`). Closes the long-running-service / rotation leak path.

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `4280790`)

- ✅ **`sqldb.Config.IsTLSEnabled()` + `IsTLSAttempted()`** — recognises Postgres (`require`/`verify-ca`/`verify-full`) and MySQL (`true`/`custom-*`); the softer `IsTLSAttempted` covers `prefer`/`allow`/`preferred`/`skip-verify` for telemetry but is **not** treated as TLS-enabled (would silently legitimise insecure configs — the post-merge code review caught this and `4d04fe1` tightened the boundary).
- ✅ **`infra/redis.ConnectUniversal(opts *redis.UniversalOptions, ...)`** — Sentinel and Cluster topologies are now supported by the same Connection wrapper.
- ✅ **`redistest.FlushDB(t)`** — per-test isolation helper; tests can use `t.Cleanup(func() { redistest.FlushDB(t) })` to scrub between scenarios.

### Migration checklist

- [x] Phase 1: gormmysql TLS registry deregister on close. ✅ `af39f9c`
- [x] Phase 3: `infra/redis.Connection` Sentinel/Cluster support. ✅ `4280790`
- [x] Phase 3: `redistest` per-test isolation + `FlushDB` helper. ✅ `4280790`
- [x] Phase 3: `dbtest` IsTLSEnabled assertion helper. ✅ `4280790`

### Related new packages

- [new/14-infra-sqldb-pgx.md](../new/14-infra-sqldb-pgx.md) — pgx-native option for LISTEN/NOTIFY, COPY, batched pipelines.
