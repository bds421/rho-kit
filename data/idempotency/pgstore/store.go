// Package pgstore provides a PostgreSQL-backed implementation of the
// idempotency.Store interface for deployments without Redis.
//
// Locking uses INSERT ... ON CONFLICT with a conditional update for atomic
// lock acquisition. Each row carries an owner_token (set by TryLock; required
// for Set/Unlock) and a fingerprint of the request that originally claimed
// the slot, so the store can reject reuse of the same key with a different
// body — see [idempotency.Store] for the full contract.
package pgstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// validTableName matches safe SQL identifiers: alphanumeric + underscore,
// optionally schema-qualified (schema.table).
var validTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// Compile-time interface check.
var _ idempotency.Store = (*PgStore)(nil)

// Option configures the PgStore.
type Option func(*PgStore)

// WithTableName sets the table name for idempotency entries.
// Default: "idempotency_keys". Panics if the name contains unsafe characters.
func WithTableName(name string) Option {
	return func(s *PgStore) { s.table = name }
}

// PgStore implements idempotency.Store using PostgreSQL.
// Safe for concurrent use across multiple service instances.
type PgStore struct {
	db    *sql.DB
	table string
}

// intervalSeconds formats a duration as a PostgreSQL-compatible interval
// literal, rounding sub-second values up to 1 second. PostgreSQL intervals
// in this code path are addressed at second precision; truncating with
// int(d.Seconds()) used to round 500ms down to "0 seconds" and create a
// row that was already expired before the lock could be observed.
func intervalSeconds(d time.Duration) string {
	secs := d / time.Second
	if d%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf("%d seconds", secs)
}

// New creates a PgStore backed by the given database connection.
// Panics if db is nil or the table name is not a valid SQL identifier —
// both are programming errors that would otherwise defer the failure to
// the first request after startup.
func New(db *sql.DB, opts ...Option) *PgStore {
	if db == nil {
		panic("pgstore: New requires a non-nil *sql.DB")
	}
	s := &PgStore{
		db:    db,
		table: "idempotency_keys",
	}
	for _, o := range opts {
		o(s)
	}
	if !validTableName.MatchString(s.table) {
		panic("pgstore: invalid table name: " + s.table)
	}
	return s
}

// Get returns a cached response for the key, applying fingerprint comparison
// when a non-nil fingerprint is supplied.
func (s *PgStore) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	query := fmt.Sprintf(
		`SELECT status_code, headers, response_body, fingerprint FROM %s
		 WHERE key = $1 AND response_body IS NOT NULL AND expires_at > now()`,
		s.table,
	)

	var statusCode int
	var headersJSON, body, storedFP []byte

	err := s.db.QueryRowContext(ctx, query, key).Scan(&statusCode, &headersJSON, &body, &storedFP)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("pgstore: get %q: %w", key, err)
	}

	if fingerprint != nil && storedFP != nil && !bytes.Equal(storedFP, fingerprint) {
		return nil, true, nil
	}

	// Headers JSON corruption policy: fail closed.
	//
	// If headers JSON is malformed, we surface the error rather than partially
	// recover (e.g. with empty headers). The middleware treats any backend
	// error as 500, which means the client retries — re-running the handler
	// and re-populating the row with fresh headers. Better than serving a
	// cached response with the wrong headers (which could leak Set-Cookie /
	// Authorization across requests if those ever slipped past the
	// identity-header strip in the middleware).
	var headers map[string][]string
	if len(headersJSON) > 0 {
		if err := json.Unmarshal(headersJSON, &headers); err != nil {
			return nil, false, fmt.Errorf("pgstore: unmarshal headers %q: %w", key, err)
		}
	}

	return &idempotency.CachedResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}, false, nil
}

// Set replaces the lock row with the cached response. Returns
// [idempotency.ErrLockLost] if the caller's token no longer matches the
// current row's owner_token (TTL expired and another caller acquired).
// Returns [idempotency.ErrInvalidTTL] when ttl <= 0 — the interval cast
// would otherwise round sub-second values to "0 seconds" and create a
// row that's already expired before any consumer can read it.
func (s *PgStore) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	if ttl <= 0 {
		return idempotency.ErrInvalidTTL
	}
	headersJSON, err := json.Marshal(resp.Headers)
	if err != nil {
		return fmt.Errorf("pgstore: marshal headers: %w", err)
	}

	query := fmt.Sprintf(
		`UPDATE %s SET
		   status_code   = $1,
		   headers       = $2,
		   response_body = $3,
		   expires_at    = now() + $4::interval
		 WHERE key = $5 AND owner_token = $6 AND expires_at > now()`,
		s.table,
	)

	result, err := s.db.ExecContext(ctx, query,
		resp.StatusCode, headersJSON, resp.Body, intervalSeconds(ttl), key, token)
	if err != nil {
		return fmt.Errorf("pgstore: set %q: %w", key, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("pgstore: set %q rows affected: %w", key, err)
	}
	if rows == 0 {
		return idempotency.ErrLockLost
	}
	return nil
}

// TryLock implements the contract from [idempotency.Store.TryLock]. The
// fingerprint is stored alongside the owner_token so subsequent TryLock
// calls with a *different* fingerprint can be rejected with
// fingerprintMismatch=true. Returns [idempotency.ErrInvalidTTL] when
// ttl <= 0.
func (s *PgStore) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if ttl <= 0 {
		return "", false, false, idempotency.ErrInvalidTTL
	}
	token := idempotency.GenerateToken()

	query := fmt.Sprintf(
		`INSERT INTO %s (key, owner_token, fingerprint, expires_at)
		 VALUES ($1, $2, $3, now() + $4::interval)
		 ON CONFLICT (key) DO UPDATE SET
		   owner_token   = $2,
		   fingerprint   = $3,
		   expires_at    = now() + $4::interval,
		   status_code   = NULL,
		   headers       = NULL,
		   response_body = NULL
		 WHERE %s.expires_at <= now()`,
		s.table, s.table,
	)

	result, err := s.db.ExecContext(ctx, query, key, token, fingerprint, intervalSeconds(ttl))
	if err != nil {
		return "", false, false, fmt.Errorf("pgstore: lock %q: %w", key, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return "", false, false, fmt.Errorf("pgstore: lock %q rows affected: %w", key, err)
	}
	if rows == 1 {
		return token, false, true, nil
	}

	// Acquisition failed because the row exists and is not expired. Inspect
	// the existing fingerprint to distinguish "concurrent retry, same body"
	// from "different body, 422".
	var storedFP []byte
	err = s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT fingerprint FROM %s WHERE key = $1 AND expires_at > now()`, s.table),
		key,
	).Scan(&storedFP)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Race: row TTL expired between our INSERT and our SELECT. The
			// next TryLock will succeed.
			return "", false, false, nil
		}
		return "", false, false, fmt.Errorf("pgstore: inspect %q: %w", key, err)
	}
	if fingerprint != nil && storedFP != nil && !bytes.Equal(storedFP, fingerprint) {
		return "", true, false, nil
	}
	return "", false, false, nil
}

// Unlock releases the processing lock. Best-effort: token mismatch is a
// silent no-op (returns nil) because Unlock runs in panic-cleanup paths
// where surfacing lock-loss would mask the original panic.
func (s *PgStore) Unlock(ctx context.Context, key, token string) error {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE key = $1 AND owner_token = $2 AND response_body IS NULL`,
		s.table,
	)
	_, err := s.db.ExecContext(ctx, query, key, token)
	if err != nil {
		return fmt.Errorf("pgstore: unlock %q: %w", key, err)
	}
	return nil
}

// DeleteExpired removes all expired entries. Call this periodically
// (e.g., via cron) to prevent table bloat.
func (s *PgStore) DeleteExpired(ctx context.Context) (int64, error) {
	query := fmt.Sprintf(`DELETE FROM %s WHERE expires_at <= now()`, s.table)
	result, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("pgstore: delete expired: %w", err)
	}
	return result.RowsAffected()
}
