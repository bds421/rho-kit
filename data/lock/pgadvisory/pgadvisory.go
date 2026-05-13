// Package pgadvisory implements [lock.Locker] using PostgreSQL
// advisory locks.
//
// Compared to redislock:
//
//   - No external broker. The lock state lives in the database that
//     already serves the workload.
//   - True fencing. The database refuses concurrent writes to the
//     locked rows because the locking session holds the connection.
//   - Session-scoped. The lock is automatically released when the
//     session ends (process crash, network drop) — no TTL juggling.
//
// Cost: each session-scoped lock holds one connection from the pool
// for its lifetime. For long-running locks, size the pool accordingly
// or prefer the transaction-scoped variant ([Locker.AcquireTx]) which
// releases on COMMIT/ROLLBACK and doesn't pin the connection past the
// transaction boundary.
//
// Recommended for:
//
//   - Critical writes (financial transactions, schema migrations,
//     leader election — see leader-election package).
//   - Workloads where the database is the source of truth anyway.
//
// Use redislock when there is no Postgres in the path or when the
// lock guards a non-Postgres resource (Redis cache invalidation, etc).
package pgadvisory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"

	"github.com/bds421/rho-kit/data/v2/lock"
)

// Locker is a [lock.Locker] backed by Postgres advisory locks.
type Locker struct {
	db *sql.DB
}

// New constructs a Locker from a Postgres *sql.DB. The pool's
// MaxOpenConns must be sized to accommodate the expected number of
// concurrent session-scoped locks plus the application's normal query
// load.
func New(db *sql.DB) *Locker {
	if db == nil {
		panic("pgadvisory: db must not be nil")
	}
	return &Locker{db: db}
}

// Acquire takes a session-scoped advisory lock for the given key. The
// returned [lock.Lock] holds a dedicated connection from the pool until
// Release is called.
//
// Returns (nil, false, nil) when the lock is held by another session.
// Returns (nil, false, err) on backend errors.
func (l *Locker) Acquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("pgadvisory: acquire conn: %w", err)
	}
	id := keyToInt64(key)
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("pgadvisory: pg_try_advisory_lock: %w", err)
	}
	if !got {
		_ = conn.Close()
		return nil, false, nil
	}
	return &sessionLock{conn: conn, id: id}, true, nil
}

// AcquireTx takes a transaction-scoped advisory lock inside tx. The
// lock is released automatically when tx commits or rolls back; no
// Lock handle is returned because there is nothing for the caller to
// release.
//
// Returns (true, nil) on success, (false, nil) when the lock is held
// elsewhere, or (false, err) on backend errors.
func (l *Locker) AcquireTx(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("pgadvisory: tx must not be nil")
	}
	id := keyToInt64(key)
	var got bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", id).Scan(&got); err != nil {
		return false, fmt.Errorf("pgadvisory: pg_try_advisory_xact_lock: %w", err)
	}
	return got, nil
}

// sessionLock implements [lock.Lock] for a session-scoped advisory lock.
type sessionLock struct {
	conn *sql.Conn
	id   int64
}

// Release releases the advisory lock and returns the dedicated
// connection to the pool.
//
// pg_advisory_unlock returns false when the calling session never held
// the lock — that maps to ErrLockLost. The connection is still
// returned to the pool either way so a stale lock value doesn't leak
// connections.
func (s *sessionLock) Release(ctx context.Context) error {
	defer func() { _ = s.conn.Close() }()
	var ok bool
	if err := s.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", s.id).Scan(&ok); err != nil {
		return fmt.Errorf("pgadvisory: pg_advisory_unlock: %w", err)
	}
	if !ok {
		return lock.ErrLockLost
	}
	return nil
}

// Extend round-trips the dedicated session connection so callers can
// detect lost-leader scenarios (network drop, server failover). Postgres
// holds the lock for the session's lifetime; what Extend verifies is
// that the session itself is still alive. A failed ping returns
// (false, err) so the [leaderelection.Elector] can step down.
func (s *sessionLock) Extend(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("pgadvisory: extend ping: %w", err)
	}
	if _, err := s.conn.ExecContext(ctx, "SELECT 1"); err != nil {
		return false, fmt.Errorf("pgadvisory: extend ping: %w", err)
	}
	return true, nil
}

// keyToInt64 hashes the caller's string key into the int8 advisory
// lock space using the leading 8 bytes of SHA-256. SHA-256 is
// cryptographically collision-resistant against adversarial inputs;
// FNV-1a is not, so a previous version was vulnerable to attacker-chosen
// keys colliding into the same advisory-lock id and silently breaking
// mutual exclusion. SHA-256 is deterministic across processes, so the
// shared-key contract is preserved.
//
// Note: int64 still has 64-bit space, so birthday-paradox collision is
// possible at ~2^32 distinct keys — for adversarially-chosen keys the
// attacker has to brute-force SHA-256, which is infeasible.
func keyToInt64(key string) int64 {
	sum := sha256.Sum256([]byte(key))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
