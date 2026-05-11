// Package postgres is the pgx-backed [approval.Store]. v2 dropped GORM
// and runs hand-written queries against a *pgxpool.Pool.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/data/v2/approval"
)

const defaultLimit = 100

// Store is a pgx-backed [approval.Store]. State transitions run inside a
// transaction with SELECT FOR UPDATE so concurrent decisions on the same
// request serialise on the row lock.
type Store struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock overrides the wall-clock used for the auto-expire branch
// inside Decide. Tests use this to make the late-approval branch
// deterministic. Panics on a nil fn.
func WithClock(fn func() time.Time) Option {
	if fn == nil {
		panic("approval/postgres: WithClock requires a non-nil function")
	}
	return func(s *Store) { s.clock = fn }
}

// New returns a Store backed by pool. Panics on a nil pool.
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	if pool == nil {
		panic("approval/postgres: pool must not be nil")
	}
	s := &Store{pool: pool, clock: time.Now}
	for _, o := range opts {
		if o == nil {
			panic("approval/postgres: option must not be nil")
		}
		o(s)
	}
	return s
}

// Create persists a new request in StatePending.
func (s *Store) Create(ctx context.Context, r approval.Request) (approval.Request, error) {
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	if err := approval.ValidateForCreate(r, s.clock()); err != nil {
		return approval.Request{}, err
	}
	r.State = approval.StatePending
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.clock()
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.ExpiresAt = r.ExpiresAt.UTC()

	const q = `
INSERT INTO approval_requests
(id, tenant_id, actor, action, resource, payload, state, decided_by,
 decided_at, reason, created_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	if _, err := s.pool.Exec(ctx, q,
		r.ID, r.TenantID, r.Actor, r.Action, r.Resource, payloadBytes(r.Payload),
		string(r.State), r.DecidedBy, nullableTime(r.DecidedAt), r.Reason,
		r.CreatedAt, r.ExpiresAt,
	); err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: create: %w", err)
	}
	return r, nil
}

// Get returns the request by id.
func (s *Store) Get(ctx context.Context, id string) (approval.Request, error) {
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	const q = `
SELECT id, tenant_id, actor, action, resource, payload, state, decided_by,
       decided_at, reason, created_at, expires_at
FROM approval_requests
WHERE id = $1`
	row := s.pool.QueryRow(ctx, q, id)
	out, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return approval.Request{}, approval.ErrNotFound
		}
		return approval.Request{}, fmt.Errorf("approval/postgres: get: %w", err)
	}
	return out, nil
}

// List returns matching requests newest-first. Returns
// [approval.ErrQueryTenantRequired] when the caller did not set
// [approval.Query.TenantID] or opt into AllTenants — see audit
// FR-053 for why cross-tenant listings must be explicit. Returns
// [approval.ErrQueryScopeConflict] when both scope modes are set.
func (s *Store) List(ctx context.Context, q approval.Query) ([]approval.Request, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	args := make([]any, 0, 6)
	clauses := make([]string, 0, 6)
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
	if q.State != "" {
		add("state = $%d", string(q.State))
	}
	if !q.Since.IsZero() {
		add("created_at >= $%d", q.Since.UTC())
	}
	if !q.Until.IsZero() {
		add("created_at <= $%d", q.Until.UTC())
	}

	sql := `SELECT id, tenant_id, actor, action, resource, payload, state, decided_by,
       decided_at, reason, created_at, expires_at
FROM approval_requests`
	if len(clauses) > 0 {
		sql += " WHERE " + joinAnd(clauses)
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("approval/postgres: list: %w", err)
	}
	defer rows.Close()

	out := make([]approval.Request, 0, limit)
	for rows.Next() {
		req, scanErr := scanRequest(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("approval/postgres: list scan: %w", scanErr)
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("approval/postgres: list iterate: %w", err)
	}
	return out, nil
}

// Decide records an approver's decision atomically.
//
// Mirrors data/approval/memory: idempotent for the same decision,
// refuses to flip a recorded decision, refuses to move out of a
// terminal state, auto-expires past-deadline pending requests.
func (s *Store) Decide(ctx context.Context, id, decidedBy, reason string, approve bool) (approval.Request, error) {
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	if err := approval.ValidateDecision(decidedBy); err != nil {
		return approval.Request{}, err
	}
	if err := approval.ValidateReason(reason); err != nil {
		return approval.Request{}, err
	}
	target := approval.StateApproved
	if !approve {
		target = approval.StateRejected
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const selectForUpdate = `
SELECT id, tenant_id, actor, action, resource, payload, state, decided_by,
       decided_at, reason, created_at, expires_at
FROM approval_requests
WHERE id = $1
FOR UPDATE`
	row := tx.QueryRow(ctx, selectForUpdate, id)
	r, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return approval.Request{}, approval.ErrNotFound
		}
		return approval.Request{}, fmt.Errorf("approval/postgres: decide select: %w", err)
	}

	now := s.clock().UTC()

	// Auto-expire branch: persist the state flip to expired AND surface
	// ErrInvalidTransition to the caller. We commit the state change and
	// stash the error so the next Decide cannot re-flip the row.
	if r.State == approval.StatePending && !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		const expireSQL = `UPDATE approval_requests SET state = $1, decided_at = $2 WHERE id = $3`
		if _, err := tx.Exec(ctx, expireSQL, string(approval.StateExpired), now, r.ID); err != nil {
			return approval.Request{}, fmt.Errorf("approval/postgres: expire: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return approval.Request{}, fmt.Errorf("approval/postgres: commit: %w", err)
		}
		return approval.Request{}, fmt.Errorf("%w: request expired", approval.ErrInvalidTransition)
	}

	if r.State == target {
		// Idempotent: refresh decider/reason metadata.
		const refreshSQL = `UPDATE approval_requests SET decided_by = $1, reason = $2 WHERE id = $3`
		if _, err := tx.Exec(ctx, refreshSQL, decidedBy, reason, r.ID); err != nil {
			return approval.Request{}, fmt.Errorf("approval/postgres: refresh: %w", err)
		}
		r.DecidedBy = decidedBy
		r.Reason = reason
		if err := tx.Commit(ctx); err != nil {
			return approval.Request{}, fmt.Errorf("approval/postgres: commit: %w", err)
		}
		return r, nil
	}

	if r.State.IsTerminal() {
		return approval.Request{}, fmt.Errorf("%w: cannot transition out of terminal state", approval.ErrInvalidTransition)
	}

	if r.State == approval.StateApproved || r.State == approval.StateRejected {
		return approval.Request{}, fmt.Errorf("%w: cannot flip decision once recorded", approval.ErrInvalidTransition)
	}

	const decideSQL = `UPDATE approval_requests SET state = $1, decided_by = $2, reason = $3, decided_at = $4 WHERE id = $5`
	if _, err := tx.Exec(ctx, decideSQL, string(target), decidedBy, reason, now, r.ID); err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: decide update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: commit: %w", err)
	}
	r.State = target
	r.DecidedBy = decidedBy
	r.Reason = reason
	r.DecidedAt = now
	return r, nil
}

// MarkExecuted moves an approved request to executed.
func (s *Store) MarkExecuted(ctx context.Context, id string) (approval.Request, error) {
	if err := s.ready(); err != nil {
		return approval.Request{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const selectForUpdate = `
SELECT id, tenant_id, actor, action, resource, payload, state, decided_by,
       decided_at, reason, created_at, expires_at
FROM approval_requests
WHERE id = $1
FOR UPDATE`
	row := tx.QueryRow(ctx, selectForUpdate, id)
	r, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return approval.Request{}, approval.ErrNotFound
		}
		return approval.Request{}, fmt.Errorf("approval/postgres: mark executed select: %w", err)
	}

	if r.State == approval.StateExecuted {
		if err := tx.Commit(ctx); err != nil {
			return approval.Request{}, fmt.Errorf("approval/postgres: commit: %w", err)
		}
		return r, nil
	}
	if r.State != approval.StateApproved {
		return approval.Request{}, fmt.Errorf("%w: request is not approved", approval.ErrInvalidTransition)
	}

	const updateSQL = `UPDATE approval_requests SET state = $1 WHERE id = $2`
	if _, err := tx.Exec(ctx, updateSQL, string(approval.StateExecuted), r.ID); err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: mark executed update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: commit: %w", err)
	}
	r.State = approval.StateExecuted
	return r, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanRequest(s scannable) (approval.Request, error) {
	var (
		out         approval.Request
		state       string
		payloadRaw  []byte
		decidedAtNT *time.Time
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &out.Actor, &out.Action, &out.Resource, &payloadRaw,
		&state, &out.DecidedBy, &decidedAtNT, &out.Reason, &out.CreatedAt, &out.ExpiresAt,
	); err != nil {
		return approval.Request{}, err
	}
	out.State = approval.State(state)
	out.CreatedAt = out.CreatedAt.UTC()
	out.ExpiresAt = out.ExpiresAt.UTC()
	if decidedAtNT != nil {
		out.DecidedAt = decidedAtNT.UTC()
	}
	if len(payloadRaw) > 0 {
		out.Payload = json.RawMessage(payloadRaw)
	}
	return out, nil
}

func payloadBytes(p json.RawMessage) []byte {
	if len(p) == 0 {
		return nil
	}
	return []byte(p)
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func (s *Store) ready() error {
	if s == nil || s.pool == nil || s.clock == nil {
		return approval.ErrInvalidStore
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
