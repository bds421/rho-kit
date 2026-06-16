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
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/lock"
)

// Locker is a [lock.Locker] backed by Postgres advisory locks.
type Locker struct {
	db     *sql.DB
	logger *slog.Logger
}

// MaxLockKeyLen caps the byte length of a lock key passed to
// [Locker.Acquire] / [Locker.AcquireTx]. Mirrors
// [redislock.MaxLockKeyLen]: the key is hashed before it reaches
// Postgres, but the same hygiene applies so unvalidated bytes never
// flow into spans/logs and the validation behavior matches its
// redislock/redlock siblings across the [lock.Locker] interface.
const MaxLockKeyLen = 1024

// validateLockKey enforces the kit's lock-key shape: non-empty, no
// control bytes, length within MaxLockKeyLen. pgadvisory hashes the key
// to an int64 (so an empty key would otherwise hash to a valid id and
// silently acquire), but it shares this guard with redislock/redlock so
// code swapping backends through [lock.Locker] gets identical key
// validation rather than a silent divergence.
func validateLockKey(key string) error {
	if key == "" {
		return errors.New("pgadvisory: lock key must not be empty")
	}
	if len(key) > MaxLockKeyLen {
		return errors.New("pgadvisory: lock key exceeds maximum length")
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x20 || c == 0x7f {
			return errors.New("pgadvisory: lock key contains control bytes")
		}
	}
	return nil
}

// Option configures a [Locker] at construction.
type Option func(*Locker)

// WithLogger sets the *slog.Logger used to record the double-release
// and extend-on-released-lock recovery paths at debug level. Those
// paths are contract-correct (Release returning ErrLockLost on a
// closed conn; Extend returning (false, nil)) but they are also
// signal-bearing — repeated occurrences indicate caller code is
// double-releasing or extending a lost lock. When unset the locker
// falls back to [slog.Default]. Matches the kit's per-package
// [WithLogger] convention.
func WithLogger(l *slog.Logger) Option {
	return func(lc *Locker) {
		if l != nil {
			lc.logger = l
		}
	}
}

// New constructs a Locker from a Postgres *sql.DB. The pool's
// MaxOpenConns must be sized to accommodate the expected number of
// concurrent session-scoped locks plus the application's normal query
// load.
func New(db *sql.DB, opts ...Option) *Locker {
	if db == nil {
		panic("pgadvisory: New db must not be nil")
	}
	lc := &Locker{db: db}
	for _, opt := range opts {
		if opt == nil {
			panic("pgadvisory: New option must not be nil")
		}
		opt(lc)
	}
	if lc.logger == nil {
		lc.logger = slog.Default()
	}
	return lc
}

// Acquire takes a session-scoped advisory lock for the given key. The
// returned [lock.Lock] holds a dedicated connection from the pool until
// Release is called.
//
// Returns (nil, false, nil) when the lock is held by another session.
// Returns (nil, false, err) on backend errors. A nil ctx is rejected —
// a nil context is a wiring bug that would otherwise panic deep inside
// database/sql. The key is validated the same way as redislock/redlock
// (non-empty, no control bytes, within [MaxLockKeyLen]); an invalid key
// returns (nil, false, err) so backends stay swappable through
// [lock.Locker].
func (l *Locker) Acquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if ctx == nil {
		return nil, false, fmt.Errorf("pgadvisory: Acquire requires a non-nil context")
	}
	ctx, span := startSpan(ctx, "lock.Acquire")
	defer span.End()
	lk, ok, err := l.doAcquire(ctx, key)
	recordResult(span, err)
	return lk, ok, err
}

func (l *Locker) doAcquire(ctx context.Context, key string) (lock.Lock, bool, error) {
	if err := validateLockKey(key); err != nil {
		return nil, false, err
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, false, redact.WrapError("pgadvisory: acquire conn", err)
	}
	id := keyToInt64(key)
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&got); err != nil {
		// The query failed client-side, but pg_try_advisory_lock may have
		// already succeeded server-side (e.g. ctx cancelled mid-flight, the
		// response was lost). Returning the connection to the pool would leak
		// the granted session-scoped lock indefinitely — it has no TTL.
		// Discard the connection so Postgres terminates the session and frees
		// any lock it holds.
		discardConn(conn)
		return nil, false, redact.WrapError("pgadvisory: pg_try_advisory_lock", err)
	}
	if !got {
		_ = conn.Close()
		return nil, false, nil
	}
	return &sessionLock{conn: conn, id: id, logger: l.logger}, true, nil
}

// discardConn forces the underlying physical connection to be removed from the
// pool instead of recycled. A session-scoped advisory lock is released only
// when its backing session ends, so whenever the unlock round trip cannot be
// confirmed the connection must be torn down rather than handed back to another
// caller still holding the lock.
//
// Returning [driver.ErrBadConn] from the [sql.Conn.Raw] callback marks the
// driver connection bad; database/sql then closes it on Close rather than
// returning it to the idle pool. If Raw itself fails (the conn is already
// done), Close still runs as a best-effort fallback.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// AcquireTx takes a transaction-scoped advisory lock inside tx. The
// lock is released automatically when tx commits or rolls back; no
// Lock handle is returned because there is nothing for the caller to
// release.
//
// Returns (true, nil) on success, (false, nil) when the lock is held
// elsewhere, or (false, err) on backend errors.
func (l *Locker) AcquireTx(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("pgadvisory: AcquireTx requires a non-nil context")
	}
	if tx == nil {
		return false, fmt.Errorf("pgadvisory: tx must not be nil")
	}
	if err := validateLockKey(key); err != nil {
		return false, err
	}
	ctx, span := startSpan(ctx, "lock.AcquireTx")
	defer span.End()
	id := keyToInt64(key)
	var got bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", id).Scan(&got); err != nil {
		recordResult(span, err)
		return false, redact.WrapError("pgadvisory: pg_try_advisory_xact_lock", err)
	}
	return got, nil
}

// sessionLock implements [lock.Lock] for a session-scoped advisory lock.
type sessionLock struct {
	conn   *sql.Conn
	id     int64
	logger *slog.Logger
}

// Release releases the advisory lock and returns the dedicated
// connection to the pool.
//
// pg_advisory_unlock returns false when the calling session never held
// the lock — that maps to ErrLockLost. The connection is still
// returned to the pool either way so a stale lock value doesn't leak
// connections.
func (s *sessionLock) Release(ctx context.Context) error {
	ctx, span := startSpan(ctx, "lock.Release")
	defer span.End()
	err := s.doRelease(ctx)
	recordResult(span, err)
	return err
}

func (s *sessionLock) doRelease(ctx context.Context) error {
	var ok bool
	if err := s.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", s.id).Scan(&ok); err != nil {
		// A second Release call hits a conn the first Release
		// already closed. Per the lock.Locker contract,
		// "Release on an already-released Lock" must return
		// ErrLockLost so callers can errors.Is detect it;
		// surfacing the raw "sql: connection is already closed"
		// would force every caller to special-case driver-level
		// error text.
		if errors.Is(err, sql.ErrConnDone) {
			// Contract-correct ErrLockLost path; also log at debug
			// so repeated occurrences are visible — they imply
			// caller code is double-releasing. The conn is already
			// closed, so Close is a no-op here.
			_ = s.conn.Close()
			s.logger.Debug("pgadvisory: Release on a closed conn (double-release)",
				"lock_id", s.id,
			)
			return lock.ErrLockLost
		}
		// The unlock round trip failed (e.g. ctx cancelled mid-flight). We
		// cannot confirm pg_advisory_unlock ran, so the session may still
		// hold the lock. Session-scoped locks have no TTL: returning this
		// connection to the pool would wedge the key permanently. Discard the
		// connection so Postgres ends the session and frees the lock.
		discardConn(s.conn)
		return redact.WrapError("pgadvisory: pg_advisory_unlock", err)
	}
	// Unlock executed successfully (lock freed, or it was never held by this
	// session). The session is healthy — return the connection to the pool.
	_ = s.conn.Close()
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
	ctx, span := startSpan(ctx, "lock.Extend")
	defer span.End()
	ok, err := s.doExtend(ctx)
	recordResult(span, err)
	return ok, err
}

func (s *sessionLock) doExtend(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, redact.WrapError("pgadvisory: extend ping", err)
	}
	if _, err := s.conn.ExecContext(ctx, "SELECT 1"); err != nil {
		// A previously-Released lock has a closed conn. The
		// lock.Lock contract says Extend on a released lock
		// returns (false, nil) — losing the race is normal in
		// distributed systems; the caller branches on the bool,
		// not on err.
		if errors.Is(err, sql.ErrConnDone) {
			// Contract-correct (false, nil) path: the lock was
			// already released before this Extend ran. Debug-log
			// because repeated occurrences imply caller code is
			// extending a lock it has already lost.
			s.logger.Debug("pgadvisory: Extend on a closed conn (lock already released)",
				"lock_id", s.id,
			)
			return false, nil
		}
		return false, redact.WrapError("pgadvisory: extend ping", err)
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
