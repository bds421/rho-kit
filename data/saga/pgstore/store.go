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

// ErrConcurrentUpdate is returned by Put when the write affected no
// row: either an insert lost the ON CONFLICT race (another writer
// created the row first) or an update found the row already gone (a
// concurrent Delete). It signals the caller should re-read state rather
// than assume the write landed.
var ErrConcurrentUpdate = errors.New("pgstore: saga instance updated concurrently")

// ErrInvalidStore is returned by Store methods invoked on a nil receiver
// or a zero-value &Store{} that bypassed [New] (e.g. db is nil). It
// mirrors the kit-wide invalid-receiver convention used by sibling
// backends (queue.ErrInvalidQueue, ratelimit.ErrInvalidLimiter): a
// method call on an uninitialized handle returns a sentinel rather than
// panicking with a nil-pointer dereference.
var ErrInvalidStore = errors.New("pgstore: store is not initialized")

var validIdent = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// Store implements [saga.StateStore] against Postgres.
type Store struct {
	db    *sql.DB
	table string
}

// ready reports whether s is usable. A nil receiver or a zero-value
// &Store{} that skipped [New] (nil db, empty table) is not usable and
// yields [ErrInvalidStore]. [New] always sets both fields, so a
// constructed Store always passes.
func (s *Store) ready() error {
	if s == nil || s.db == nil || s.table == "" {
		return ErrInvalidStore
	}
	return nil
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
//   - UpdatedAt zero → INSERT ... ON CONFLICT (id) DO NOTHING. This is
//     the first write of a fresh instance; a row with the same ID
//     already existing surfaces as ErrConcurrentUpdate (the executor
//     will re-read and re-decide). NEVER overwrites.
//   - UpdatedAt non-zero → UPDATE the existing row in place by ID. The
//     caller has already read the instance via Get, so this overwrites
//     its mutable columns, matching the "writes (or overwrites)"
//     contract of [saga.StateStore.Put]. A vanished row (concurrent
//     Delete) surfaces as ErrConcurrentUpdate.
//
// The UPDATE path does NOT gate on updated_at: the executor reads an
// Instance once and then Puts repeatedly without re-reading, so its
// in-memory UpdatedAt is stale after the first write. See
// putUpdateOptimistic for the full rationale.
func (s *Store) Put(ctx context.Context, inst saga.Instance) error {
	if err := s.ready(); err != nil {
		return err
	}
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

// putUpdateOptimistic handles state-advance semantics: UPDATE the row
// in place by ID, overwriting its mutable columns.
//
// It deliberately does NOT gate on `updated_at` matching the caller's
// snapshot. The [saga.StateStore.Put] contract is "writes (or
// overwrites) the instance"; it carries no read-your-write token, and
// saga.DurableExecutor.executeInstance reads an Instance ONCE via Get
// and then calls Put repeatedly without re-reading. The server stamps a
// fresh updated_at on every write, so after the first Put the caller's
// in-memory UpdatedAt is stale by design. Gating on it would make every
// Put after the first match zero rows and fail every multi-step saga.
//
// A zero-row result here means the row vanished between the caller's Get
// and this Put (e.g. a concurrent Delete) — surfaced as
// ErrConcurrentUpdate so the executor does not silently no-op.
func (s *Store) putUpdateOptimistic(ctx context.Context, inst saga.Instance, compensatedJSON, resultsJSON []byte) error {
	query := fmt.Sprintf(`UPDATE %s SET
		  state         = $1,
		  current_step  = $2,
		  compensated   = $3::jsonb,
		  step_results  = $4::jsonb,
		  last_error    = $5,
		  updated_at    = now()
		WHERE id = $6`,
		s.table)
	res, err := s.db.ExecContext(ctx, query,
		string(inst.State), inst.CurrentStep,
		string(compensatedJSON), string(resultsJSON), inst.LastError,
		inst.ID,
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
	if err := s.ready(); err != nil {
		return saga.Instance{}, err
	}
	query := fmt.Sprintf(`SELECT id, definition, state, current_step,
		compensated, input, step_results, last_error, created_at, updated_at
		FROM %s WHERE id = $1`, s.table)
	row := s.db.QueryRowContext(ctx, query, id)
	return s.scanRow(row)
}

// ListResumable implements [saga.StateStore].
func (s *Store) ListResumable(ctx context.Context, olderThan time.Duration) ([]saga.Instance, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
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
	defer func() { _ = rows.Close() }()
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
	if err := s.ready(); err != nil {
		return err
	}
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.table)
	_, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return redact.WrapError("pgstore: Delete", err)
	}
	return nil
}

// DeleteTerminalBefore prunes terminal (completed / failed) saga
// instances whose updated_at is older than before, returning the number
// of rows removed. It is the retention sweep for this store: the
// executor leaves completed and failed instances in place (each carries
// input + per-step JSONB results), so without a periodic prune the table
// grows unbounded. Run it from a scheduled job — mirrors
// outbox.DeletePublishedBefore / DeleteFailedBefore and
// idempotency.DeleteExpired.
//
// Only terminal states are touched, so an in-flight (pending / running /
// compensating) instance is never collected even if its updated_at is
// stale; ResetStaleProcessing-style recovery via ListResumable stays
// intact. The partial index idx_saga_instances_terminal keeps the sweep
// O(rows-to-delete). Not part of [saga.StateStore]: it is a
// backend-specific extension, like the outbox store's prune methods, so
// adding it does not force the in-memory backend to implement retention.
func (s *Store) DeleteTerminalBefore(ctx context.Context, before time.Time) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE state IN ('completed', 'failed') AND updated_at < $1`,
		s.table,
	)
	res, err := s.db.ExecContext(ctx, query, before.UTC())
	if err != nil {
		return 0, redact.WrapError("pgstore: DeleteTerminalBefore", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, redact.WrapError("pgstore: DeleteTerminalBefore rows", err)
	}
	return n, nil
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
		inputBytes      []byte
		resultsJSON     []byte
	)
	// input is a nullable BYTEA: a saga started with no input stores SQL
	// NULL. database/sql cannot scan NULL into json.RawMessage directly
	// (it errors "unsupported Scan ... <nil> into *json.RawMessage"), so
	// scan through a plain []byte, which receives NULL as nil.
	err := row.Scan(
		&inst.ID, &inst.Definition, &stateStr, &inst.CurrentStep,
		&compensatedJSON, &inputBytes, &resultsJSON, &inst.LastError,
		&inst.CreatedAt, &inst.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return saga.Instance{}, saga.ErrInstanceNotFound
		}
		return saga.Instance{}, redact.WrapError("pgstore: scan", err)
	}
	if inputBytes != nil {
		inst.Input = json.RawMessage(inputBytes)
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
