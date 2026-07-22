package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

// Store is a pgx-backed [outbox.Store]. It implements every role in
// the [outbox] persistence contract (Inserter, Claimer, Outcomer,
// Janitor, Observer) against a single outbox_entries table.
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

	// claimTokens fences outcome updates by claim ownership. FetchPending
	// stamps a fresh per-row token on each claim (DB column claim_token)
	// and remembers id->token here; MarkPublished / MarkFailed /
	// IncrementAttempts / ResetPending add `AND claim_token = $token` so a
	// late update from a relay whose claim was stale-reset (and re-claimed
	// by another process) cannot resurrect or duplicate the row — the ABA
	// race. The map is process-local, so each process only ever fences with
	// tokens it minted; cross-process fencing falls out of that naturally.
	// Entries are removed on terminal outcomes to keep the map bounded.
	claimMu     sync.Mutex
	claimTokens map[string]string
}

// Compile-time guarantees that Store satisfies the full persistence
// contract and the optional shutdown-reset capability the relay probes
// for via a type assertion.
var (
	_ outbox.Store           = (*Store)(nil)
	_ outbox.PendingResetter = (*Store)(nil)
)

// New returns a Store wrapping pool. Panics on a nil pool — fail fast
// at construction beats a deferred nil-deref on the first Insert.
func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		panic("outbox/postgres: New pool must not be nil")
	}
	return &Store{
		pool:        pool,
		claimTokens: make(map[string]string),
	}
}

// rememberClaim records the token minted for a claimed row. Overwrites any
// prior token for the id (a re-claim after this process reset the row).
func (s *Store) rememberClaim(id, token string) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	if s.claimTokens == nil {
		s.claimTokens = make(map[string]string)
	}
	s.claimTokens[id] = token
}

// claimToken returns the remembered token for id and whether one exists.
func (s *Store) claimToken(id string) (string, bool) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	tok, ok := s.claimTokens[id]
	return tok, ok
}

// forgetClaim drops the remembered token for id once the row reaches a
// terminal outcome (published / failed / re-queued / reset), keeping the
// id->token map bounded.
func (s *Store) forgetClaim(id string) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	delete(s.claimTokens, id)
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
(id, topic, routing_key, message_id, message_type, schema_version, payload, headers,
 status, attempts, created_at, updated_at, published_at, next_retry_at, last_error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`
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
		entry.ID, entry.Topic, entry.RoutingKey, entry.MessageID, entry.MessageType, entry.SchemaVersion,
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
		return redact.WrapError("outbox/postgres: insert", err)
	}
	return nil
}

// FetchPending claims up to limit pending entries by transitioning
// them to processing. SKIP LOCKED lets multiple relay replicas
// coordinate without holding row locks across the publish phase —
// each replica grabs a disjoint slice. Entries whose next_retry_at is
// still in the future are skipped so exponential backoff actually
// takes effect.
//
// FIFO contract: the returned slice is ordered oldest-first by
// created_at, tie-broken by id, so the relay's default serial publish
// path preserves the FIFO-on-the-wire behaviour documented on
// [outbox.Relay]. Postgres does not guarantee that a bare
// UPDATE ... RETURNING preserves any CTE ORDER BY, so we carry the
// ordinal through a row_number() column and re-order the final
// SELECT explicitly.
//
// SQL shape: the locking SELECT is its own CTE WITHOUT a window
// function — Postgres rejects FOR UPDATE in a query that has a
// window function in its select list. A second CTE adds the
// row_number for stable FIFO ordering downstream.
func (s *Store) FetchPending(ctx context.Context, limit int) ([]outbox.Entry, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("outbox/postgres: store not initialized")
	}
	if limit <= 0 {
		return nil, nil
	}
	// claim_token = gen_random_uuid() mints a fresh per-row token on every
	// claim; it is RETURNed (last column) so the store can remember
	// id->token and later fence the outcome UPDATEs on claim ownership.
	const q = `
WITH locked AS (
    SELECT id, created_at
    FROM outbox_entries
    WHERE status = 'pending'
      AND (next_retry_at IS NULL OR next_retry_at <= NOW())
    ORDER BY created_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
),
ordered AS (
    SELECT id,
           row_number() OVER (ORDER BY created_at, id) AS ord
    FROM locked
),
updated AS (
    UPDATE outbox_entries AS o
    SET status = 'processing',
        updated_at = NOW(),
        claim_token = gen_random_uuid()
    FROM ordered
    WHERE o.id = ordered.id
    RETURNING o.id, o.topic, o.routing_key, o.message_id, o.message_type, o.schema_version,
              o.payload, o.headers, o.status, o.attempts, o.created_at,
              o.published_at, o.next_retry_at, o.last_error, o.claim_token,
              ordered.ord
)
SELECT id, topic, routing_key, message_id, message_type, schema_version, payload, headers,
       status, attempts, created_at, published_at, next_retry_at, last_error,
       claim_token
FROM updated
ORDER BY ord`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, redact.WrapError("outbox/postgres: fetch pending", err)
	}
	defer rows.Close()
	out := make([]outbox.Entry, 0, limit)
	type claim struct{ id, token string }
	claims := make([]claim, 0, limit)
	for rows.Next() {
		e, token, scanErr := scanClaimedEntry(rows)
		if scanErr != nil {
			return nil, redact.WrapError("outbox/postgres: fetch pending scan", scanErr)
		}
		out = append(out, e)
		claims = append(claims, claim{id: e.ID.String(), token: token})
	}
	if err := rows.Err(); err != nil {
		return nil, redact.WrapError("outbox/postgres: fetch pending iterate", err)
	}
	// Remember tokens only after a clean iteration so a mid-scan failure
	// does not leave half a batch fenced under this process.
	for _, c := range claims {
		s.rememberClaim(c.id, c.token)
	}
	return out, nil
}

// Heartbeat extends the updated_at watermark on the listed processing
// rows so [ResetStaleProcessing] does not reset them while a long
// publish is in flight. Rows are fenced by the process-local claim_token
// remembered at FetchPending — a late heartbeat from a relay whose claim
// was stale-reset and re-claimed by another process cannot keep the new
// owner's lease alive. The status='processing' guard additionally keeps a
// stale list from resurrecting rows that already moved to published/failed.
func (s *Store) Heartbeat(ctx context.Context, ids []string) (int64, error) {
	if s == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	if len(ids) == 0 {
		return 0, nil
	}
	// Pair each id with the claim token this process minted. Skip ids we
	// do not own (no remembered token) so we never refresh another
	// worker's claim. Empty ownership set is a no-op (no SQL).
	hbIDs := make([]string, 0, len(ids))
	hbTokens := make([]string, 0, len(ids))
	for _, id := range ids {
		tok, ok := s.claimToken(id)
		if !ok || tok == "" {
			continue
		}
		hbIDs = append(hbIDs, id)
		hbTokens = append(hbTokens, tok)
	}
	if len(hbIDs) == 0 {
		return 0, nil
	}
	if s.pool == nil {
		return 0, errors.New("outbox/postgres: store not initialized")
	}
	const q = `
UPDATE outbox_entries AS o
SET updated_at = NOW()
FROM unnest($1::uuid[], $2::uuid[]) AS t(id, token)
WHERE o.id = t.id
  AND o.status = 'processing'
  AND o.claim_token = t.token`
	ct, err := s.pool.Exec(ctx, q, hbIDs, hbTokens)
	if err != nil {
		return 0, redact.WrapError("outbox/postgres: heartbeat", err)
	}
	return ct.RowsAffected(), nil
}

// MarkPublished transitions a claimed entry to the terminal published
// state. The (status='processing' AND claim_token=$tok) guard turns into
// [outbox.ErrStaleState] when a concurrent ResetStaleProcessing has
// already pulled the row back to pending (or another relay re-claimed it,
// minting a new token) — the caller (relay) must not record a successful
// publish in that case because the same row may be owned by another
// worker. The claim_token positional ($3) is appended by transition.
func (s *Store) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	return s.transition(ctx, id, transitionParams{
		sql: `
UPDATE outbox_entries
SET status = 'published',
    updated_at = NOW(),
    published_at = $2,
    last_error = NULL
WHERE id = $1 AND status = 'processing' AND claim_token = $3`,
		args: []any{publishedAt.UTC()},
	})
}

// MarkFailed transitions a claimed entry to the terminal failed state
// (max-attempts exhausted). The (status='processing' AND claim_token)
// guard mirrors MarkPublished's stale-state semantics. claim_token is $3.
func (s *Store) MarkFailed(ctx context.Context, id string, lastError string) error {
	return s.transition(ctx, id, transitionParams{
		sql: `
UPDATE outbox_entries
SET status = 'failed',
    updated_at = NOW(),
    last_error = $2
WHERE id = $1 AND status = 'processing' AND claim_token = $3`,
		args: []any{lastError},
	})
}

// IncrementAttempts records a transient publish failure: bump
// attempts, store last_error, schedule the next retry, and return the
// row to pending so FetchPending will pick it up again after
// nextRetryAt. The (status='processing' AND claim_token) guard mirrors
// MarkPublished/MarkFailed; claim_token is $4. The row is cleared back to
// claim_token=NULL so a later claim mints a fresh token.
func (s *Store) IncrementAttempts(ctx context.Context, id string, lastError string, nextRetryAt time.Time) error {
	return s.transition(ctx, id, transitionParams{
		sql: `
UPDATE outbox_entries
SET status = 'pending',
    attempts = attempts + 1,
    updated_at = NOW(),
    next_retry_at = $2,
    last_error = $3,
    claim_token = NULL
WHERE id = $1 AND status = 'processing' AND claim_token = $4`,
		args: []any{nextRetryAt.UTC(), lastError},
	})
}

// ResetPending returns the listed claimed rows to "pending" so a freshly
// started replica can re-claim them immediately rather than waiting out
// the stale window. It is the [outbox.PendingResetter] capability the
// relay calls on shutdown for rows it claimed but never finished
// publishing.
//
// Each id is fenced on (status='processing' AND claim_token=$tok) using
// the token this process remembered when it claimed the row, so a row
// that has since been re-claimed by another process (different token), or
// already published/failed, is skipped silently — never resurrected.
// claim_token is cleared to NULL on reset so the next claim mints a fresh
// token. The forgotten ids drop out of the in-process token map.
func (s *Store) ResetPending(ctx context.Context, ids []string) error {
	if s == nil || s.pool == nil {
		return errors.New("outbox/postgres: store not initialized")
	}
	if len(ids) == 0 {
		return nil
	}
	// Collect (id, token) pairs this process still remembers, then forget
	// every one regardless of Exec outcome — ownership is being relinquished
	// on the shutdown path either way (prevents claimTokens growth on
	// partial errors).
	type pair struct{ id, token string }
	pairs := make([]pair, 0, len(ids))
	for _, id := range ids {
		token, ok := s.claimToken(id)
		if !ok {
			continue
		}
		pairs = append(pairs, pair{id: id, token: token})
	}
	for _, p := range pairs {
		s.forgetClaim(p.id)
	}
	if len(pairs) == 0 {
		return nil
	}

	// Single batched UPDATE ... FROM (VALUES ...) so shutdown under a tight
	// resetTimeout does not pay N serial round-trips.
	args := make([]any, 0, len(pairs)*2)
	valParts := make([]string, 0, len(pairs))
	for i, p := range pairs {
		a := i*2 + 1
		b := i*2 + 2
		valParts = append(valParts, fmt.Sprintf("($%d::text, $%d::text)", a, b))
		args = append(args, p.id, p.token)
	}
	q := `
UPDATE outbox_entries AS o
SET status = 'pending',
    updated_at = NOW(),
    claim_token = NULL
FROM (VALUES ` + strings.Join(valParts, ", ") + `) AS v(id, token)
WHERE o.id = v.id AND o.status = 'processing' AND o.claim_token = v.token`
	if _, err := s.pool.Exec(ctx, q, args...); err != nil {
		return redact.WrapError("outbox/postgres: reset pending", err)
	}
	return nil
}

type transitionParams struct {
	sql  string
	args []any
}

// transition runs an outcome UPDATE fenced on (id, status='processing',
// claim_token) and translates RowsAffected==0 into [outbox.ErrNotFound]
// (no row with that id) or [outbox.ErrStaleState] (row exists but is no
// longer in processing under this claim's token — reset, re-claimed, or
// already completed) by issuing a tiny lookup query. The two-query shape
// is the simplest portable way to distinguish those cases — Postgres'
// xmax / pgx CommandTag does not surface which clause filtered the row
// out.
//
// The claim_token to fence with is the token this process remembered when
// it claimed the row via FetchPending. When no token is remembered (the
// row was never claimed by this process), uuid.Nil is used — it cannot
// match any gen_random_uuid() token, so the update affects no row and the
// disambiguation reports NotFound/StaleState correctly rather than
// silently mutating a row this process does not own. A successful (owned)
// terminal transition forgets the claim to keep the token map bounded.
func (s *Store) transition(ctx context.Context, id string, p transitionParams) error {
	if s == nil || s.pool == nil {
		return errors.New("outbox/postgres: store not initialized")
	}
	token, ok := s.claimToken(id)
	if !ok {
		token = uuid.Nil.String()
	}
	args := append([]any{id}, p.args...)
	args = append(args, token)
	ct, err := s.pool.Exec(ctx, p.sql, args...)
	if err != nil {
		return redact.WrapError("outbox/postgres: transition", err)
	}
	if ct.RowsAffected() == 1 {
		// Terminal outcome under our claim — drop the remembered token.
		s.forgetClaim(id)
		return nil
	}
	// Disambiguate not-found vs stale-state for the relay's logging
	// and metric paths.
	var status string
	err = s.pool.QueryRow(ctx, `SELECT status FROM outbox_entries WHERE id = $1`, id).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row gone entirely — our claim is meaningless now.
			s.forgetClaim(id)
			return outbox.ErrNotFound
		}
		return redact.WrapError("outbox/postgres: transition disambiguate", err)
	}
	// Row exists but our fenced UPDATE matched nothing: it was reset,
	// re-claimed (new token), or already completed. We no longer own it.
	s.forgetClaim(id)
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
		return 0, redact.WrapError("outbox/postgres: delete published before", err)
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
		return 0, redact.WrapError("outbox/postgres: delete failed before", err)
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
	// Clear claim_token so a reset row matches the documented invariant
	// (status='pending' ⇒ claim_token IS NULL) used by IncrementAttempts
	// and ResetPending. FetchPending always overwrites the token on the
	// next claim, but leaving a stale token on pending rows is a trap
	// for future fences that assume the invariant.
	const q = `
UPDATE outbox_entries
SET status = 'pending',
    claim_token = NULL,
    updated_at = NOW()
WHERE status = 'processing'
  AND updated_at < NOW() - make_interval(secs => $1)`
	ct, err := s.pool.Exec(ctx, q, staleDuration.Seconds())
	if err != nil {
		return 0, redact.WrapError("outbox/postgres: reset stale processing", err)
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
		return 0, redact.WrapError("outbox/postgres: count pending", err)
	}
	return n, nil
}

type scannable interface {
	Scan(dest ...any) error
}

// scanClaimedEntry scans a FetchPending row that carries the freshly
// minted claim_token as its final column, returning the entry plus the
// token string the store remembers to fence later outcome updates. The
// token column is non-NULL on every claimed row because FetchPending sets
// it in the same UPDATE that transitions the row to 'processing'.
func scanClaimedEntry(s scannable) (outbox.Entry, string, error) {
	var (
		e             outbox.Entry
		schemaVersion int64
		payload       []byte
		headers       []byte
		status        string
		publishedAt   *time.Time
		nextRetryAt   *time.Time
		lastErrorStr  *string
		token         uuid.UUID
	)
	if err := s.Scan(
		&e.ID, &e.Topic, &e.RoutingKey, &e.MessageID, &e.MessageType, &schemaVersion,
		&payload, &headers, &status, &e.Attempts, &e.CreatedAt,
		&publishedAt, &nextRetryAt, &lastErrorStr, &token,
	); err != nil {
		return outbox.Entry{}, "", err
	}
	if schemaVersion < 0 {
		return outbox.Entry{}, "", fmt.Errorf("outbox/postgres: negative schema version")
	}
	e.SchemaVersion = uint(schemaVersion)
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
	return e, token.String(), nil
}
