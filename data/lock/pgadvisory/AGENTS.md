# AGENTS.md — `data/lock/pgadvisory`

## When to use this package

- The service already depends on Postgres (any kit `data/*pgstore` or `infra/sqldb/pgx` user qualifies).
- True mutual exclusion required — the session-scoped variant has actual fencing (Postgres refuses concurrent writes from the locked session), not TTL-based hope.
- Critical writes that must NEVER overlap: financial transactions, schema migrations, single-writer reconciliation jobs.

## When to use something else

- **No Postgres in the path:** `data/lock/redislock` (single-instance, brief overlap acceptable) or `data/lock/redislock/redlock` (quorum, true exclusion).
- **The lock guards a NON-Postgres resource (Redis cache, S3 object, AMQP queue setup):** `redislock` is more natural — the lock and the resource live in the same broker.
- **Leader election (long-lived single-leader):** `infra/leaderelection/pgadvisory` — wraps this primitive in the kit's `OnAcquired`/`OnLost` lifecycle.
- **Inside a transaction that already exists:** use `AcquireTx` (transaction-scoped). Avoids pinning an extra connection.

## Key APIs

- `New(db)` — wraps an `*sql.DB`. Size `MaxOpenConns` to accommodate concurrent session-scoped locks plus normal query load.
- `Acquire(ctx, key)` — session-scoped. Holds one connection from the pool until `Release`. Use for long-lived locks where you want auto-release on connection drop.
- `AcquireTx(ctx, tx, key)` — transaction-scoped. Released on COMMIT/ROLLBACK. No `Lock` handle returned because there's nothing to release manually. Use for "lock + write + commit" patterns.
- `Lock.Extend(ctx)` — pings the session connection. Returns `(false, err)` if the session is dead.

## Common mistakes

- **Using session-scoped `Acquire` for hot-loop locks** — every active lock pins one pool connection. A pool of 25 with 25 long-held locks deadlocks every query. Use `AcquireTx` inside the transaction that needs exclusion.
- **Treating `ErrLockLost` from `Release` as a fatal error** — it means "your session is no longer the holder" (network drop, server failover). Inspect via `errors.Is(err, lock.ErrLockLost)` and reconcile.
- **Calling `Release` without `defer`** — a panicking handler leaks the connection back to the pool but the lock stays held until session timeout. `WithLock`-style wrapping (build your own) handles this.
- **Computing `keyToInt64` yourself with FNV** — the kit hashes with SHA-256 specifically to defeat the adversarial-key collision attack that previous versions had. Always pass strings to `Acquire`/`AcquireTx`; never compute the int64 key manually.

## Observability

- OTel spans: `lock.Acquire` / `lock.AcquireTx` / `lock.Release` / `lock.Extend` with `db.system=postgresql`, `kit.lock.backend=pgadvisory`.
