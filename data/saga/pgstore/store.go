package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/runtime/v2/saga"
)

// ErrConcurrentUpdate is returned by Put when the row's updated_at
// has advanced since the Instance the caller read. Surfaces the
// optimistic-concurrency conflict so the executor can re-read state
// instead of overwriting a sibling replica's progress.
var ErrConcurrentUpdate = errors.New("pgstore: saga instance updated concurrently")

var validIdent = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// Store implements [saga.StateStore] against Postgres.
type Store struct {
	db    *sql.DB
	table string
}

// Option configures the Store.
type Option func(*Store)

// WithTableName overrides the default "saga_instances".
func WithTableName(name string) Option {
	if !validIdent.MatchString(name) {
		panic("pgstore: WithTableName requires safe SQL identifier")
	}
	return func(s *Store) { s.table = name }
}

// New constructs a Store backed by db. Panics on nil db.
func New(db *sql.DB, opts ...Option) *Store {
	if db == nil {
		panic("pgstore: New requires non-nil *sql.DB")
	}
	s := &Store{db: db, table: "saga_instances"}
	for _, opt := range opts {
		if opt == nil {
			panic("pgstore: option must not be nil")
		}
		opt(s)
	}
	return s
}

// Put implements [saga.StateStore]. Routes to two distinct SQL paths
// based on whether the caller's Instance carries a non-zero UpdatedAt:
//
//   - UpdatedAt zero → INSERT ... ON CONFLICT (id) DO NOTHING. A row
//     with the same ID already existing surfaces as ErrConcurrentUpdate
//     (the executor will re-read and re-decide). NEVER overwrites.
//   - UpdatedAt non-zero → UPDATE ... WHERE updated_at = $old. Strict
//     optimistic concurrency: any other replica's write since the
//     caller read state surfaces as ErrConcurrentUpdate.
//
// Splitting the paths closes the v2.0 blocker B2: a single
// INSERT…ON CONFLICT DO UPDATE WHERE (updated_at=$9 OR $9 IS NULL)
// had a `IS NULL` escape that let a misbehaving caller bypass the
// concurrency check by passing a fresh Instance{} with an existing
// ID. Two-path implementation has no such escape.
func (s *Store) Put(ctx context.Context, inst saga.Instance) error {
	if inst.ID == "" {
		return errors.New("pgstore: Put requires Instance.ID")
	}
	compensatedJSON, err := json.Marshal(inst.Compensated)
	if err != nil {
		return redact.WrapError("pgstore: marshal compensated", err)
	}
	resultsJSON, err := json.Marshal(inst.StepResults)
	if err != nil {
		return redact.WrapError("pgstore: marshal step_results", err)
	}

	if inst.UpdatedAt.IsZero() {
		return s.putInsertOnly(ctx, inst, compensatedJSON, resultsJSON)
	}
	return s.putUpdateOptimistic(ctx, inst, compensatedJSON, resultsJSON)
}

// putInsertOnly handles first-write semantics: insert when the row
// doesn't exist; surface ErrConcurrentUpdate if it does (the row was
// written by another replica between the caller's decision and now).
func (s *Store) putInsertOnly(ctx context.Context, inst saga.Instance, compensatedJSON, resultsJSON []byte) error {
	query := fmt.Sprintf(`INSERT INTO %s
		(id, definition, state, current_step, compensated, input, step_results, last_error, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8, now())
		ON CONFLICT (id) DO NOTHING`,
		s.table)
	res, err := s.db.ExecContext(ctx, query,
		inst.ID, inst.Definition, string(inst.State), inst.CurrentStep,
		string(compensatedJSON), inst.Input, string(resultsJSON), inst.LastError,
	)
	if err != nil {
		return redact.WrapError("pgstore: Put insert", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return redact.WrapError("pgstore: Put insert rows", err)
	}
	if rows == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

// putUpdateOptimistic handles state-advance semantics: UPDATE only
// when updated_at still matches what the caller read. No NULL escape.
func (s *Store) putUpdateOptimistic(ctx context.Context, inst saga.Instance, compensatedJSON, resultsJSON []byte) error {
	query := fmt.Sprintf(`UPDATE %s SET
		  state         = $1,
		  current_step  = $2,
		  compensated   = $3::jsonb,
		  step_results  = $4::jsonb,
		  last_error    = $5,
		  updated_at    = now()
		WHERE id = $6 AND updated_at = $7`,
		s.table)
	res, err := s.db.ExecContext(ctx, query,
		string(inst.State), inst.CurrentStep,
		string(compensatedJSON), string(resultsJSON), inst.LastError,
		inst.ID, inst.UpdatedAt,
	)
	if err != nil {
		return redact.WrapError("pgstore: Put update", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return redact.WrapError("pgstore: Put update rows", err)
	}
	if rows == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

// Get implements [saga.StateStore].
func (s *Store) Get(ctx context.Context, id string) (saga.Instance, error) {
	query := fmt.Sprintf(`SELECT id, definition, state, current_step,
		compensated, input, step_results, last_error, created_at, updated_at
		FROM %s WHERE id = $1`, s.table)
	row := s.db.QueryRowContext(ctx, query, id)
	return s.scanRow(row)
}

// ListResumable implements [saga.StateStore].
func (s *Store) ListResumable(ctx context.Context, olderThan time.Duration) ([]saga.Instance, error) {
	var (
		query string
		args  []any
	)
	if olderThan > 0 {
		query = fmt.Sprintf(`SELECT id, definition, state, current_step,
			compensated, input, step_results, last_error, created_at, updated_at
			FROM %s
			WHERE state IN ('pending', 'running', 'compensating')
			  AND updated_at < now() - $1::interval
			ORDER BY updated_at ASC`, s.table)
		args = []any{fmt.Sprintf("%d milliseconds", olderThan.Milliseconds())}
	} else {
		query = fmt.Sprintf(`SELECT id, definition, state, current_step,
			compensated, input, step_results, last_error, created_at, updated_at
			FROM %s
			WHERE state IN ('pending', 'running', 'compensating')
			ORDER BY updated_at ASC`, s.table)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, redact.WrapError("pgstore: ListResumable", err)
	}
	defer rows.Close()
	var out []saga.Instance
	for rows.Next() {
		inst, err := s.scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, redact.WrapError("pgstore: ListResumable rows", err)
	}
	return out, nil
}

// Delete implements [saga.StateStore]. Idempotent.
func (s *Store) Delete(ctx context.Context, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.table)
	_, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return redact.WrapError("pgstore: Delete", err)
	}
	return nil
}

// scannable is the minimal contract both *sql.Row and *sql.Rows
// satisfy so scanRow handles both.
type scannable interface {
	Scan(dest ...any) error
}

func (s *Store) scanRow(row scannable) (saga.Instance, error) {
	var (
		inst            saga.Instance
		stateStr        string
		compensatedJSON []byte
		resultsJSON     []byte
	)
	err := row.Scan(
		&inst.ID, &inst.Definition, &stateStr, &inst.CurrentStep,
		&compensatedJSON, &inst.Input, &resultsJSON, &inst.LastError,
		&inst.CreatedAt, &inst.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return saga.Instance{}, saga.ErrInstanceNotFound
		}
		return saga.Instance{}, redact.WrapError("pgstore: scan", err)
	}
	inst.State = saga.State(stateStr)
	if len(compensatedJSON) > 0 {
		if err := json.Unmarshal(compensatedJSON, &inst.Compensated); err != nil {
			return saga.Instance{}, redact.WrapError("pgstore: unmarshal compensated", err)
		}
	}
	if len(resultsJSON) > 0 {
		if err := json.Unmarshal(resultsJSON, &inst.StepResults); err != nil {
			return saga.Instance{}, redact.WrapError("pgstore: unmarshal step_results", err)
		}
	}
	return inst, nil
}
