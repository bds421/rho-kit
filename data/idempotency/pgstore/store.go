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

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// validTableName matches safe SQL identifiers: alphanumeric + underscore,
// optionally schema-qualified (schema.table).
var validTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// Compile-time interface check.
var _ idempotency.Store = (*Store)(nil)

// Option configures the Store.
type Option func(*Store)

// WithTableName sets the table name for idempotency entries.
// Default: "idempotency_keys". The name is validated when [New] runs
// (unsafe characters panic there), not when the option closure is built.
func WithTableName(name string) Option {
	return func(s *Store) { s.table = name }
}

// Store implements idempotency.Store using PostgreSQL.
// Safe for concurrent use across multiple service instances.
type Store struct {
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

// New creates a Store backed by the given database connection.
// Panics if db is nil or the table name is not a valid SQL identifier —
// both are programming errors that would otherwise defer the failure to
// the first request after startup.
func New(db *sql.DB, opts ...Option) *Store {
	if db == nil {
		panic("pgstore: New requires a non-nil *sql.DB")
	}
	s := &Store{
		db:    db,
		table: "idempotency_keys",
	}
	for _, o := range opts {
		if o == nil {
			panic("pgstore: New option must not be nil")
		}
		o(s)
	}
	if !validTableName.MatchString(s.table) {
		panic("pgstore: New invalid table name")
	}
	return s
}

// maxCachedHeadersBytes bounds the on-disk size of the headers JSON column
// that doGet will pull into Go memory and json.Unmarshal. The body column is
// size-gated separately (Wave 66) via octet_length(response_body), but the
// headers JSON column had no gate: a hostile or legacy row with a multi-MB
// headers blob would be fully allocated and unmarshalled before
// ValidateCachedResponse could reject it — the same allocation-before-cap
// pattern the body gate closed.
//
// The bound is derived from the per-field caps that ValidateCachedResponse
// enforces so it never rejects a legitimately stored row. The worst-case
// JSON encoding of a maximal valid headers map is:
//
//	MaxCachedHeaders header entries, each with
//	  a name up to MaxCachedHeaderNameBytes, and
//	  up to MaxCachedHeaderValues values, each up to MaxCachedHeaderValueBytes,
//
// plus JSON structural overhead (quotes, colons, commas, brackets).
//
// ValidateCachedResponse permits '"' and '\' in header values; each such
// byte doubles under JSON string escaping, and octet_length(headers::text)
// measures the escaped form. We therefore budget 2× MaxCachedHeaderValueBytes
// per value (plus fixed overhead for quoting/separators) so a legitimately
// stored maximal map rich in quotes/backslashes never trips the gate.
const maxCachedHeadersBytes = int64(idempotency.MaxCachedHeaders) *
	(int64(idempotency.MaxCachedHeaderNameBytes) + 8 +
		int64(idempotency.MaxCachedHeaderValues)*(2*int64(idempotency.MaxCachedHeaderValueBytes)+8))

// headersWithinBound reports whether a headers JSON column of n bytes is small
// enough to materialise. Extracted as a pure helper so the size-gate threshold
// can be unit-tested without a live database.
func headersWithinBound(n int64) bool {
	return n <= maxCachedHeadersBytes
}

// Get returns a cached response for the key, applying fingerprint comparison
// when a non-nil fingerprint is supplied.
//
// The "is this row cached?" discriminator is `status_code IS NOT NULL`,
// not `response_body IS NOT NULL` (audit FR-030). pgx maps a Go `nil`
// []byte to SQL NULL, so a handler that legitimately responded with an
// empty body (HTTP 204 No Content, 304 Not Modified, an empty 200) used
// to look identical to "still locked, no response yet" — Get returned
// (nil, false, nil) and the middleware would re-execute the handler
// instead of replaying. status_code is only ever set by [Store.Set]
// and cleared back to NULL by [Store.TryLock] (ON CONFLICT branch),
// so it is the correct cache-vs-lock signal.
func (s *Store) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	ctx, span := s.startSpan(ctx, "idempotency.Get")
	defer span.End()
	cached, mismatch, err := s.doGet(ctx, key, fingerprint)
	recordResult(span, err)
	return cached, mismatch, err
}

func (s *Store) doGet(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	if err := idempotency.ValidateStorageKey(key); err != nil {
		return nil, false, err
	}
	// Size-gate body and headers in the same SELECT that fetches the row
	// so a hostile swap between probe and read cannot force a multi-MB
	// allocation (TOCTOU on the old two-query path). CASE WHEN projects
	// NULL for oversize columns; lengths are returned alongside so the
	// caller can distinguish oversize from a legitimate empty body.
	// headers is JSONB: octet_length needs a text cast.
	query := fmt.Sprintf(
		`SELECT status_code,
		        CASE WHEN COALESCE(octet_length(response_body), 0) <= $2 THEN response_body END,
		        CASE WHEN COALESCE(octet_length(headers::text), 0) <= $3 THEN headers END,
		        fingerprint,
		        COALESCE(octet_length(response_body), 0),
		        COALESCE(octet_length(headers::text), 0)
		 FROM %s
		 WHERE key = $1 AND status_code IS NOT NULL AND expires_at > now()`,
		s.table,
	)

	var statusCode int
	var headersJSON, body, storedFP []byte
	var bodyLen, headersLen int64

	err := s.db.QueryRowContext(ctx, query, key, idempotency.MaxCachedBodyBytes, maxCachedHeadersBytes).
		Scan(&statusCode, &body, &headersJSON, &storedFP, &bodyLen, &headersLen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, redact.WrapError("pgstore: get", err)
	}
	if bodyLen > int64(idempotency.MaxCachedBodyBytes) {
		return nil, false, fmt.Errorf("pgstore: stored response body exceeds %d bytes", idempotency.MaxCachedBodyBytes)
	}
	if !headersWithinBound(headersLen) {
		return nil, false, fmt.Errorf("pgstore: stored response headers exceed %d bytes", maxCachedHeadersBytes)
	}

	// Fail closed when the caller supplies a fingerprint but the stored
	// row has NULL (legacy pre-fingerprint rows, or a writer that omitted
	// it): replaying without a body match would disable the mismatch
	// guard the fingerprint feature exists to provide.
	if fingerprint != nil && (storedFP == nil || !bytes.Equal(storedFP, fingerprint)) {
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
			return nil, false, redact.WrapError("pgstore: unmarshal headers", err)
		}
	}

	resp := idempotency.CachedResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}
	if err := idempotency.ValidateCachedResponse(resp); err != nil {
		return nil, false, redact.WrapError("pgstore: invalid cached response", err)
	}
	return &resp, false, nil
}

// Set replaces the lock row with the cached response. Returns
// [idempotency.ErrLockLost] if the caller's token no longer matches the
// current row's owner_token (TTL expired and another caller acquired).
// Returns [idempotency.ErrInvalidTTL] when ttl <= 0 — the interval cast
// would otherwise round sub-second values to "0 seconds" and create a
// row that's already expired before any consumer can read it.
func (s *Store) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	ctx, span := s.startSpan(ctx, "idempotency.Set")
	defer span.End()
	err := s.doSet(ctx, key, token, resp, ttl)
	recordResult(span, err)
	return err
}

func (s *Store) doSet(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := idempotency.ValidateStorageKey(key); err != nil {
		return err
	}
	if ttl <= 0 {
		return idempotency.ErrInvalidTTL
	}
	if err := idempotency.ValidateCachedResponse(resp); err != nil {
		return err
	}
	headersJSON, err := json.Marshal(resp.Headers)
	if err != nil {
		return redact.WrapError("pgstore: marshal headers", err)
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
		return redact.WrapError("pgstore: set", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return redact.WrapError("pgstore: set rows affected", err)
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
func (s *Store) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	ctx, span := s.startSpan(ctx, "idempotency.TryLock")
	defer span.End()
	token, mismatch, ok, err := s.doTryLock(ctx, key, fingerprint, ttl)
	recordResult(span, err)
	return token, mismatch, ok, err
}

func (s *Store) doTryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if err := s.ready(); err != nil {
		return "", false, false, err
	}
	if err := idempotency.ValidateStorageKey(key); err != nil {
		return "", false, false, err
	}
	if ttl <= 0 {
		return "", false, false, idempotency.ErrInvalidTTL
	}
	token, err := idempotency.GenerateToken()
	if err != nil {
		return "", false, false, err
	}

	// Atomic acquire + fingerprint observe via RETURNING. On conflict we
	// only overwrite when the row is expired; otherwise columns keep prior
	// values and RETURNING still yields the live owner_token/fingerprint so
	// there is no second-statement TOCTOU window.
	query := fmt.Sprintf(
		`INSERT INTO %s (key, owner_token, fingerprint, expires_at)
		 VALUES ($1, $2, $3, now() + $4::interval)
		 ON CONFLICT (key) DO UPDATE SET
		   owner_token   = CASE WHEN %s.expires_at <= now() THEN EXCLUDED.owner_token   ELSE %s.owner_token   END,
		   fingerprint   = CASE WHEN %s.expires_at <= now() THEN EXCLUDED.fingerprint   ELSE %s.fingerprint   END,
		   expires_at    = CASE WHEN %s.expires_at <= now() THEN EXCLUDED.expires_at    ELSE %s.expires_at    END,
		   status_code   = CASE WHEN %s.expires_at <= now() THEN NULL                   ELSE %s.status_code   END,
		   headers       = CASE WHEN %s.expires_at <= now() THEN NULL                   ELSE %s.headers       END,
		   response_body = CASE WHEN %s.expires_at <= now() THEN NULL                   ELSE %s.response_body END
		 RETURNING owner_token, fingerprint`,
		s.table,
		s.table, s.table,
		s.table, s.table,
		s.table, s.table,
		s.table, s.table,
		s.table, s.table,
		s.table, s.table,
	)

	var returnedToken string
	var storedFP []byte
	err = s.db.QueryRowContext(ctx, query, key, token, fingerprint, intervalSeconds(ttl)).Scan(&returnedToken, &storedFP)
	if err != nil {
		return "", false, false, redact.WrapError("pgstore: lock", err)
	}
	if returnedToken == token {
		return token, false, true, nil
	}
	// Live lock owned by someone else — fingerprint compare is atomic with
	// the observe above (same RETURNING row).
	if fingerprint != nil && (storedFP == nil || !bytes.Equal(storedFP, fingerprint)) {
		return "", true, false, nil
	}
	return "", false, false, nil
}

// Unlock releases the processing lock. Best-effort: token mismatch is a
// silent no-op (returns nil) because Unlock runs in panic-cleanup paths
// where surfacing lock-loss would mask the original panic.
//
// We delete only "still locked" rows — discriminated by
// `status_code IS NULL`, matching the symmetric check in Get
// (audit FR-030). Using `response_body IS NULL` here would mean
// Unlock could destroy a successfully-cached empty-body response
// (HTTP 204) on a panic-during-second-request path.
func (s *Store) Unlock(ctx context.Context, key, token string) error {
	ctx, span := s.startSpan(ctx, "idempotency.Unlock")
	defer span.End()
	err := s.doUnlock(ctx, key, token)
	recordResult(span, err)
	return err
}

func (s *Store) doUnlock(ctx context.Context, key, token string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := idempotency.ValidateStorageKey(key); err != nil {
		return err
	}
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE key = $1 AND owner_token = $2 AND status_code IS NULL`,
		s.table,
	)
	_, err := s.db.ExecContext(ctx, query, key, token)
	if err != nil {
		return redact.WrapError("pgstore: unlock", err)
	}
	return nil
}

func (s *Store) ready() error {
	if s == nil || s.db == nil || s.table == "" || !validTableName.MatchString(s.table) {
		return idempotency.ErrInvalidStore
	}
	return nil
}

// DeleteExpired removes all expired entries. Call this periodically
// (e.g., via cron) to prevent table bloat.
func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	ctx, span := s.startSpan(ctx, "idempotency.DeleteExpired")
	defer span.End()
	n, err := s.doDeleteExpired(ctx)
	recordResult(span, err)
	return n, err
}

func (s *Store) doDeleteExpired(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	query := fmt.Sprintf(`DELETE FROM %s WHERE expires_at <= now()`, s.table)
	result, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return 0, redact.WrapError("pgstore: delete expired", err)
	}
	return result.RowsAffected()
}
