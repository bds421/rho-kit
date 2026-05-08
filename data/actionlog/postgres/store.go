// Package postgres is the pgx-backed [actionlog.Store]. v2 dropped GORM
// and runs sqlc-style hand-written queries against a *pgxpool.Pool.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/data/actionlog"
)

const defaultLimit = 100

// uniqueViolation is the SQLSTATE code Postgres returns when a unique
// constraint is violated. Used to translate (tenant_id, seq) collisions
// from concurrent appends back into a typed error the caller can retry.
const uniqueViolation = "23505"

// Store is a pgx-backed [actionlog.Store]. The append path holds a
// per-tenant advisory lock (pg_advisory_xact_lock) plus SELECT FOR
// UPDATE on the tenant's latest row so concurrent appends serialise
// even when the tenant has zero rows yet — the lock is the only thing
// preventing two concurrent first-appends from both racing to seq=1.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store. Panics on a nil pool — fail fast at startup so
// the failure is visible at boot rather than at first append.
func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		panic("actionlog/postgres: pool must not be nil")
	}
	return &Store{pool: pool}
}

// AppendChained runs build inside a transaction that holds a per-tenant
// advisory lock plus SELECT FOR UPDATE on the latest row for tenantID,
// persisting the resulting entry under the same lock so concurrent
// appends serialise — including the tenant's first append, where there
// is no row yet for SELECT FOR UPDATE to lock.
func (s *Store) AppendChained(ctx context.Context, tenantID string, build func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error)) (actionlog.Entry, error) {
	if tenantID == "" {
		return actionlog.Entry{}, actionlog.ErrInvalidEntry
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// pg_advisory_xact_lock serialises concurrent first-appends for the
	// same tenant: SELECT FOR UPDATE has nothing to lock when the tenant
	// has zero rows, so two concurrent first-append calls would otherwise
	// both build seq=1 and one would fail the (tenant_id, seq) unique
	// constraint. The lock is released at commit/rollback, so it never
	// escapes this transaction.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", tenantID); err != nil {
		return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: advisory lock: %w", err)
	}

	prev, prevSeq, err := selectLatestForUpdate(ctx, tx, tenantID)
	if err != nil {
		return actionlog.Entry{}, err
	}

	entry, err := build(prev, prevSeq)
	if err != nil {
		return actionlog.Entry{}, err
	}

	if err := insertEntry(ctx, tx, entry); err != nil {
		return actionlog.Entry{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: commit: %w", err)
	}
	return entry, nil
}

// Get returns the entry by id. Returns [actionlog.ErrNotFound] when no
// row matches.
func (s *Store) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	const q = `
SELECT id, tenant_id, actor, action, resource, outcome, reason,
       metadata, occurred_at, signature_key_id, seq, prev_hash, signature
FROM action_log_entries
WHERE id = $1`
	row := s.pool.QueryRow(ctx, q, id)
	entry, err := scanEntry(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return actionlog.Entry{}, actionlog.ErrNotFound
		}
		return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: get: %w", err)
	}
	return entry, nil
}

// List returns entries matching q, ordered by occurred_at DESC, id DESC.
func (s *Store) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	// Build the WHERE clause incrementally. Using positional placeholders
	// keeps the parameter list aligned with the SQL string and avoids any
	// risk of operator-supplied filter values being interpolated.
	args := make([]any, 0, 5)
	clauses := make([]string, 0, 5)
	add := func(expr string, val any) {
		args = append(args, val)
		clauses = append(clauses, fmt.Sprintf(expr, len(args)))
	}
	if q.TenantID != "" {
		add("tenant_id = $%d", q.TenantID)
	}
	if q.Actor != "" {
		add("actor = $%d", q.Actor)
	}
	if q.Action != "" {
		add("action = $%d", q.Action)
	}
	if !q.Since.IsZero() {
		add("occurred_at >= $%d", q.Since.UTC())
	}
	if !q.Until.IsZero() {
		add("occurred_at <= $%d", q.Until.UTC())
	}

	sql := `SELECT id, tenant_id, actor, action, resource, outcome, reason,
       metadata, occurred_at, signature_key_id, seq, prev_hash, signature
FROM action_log_entries`
	if len(clauses) > 0 {
		sql += " WHERE " + joinAnd(clauses)
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("actionlog/postgres: list: %w", err)
	}
	defer rows.Close()

	out := make([]actionlog.Entry, 0, limit)
	for rows.Next() {
		entry, scanErr := scanEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("actionlog/postgres: list scan: %w", scanErr)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("actionlog/postgres: list iterate: %w", err)
	}
	return out, nil
}

// ListByTenantSeq returns every entry for tenantID ordered by Seq ASC.
// No limit is applied — VerifyChain needs the full chain.
func (s *Store) ListByTenantSeq(ctx context.Context, tenantID string) ([]actionlog.Entry, error) {
	const q = `
SELECT id, tenant_id, actor, action, resource, outcome, reason,
       metadata, occurred_at, signature_key_id, seq, prev_hash, signature
FROM action_log_entries
WHERE tenant_id = $1
ORDER BY seq ASC`
	rows, err := s.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("actionlog/postgres: list by tenant seq: %w", err)
	}
	defer rows.Close()

	var out []actionlog.Entry
	for rows.Next() {
		entry, scanErr := scanEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("actionlog/postgres: list by tenant seq scan: %w", scanErr)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("actionlog/postgres: list by tenant seq iterate: %w", err)
	}
	return out, nil
}

// scannable abstracts pgx.Row and pgx.Rows so scanEntry handles both
// QueryRow (single-row) and Query (multi-row) cleanly.
type scannable interface {
	Scan(dest ...any) error
}

func scanEntry(s scannable) (actionlog.Entry, error) {
	var (
		e        actionlog.Entry
		outcome  string
		metaRaw  []byte
		resource string
		reason   string
	)
	if err := s.Scan(
		&e.ID, &e.TenantID, &e.Actor, &e.Action, &resource, &outcome, &reason,
		&metaRaw, &e.OccurredAt, &e.SignatureKeyID, &e.Seq, &e.PrevHash, &e.Signature,
	); err != nil {
		return actionlog.Entry{}, err
	}
	e.Resource = resource
	e.Outcome = actionlog.Outcome(outcome)
	e.Reason = reason
	e.OccurredAt = e.OccurredAt.UTC()
	if len(metaRaw) > 0 {
		var meta map[string]any
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: unmarshal metadata: %w", err)
		}
		e.Metadata = meta
	}
	return e, nil
}

func selectLatestForUpdate(ctx context.Context, tx pgx.Tx, tenantID string) (actionlog.Entry, int64, error) {
	const q = `
SELECT id, tenant_id, actor, action, resource, outcome, reason,
       metadata, occurred_at, signature_key_id, seq, prev_hash, signature
FROM action_log_entries
WHERE tenant_id = $1
ORDER BY seq DESC
LIMIT 1
FOR UPDATE`
	row := tx.QueryRow(ctx, q, tenantID)
	entry, err := scanEntry(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return actionlog.Entry{}, 0, nil
		}
		return actionlog.Entry{}, 0, fmt.Errorf("actionlog/postgres: select latest: %w", err)
	}
	return entry, entry.Seq, nil
}

func insertEntry(ctx context.Context, tx pgx.Tx, e actionlog.Entry) error {
	var metaRaw []byte
	if len(e.Metadata) > 0 {
		b, err := json.Marshal(e.Metadata)
		if err != nil {
			return fmt.Errorf("actionlog/postgres: marshal metadata: %w", err)
		}
		metaRaw = b
	}
	const q = `
INSERT INTO action_log_entries
(id, tenant_id, actor, action, resource, outcome, reason, metadata,
 occurred_at, signature_key_id, seq, prev_hash, signature)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`
	if _, err := tx.Exec(ctx, q,
		e.ID, e.TenantID, e.Actor, e.Action, e.Resource, string(e.Outcome), e.Reason, metaRaw,
		e.OccurredAt.UTC(), e.SignatureKeyID, e.Seq, e.PrevHash, e.Signature,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return fmt.Errorf("actionlog/postgres: append: concurrent seq collision: %w", err)
		}
		return fmt.Errorf("actionlog/postgres: append: %w", err)
	}
	return nil
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " AND " + p
	}
	return out
}
