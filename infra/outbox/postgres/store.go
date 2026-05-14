package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/infra/v2/outbox"
)

// Store is a pgx-backed [outbox.Store]. It implements every role in
// the [outbox] persistence contract (Inserter, Claimer, Outcomer,
// Janitor, Observer) against a single audit_log_entries table.
//
// Insert participates in the caller's business transaction when ctx
// carries a [pgx.Tx] via [WithTx]; otherwise it falls back to the
// pool. Relay paths (FetchPending / Heartbeat / Mark* / IncrementAttempts /
// Delete* / ResetStaleProcessing / CountPending) always use the pool —
// the relay's coordination semantics (FOR UPDATE SKIP LOCKED on
// pending claim, status-guarded UPDATEs on outcome) make a caller-
// supplied tx the wrong unit of atomicity for those operations.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store wrapping pool. Panics on a nil pool — fail fast
// at construction beats a deferred nil-deref on the first Insert.
func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		panic("outbox/postgres: pool must not be nil")
	}
	return &Store{pool: pool}
}

// Insert persists entry. Uses the ctx-stashed pgx.Tx when present so
// the row commits atomically with the caller's business writes; falls
// back to the pool otherwise (outbox.NewWriter's txCheck is the
// recommended guardrail against the fallback path).
func (s *Store) Insert(ctx context.Context, entry outbox.Entry) error {
	if s == nil || s.pool == nil {
		return errors.New("outbox/postgres: store not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.ID == uuid.Nil {
		return fmt.Errorf("outbox/postgres: entry id must not be zero")
	}
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	const q = `
INSERT INTO outbox_entries
(id, topic, routing_key, message_id, message_type, payload, headers,
 status, attempts, created_at, updated_at, published_at, next_retry_at, last_error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`
	var (
		headers   any
		published any
		nextRetry any
		lastErr   any
	)
	if len(entry.Headers) > 0 && string(entry.Headers) != "null" {
		headers = []byte(entry.Headers)
	}
	if entry.PublishedAt != nil {
		published = entry.PublishedAt.UTC()
	}
	if entry.NextRetryAt != nil {
		nextRetry = entry.NextRetryAt.UTC()
	}
	if entry.LastError != nil {
		lastErr = *entry.LastError
	}
	status := entry.Status
	if status == "" {
		status = outbox.StatusPending
	}
	args := []any{
		entry.ID, entry.Topic, entry.RoutingKey, entry.MessageID, entry.MessageType,
		[]byte(entry.Payload), headers, string(status), entry.Attempts,
		createdAt.UTC(), createdAt.UTC(), published, nextRetry, lastErr,
	}
	var err error
	if tx, ok := TxFromContext(ctx); ok {
		_, err = tx.Exec(ctx, q, args...)
	} else {
		_, err = s.pool.Exec(ctx, q, args...)
	}
	if err != nil {
		return fmt.Errorf("outbox/postgres: insert: %w", err)
	}
	return nil
}

// FetchPending claims up to limit pending entries by transitioning
// them to processing. SKIP LOCKED lets multiple relay replicas
// coordinate without holding row locks across the publish phase —
// each replica grabs a disjoint slice. Entries whose next_retry_at is
// still in the future are skipped so exponential backoff actually
// takes effect.
func (s *Store) FetchPending(ctx context.Context, limit int) ([]outbox.Entry, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("outbox/postgres: store not initialized")
	}
	if limit <= 0 {
		return nil, nil
	}
	const q = `
WITH claimed AS (
    SELECT id
    FROM outbox_entries
    WHERE status = 'pending'
      AND (next_retry_at IS NULL OR next_retry_at <= NOW())
    ORDER BY created_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox_entries
SET status = 'processing',
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, topic, routing_key, message_id, message_type, payload, headers,
          status, attempts, created_at, published_at, next_retry_at, last_error`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox/postgres: fetch pending: %w", err)
	}
	defer rows.Close()
	out := make([]outbox.Entry, 0, limit)
	for rows.Next() {
		e, scanErr := scanEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("outbox/postgres: fetch pending scan: %w", scanErr)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox/postgres: fetch pending iterate: %w", err)
	}
	return out, nil
}

// Heartbeat extends the updated_at watermark on the listed processing
// rows so [ResetStaleProcessing] does not reset them while a long
// publish is in flight. The status='processing' guard keeps a stale
// list from resurrecting rows that already moved to published/failed.
func (s *Store) Heartbeat(ctx context.Context, ids []string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	if len(ids) == 0 {
		return 0, nil
	}
	const q = `
UPDATE outbox_entries
SET updated_at = NOW()
WHERE status = 'processing'
  AND id = ANY($1::uuid[])`
	ct, err := s.pool.Exec(ctx, q, ids)
	if err != nil {
		return 0, fmt.Errorf("outbox/postgres: heartbeat: %w", err)
	}
	return ct.RowsAffected(), nil
}

// MarkPublished transitions a claimed entry to the terminal published
// state. The status='processing' guard turns into [outbox.ErrStaleState]
// when a concurrent ResetStaleProcessing has already pulled the row
// back to pending — the caller (relay) must not record a successful
// publish in that case because the same row may be claimed by another
// worker.
func (s *Store) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	return s.transition(ctx, id, transitionParams{
		sql: `
UPDATE outbox_entries
SET status = 'published',
    updated_at = NOW(),
    published_at = $2,
    last_error = NULL
WHERE id = $1 AND status = 'processing'`,
		args: []any{publishedAt.UTC()},
	})
}

// MarkFailed transitions a claimed entry to the terminal failed state
// (max-attempts exhausted). status='processing' guard mirrors
// MarkPublished's stale-state semantics.
func (s *Store) MarkFailed(ctx context.Context, id string, lastError string) error {
	return s.transition(ctx, id, transitionParams{
		sql: `
UPDATE outbox_entries
SET status = 'failed',
    updated_at = NOW(),
    last_error = $2
WHERE id = $1 AND status = 'processing'`,
		args: []any{lastError},
	})
}

// IncrementAttempts records a transient publish failure: bump
// attempts, store last_error, schedule the next retry, and return the
// row to pending so FetchPending will pick it up again after
// nextRetryAt. status='processing' guard mirrors MarkPublished/MarkFailed.
func (s *Store) IncrementAttempts(ctx context.Context, id string, lastError string, nextRetryAt time.Time) error {
	return s.transition(ctx, id, transitionParams{
		sql: `
UPDATE outbox_entries
SET status = 'pending',
    attempts = attempts + 1,
    updated_at = NOW(),
    next_retry_at = $2,
    last_error = $3
WHERE id = $1 AND status = 'processing'`,
		args: []any{nextRetryAt.UTC(), lastError},
	})
}

type transitionParams struct {
	sql  string
	args []any
}

// transition runs an UPDATE with the (id, status='processing') guard
// and translates RowsAffected==0 into [outbox.ErrNotFound] (no row
// with that id) or [outbox.ErrStaleState] (row exists but is not in
// processing) by issuing a tiny lookup query. The two-query shape is
// the simplest portable way to distinguish those cases — Postgres'
// xmax / pgx CommandTag does not surface which clause filtered the
// row out.
func (s *Store) transition(ctx context.Context, id string, p transitionParams) error {
	if s == nil || s.pool == nil {
		return errors.New("outbox/postgres: store not initialized")
	}
	args := append([]any{id}, p.args...)
	ct, err := s.pool.Exec(ctx, p.sql, args...)
	if err != nil {
		return fmt.Errorf("outbox/postgres: transition: %w", err)
	}
	if ct.RowsAffected() == 1 {
		return nil
	}
	// Disambiguate not-found vs stale-state for the relay's logging
	// and metric paths.
	var status string
	err = s.pool.QueryRow(ctx, `SELECT status FROM outbox_entries WHERE id = $1`, id).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return outbox.ErrNotFound
		}
		return fmt.Errorf("outbox/postgres: transition disambiguate: %w", err)
	}
	return fmt.Errorf("%w: row in status %q", outbox.ErrStaleState, status)
}

// DeletePublishedBefore prunes published entries older than the cutoff
// using the partial index on (status='published', published_at).
func (s *Store) DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	const q = `DELETE FROM outbox_entries WHERE status = 'published' AND published_at < $1`
	ct, err := s.pool.Exec(ctx, q, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("outbox/postgres: delete published before: %w", err)
	}
	return ct.RowsAffected(), nil
}

// DeleteFailedBefore prunes failed entries older than the cutoff. The
// dead-letter retention sweep prevents the table from growing
// unboundedly when a downstream stays broken across the max-attempts
// window.
func (s *Store) DeleteFailedBefore(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	const q = `DELETE FROM outbox_entries WHERE status = 'failed' AND updated_at < $1`
	ct, err := s.pool.Exec(ctx, q, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("outbox/postgres: delete failed before: %w", err)
	}
	return ct.RowsAffected(), nil
}

// ResetStaleProcessing recovers entries left in processing by a
// crashed relay. The (NOW() - updated_at > staleDuration) guard means
// a healthy Heartbeat from the live worker prevents the reset.
func (s *Store) ResetStaleProcessing(ctx context.Context, staleDuration time.Duration) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	if staleDuration <= 0 {
		return 0, fmt.Errorf("outbox/postgres: staleDuration must be positive")
	}
	// Cast via make_interval so a negative or otherwise unsafe
	// duration cannot flow into the comparison; staleDuration is
	// validated above as positive.
	const q = `
UPDATE outbox_entries
SET status = 'pending',
    updated_at = NOW()
WHERE status = 'processing'
  AND updated_at < NOW() - make_interval(secs => $1)`
	ct, err := s.pool.Exec(ctx, q, staleDuration.Seconds())
	if err != nil {
		return 0, fmt.Errorf("outbox/postgres: reset stale processing: %w", err)
	}
	return ct.RowsAffected(), nil
}

// CountPending reports the number of pending entries (including those
// awaiting their next_retry_at). Useful for dashboards and the
// outbox-depth health check.
func (s *Store) CountPending(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	const q = `SELECT count(*) FROM outbox_entries WHERE status = 'pending'`
	var n int64
	if err := s.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("outbox/postgres: count pending: %w", err)
	}
	return n, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanEntry(s scannable) (outbox.Entry, error) {
	var (
		e            outbox.Entry
		payload      []byte
		headers      []byte
		status       string
		publishedAt  *time.Time
		nextRetryAt  *time.Time
		lastErrorStr *string
	)
	if err := s.Scan(
		&e.ID, &e.Topic, &e.RoutingKey, &e.MessageID, &e.MessageType,
		&payload, &headers, &status, &e.Attempts, &e.CreatedAt,
		&publishedAt, &nextRetryAt, &lastErrorStr,
	); err != nil {
		return outbox.Entry{}, err
	}
	e.Payload = json.RawMessage(payload)
	if len(headers) > 0 {
		e.Headers = json.RawMessage(headers)
	}
	e.Status = outbox.Status(status)
	e.CreatedAt = e.CreatedAt.UTC()
	if publishedAt != nil {
		t := publishedAt.UTC()
		e.PublishedAt = &t
	}
	if nextRetryAt != nil {
		t := nextRetryAt.UTC()
		e.NextRetryAt = &t
	}
	if lastErrorStr != nil {
		v := *lastErrorStr
		e.LastError = &v
	}
	return e, nil
}
