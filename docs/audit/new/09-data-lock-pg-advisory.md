# NEW: data/lock/pgadvisory

**Phase**: 5 (Tier‑2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/data/lock/pgadvisory`

## Why

The `data/lock/redislock` package doc explicitly recommends database-level locking for critical writes — but the kit doesn't ship one. PostgreSQL advisory locks are the obvious fit: they live inside an existing transaction, hold the lock as long as the session does, and provide fencing-equivalent guarantees because the database itself enforces it.

## Public API

```go
package pgadvisory

// Lock conforms to the future data/lock.Locker interface (after the redislock
// refit — see existing/07-data-lock-and-queue.md).
type Lock struct { /* ... */ }

func New(db *sql.DB) *Lock

// Acquire takes a session-scoped advisory lock. Returns a per-call Handle
// that holds a dedicated connection from the pool until Release is called.
//
// Use a stable hash of `key` for the int8 lock id (Postgres advisory locks
// are keyed on int8, not strings).
func (l *Lock) Acquire(ctx context.Context, key string) (Handle, bool, error)

// AcquireTx takes a transaction-scoped advisory lock; released automatically
// when tx commits or rolls back. Preferred when the lock guards a single
// transaction; no Handle returned.
func (l *Lock) AcquireTx(ctx context.Context, tx *sql.Tx, key string) (bool, error)

type Handle interface {
    Release(ctx context.Context) error
    // Refresh is a no-op for session locks (Postgres holds the lock for the
    // session lifetime). Provided for interface compatibility with redislock.
    Refresh(ctx context.Context) error
}
```

## Notes

- Use `pg_try_advisory_lock(key)` for non-blocking, `pg_advisory_lock(key)` for blocking with retry.
- Use `pg_try_advisory_xact_lock(key)` for the tx-scoped variant.
- The int8 key derivation must be stable: `hash := fnv.New64a(); hash.Write([]byte(key)); return int64(hash.Sum64())`.
- Document the connection-pinning cost: session-scoped locks take a connection out of the pool for their lifetime.

## Definition of done

- [ ] Package + tests against `dbtest.StartPostgres`.
- [ ] Both session- and tx-scoped APIs.
- [ ] Conforms to refitted `data/lock.Locker` interface.
- [ ] Recipe entry in `docs/ai/sqldb.md` comparing `pgadvisory` vs `redislock`.
