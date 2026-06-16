package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/security/v2/apikey"
)

// uniqueViolation is the SQLSTATE code Postgres returns when a unique
// constraint (here the primary key) is violated.
const uniqueViolation = "23505"

// querier is the narrow slice of [pgxpool.Pool] the Store depends on. Keeping
// it unexported lets the store be exercised with a fake in unit tests without
// a live database, while New still accepts a concrete *pgxpool.Pool so the
// exported surface is unchanged.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Compile-time assertion that *pgxpool.Pool satisfies the querier seam.
var _ querier = (*pgxpool.Pool)(nil)

// Store is a pgx-backed [apikey.Repository].
type Store struct {
	pool querier
}

// Compile-time assertion that Store implements the repository contract.
var _ apikey.Repository = (*Store)(nil)

// New returns a Store. It panics on a nil pool — a missing pool is a
// fail-fast misconfiguration that would otherwise surface only at the
// first query.
func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		panic("apikey/postgres: New pool must not be nil")
	}
	return &Store{pool: pool}
}

// Insert implements [apikey.Repository]. A duplicate id returns a conflict
// error so callers can distinguish it from other failures.
func (s *Store) Insert(ctx context.Context, key apikey.Key) error {
	scopes, err := json.Marshal(key.Scopes)
	if err != nil {
		return redact.WrapError("apikey/postgres: marshal scopes", err)
	}
	const q = `
INSERT INTO api_keys
(id, prefix, hash, kind, scopes, owner, expires_at, revoked_at, rotated_from, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
	_, err = s.pool.Exec(ctx, q,
		key.ID, key.Prefix, key.Hash[:], string(key.Kind), scopes, key.Owner,
		nullTime(key.ExpiresAt), nullTime(key.RevokedAt), key.RotatedFrom, key.CreatedAt.UTC(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return apperror.NewConflictWithCause("apikey: key already exists", err)
		}
		return redact.WrapError("apikey/postgres: insert", err)
	}
	return nil
}

// FindByID implements [apikey.Repository]. Returns an [apperror] NotFound
// when no row matches.
func (s *Store) FindByID(ctx context.Context, id string) (apikey.Key, error) {
	const q = `
SELECT id, prefix, hash, kind, scopes, owner, expires_at, revoked_at, rotated_from, created_at
FROM api_keys
WHERE id = $1`
	key, err := scanKey(s.pool.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apikey.Key{}, apperror.NewNotFound("api key", id)
		}
		return apikey.Key{}, redact.WrapError("apikey/postgres: find by id", err)
	}
	return key, nil
}

// Revoke implements [apikey.Repository]. It is idempotent: an already-revoked
// key keeps its original revocation time. Returns NotFound when absent.
//
// A zero at is mapped to NULL (via [nullTime]) rather than the 0001-01-01
// sentinel, so the key reads back active — matching
// [apikey.MemoryRepository], which leaves RevokedAt zero for a zero at.
func (s *Store) Revoke(ctx context.Context, id string, at time.Time) error {
	const q = `
UPDATE api_keys
SET revoked_at = $2
WHERE id = $1 AND revoked_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, id, nullTime(at))
	if err != nil {
		return redact.WrapError("apikey/postgres: revoke", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// No row updated: either the key is missing or already revoked. Probe
	// existence so callers get a precise NotFound only when truly absent.
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM api_keys WHERE id = $1)", id).Scan(&exists); err != nil {
		return redact.WrapError("apikey/postgres: revoke existence check", err)
	}
	if !exists {
		return apperror.NewNotFound("api key", id)
	}
	return nil
}

// ListByOwner implements [apikey.Repository].
func (s *Store) ListByOwner(ctx context.Context, owner string) ([]apikey.Key, error) {
	const q = `
SELECT id, prefix, hash, kind, scopes, owner, expires_at, revoked_at, rotated_from, created_at
FROM api_keys
WHERE owner = $1
ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, owner)
	if err != nil {
		return nil, redact.WrapError("apikey/postgres: list by owner", err)
	}
	defer rows.Close()

	var out []apikey.Key
	for rows.Next() {
		key, scanErr := scanKey(rows)
		if scanErr != nil {
			return nil, redact.WrapError("apikey/postgres: list scan", scanErr)
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, redact.WrapError("apikey/postgres: list iterate", err)
	}
	return out, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanKey(s scannable) (apikey.Key, error) {
	var (
		key       apikey.Key
		hash      []byte
		kind      string
		scopesRaw []byte
		expiresAt *time.Time
		revokedAt *time.Time
	)
	if err := s.Scan(
		&key.ID, &key.Prefix, &hash, &kind, &scopesRaw, &key.Owner,
		&expiresAt, &revokedAt, &key.RotatedFrom, &key.CreatedAt,
	); err != nil {
		return apikey.Key{}, err
	}
	if len(hash) != len(key.Hash) {
		return apikey.Key{}, apperror.NewOperationFailed("apikey/postgres: stored hash has unexpected length")
	}
	copy(key.Hash[:], hash)
	key.Kind = apikey.Kind(kind)
	if len(scopesRaw) > 0 {
		if err := json.Unmarshal(scopesRaw, &key.Scopes); err != nil {
			return apikey.Key{}, redact.WrapError("apikey/postgres: unmarshal scopes", err)
		}
	}
	key.CreatedAt = key.CreatedAt.UTC()
	if expiresAt != nil {
		key.ExpiresAt = expiresAt.UTC()
	}
	if revokedAt != nil {
		key.RevokedAt = revokedAt.UTC()
	}
	return key, nil
}

// nullTime maps the zero time to NULL so "no expiry" / "not revoked" round
// trips cleanly instead of persisting a sentinel timestamp.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
