// Package pgstore provides a PostgreSQL-backed implementation of the
// idempotency.Store interface for deployments without Redis.
//
// Locking uses INSERT ... ON CONFLICT with conditional update for atomic
// lock acquisition. Cached responses and locks are stored in a single
// table with TTL-based expiration.
package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bds421/rho-kit/data/idempotency"
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
//
// The store uses a single table for both cached responses and processing
// locks. Locks are rows with a NULL response_body; cached responses have
// the full response populated. This avoids a separate locks table and
// simplifies cleanup.
type PgStore struct {
	db    *sql.DB
	table string
}

// intervalSeconds formats a duration as a PostgreSQL-compatible interval literal.
func intervalSeconds(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

// New creates a PgStore backed by the given database connection.
// Panics if the table name is not a valid SQL identifier.
func New(db *sql.DB, opts ...Option) *PgStore {
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

// Get returns a cached response for the key, or (nil, nil) if not found
// or expired.
func (s *PgStore) Get(ctx context.Context, key string) (*idempotency.CachedResponse, error) {
	query := fmt.Sprintf(
		`SELECT status_code, headers, response_body FROM %s
		 WHERE key = $1 AND response_body IS NOT NULL AND expires_at > now()`,
		s.table,
	)

	var statusCode int
	var headersJSON, body []byte

	err := s.db.QueryRowContext(ctx, query, key).Scan(&statusCode, &headersJSON, &body)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("pgstore: get %q: %w", key, err)
	}

	var headers map[string][]string
	if len(headersJSON) > 0 {
		if err := json.Unmarshal(headersJSON, &headers); err != nil {
			return nil, fmt.Errorf("pgstore: unmarshal headers %q: %w", key, err)
		}
	}

	return &idempotency.CachedResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}, nil
}

// Set stores a response for the key with the given TTL.
// This replaces the lock row (if any) with the full cached response.
func (s *PgStore) Set(ctx context.Context, key string, resp idempotency.CachedResponse, ttl time.Duration) error {
	headersJSON, err := json.Marshal(resp.Headers)
	if err != nil {
		return fmt.Errorf("pgstore: marshal headers: %w", err)
	}

	query := fmt.Sprintf(
		`INSERT INTO %s (key, status_code, headers, response_body, expires_at)
		 VALUES ($1, $2, $3, $4, now() + $5::interval)
		 ON CONFLICT (key) DO UPDATE SET
		   status_code = EXCLUDED.status_code,
		   headers = EXCLUDED.headers,
		   response_body = EXCLUDED.response_body,
		   expires_at = EXCLUDED.expires_at`,
		s.table,
	)

	_, err = s.db.ExecContext(ctx, query, key, resp.StatusCode, headersJSON, resp.Body, intervalSeconds(ttl))
	if err != nil {
		return fmt.Errorf("pgstore: set %q: %w", key, err)
	}
	return nil
}

// TryLock attempts to acquire a processing lock for the key.
// Returns true if the lock was acquired, false if already locked.
//
// Uses INSERT ... ON CONFLICT with a conditional update. If the row
// exists and is not expired, the update is skipped (lock not acquired).
// Expired rows are reclaimed by nulling out any stale cached response.
func (s *PgStore) TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	query := fmt.Sprintf(
		`INSERT INTO %s (key, expires_at)
		 VALUES ($1, now() + $2::interval)
		 ON CONFLICT (key) DO UPDATE SET
		   expires_at = now() + $2::interval,
		   status_code = NULL,
		   headers = NULL,
		   response_body = NULL
		 WHERE %s.expires_at <= now()`,
		s.table, s.table,
	)

	result, err := s.db.ExecContext(ctx, query, key, intervalSeconds(ttl))
	if err != nil {
		return false, fmt.Errorf("pgstore: lock %q: %w", key, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("pgstore: lock %q rows affected: %w", key, err)
	}

	// rows == 1 means we either inserted a new row or updated an expired one.
	return rows == 1, nil
}

// Unlock releases the processing lock for the key by deleting the row
// (only if it has no cached response yet — i.e., still just a lock).
func (s *PgStore) Unlock(ctx context.Context, key string) error {
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE key = $1 AND response_body IS NULL`,
		s.table,
	)
	_, err := s.db.ExecContext(ctx, query, key)
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
