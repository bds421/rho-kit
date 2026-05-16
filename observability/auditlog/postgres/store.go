package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

// defaultLimit is applied when [Store.Query] receives limit <= 0. The
// [auditlog.Logger] enforces limit < 0 rejection at the API boundary
// (ErrLimitNegative), so this branch is reached only on the explicit
// "Store picks the page size" code path (limit == 0).
const defaultLimit = 50

// chainLockKey is the bigint argument passed to pg_advisory_xact_lock
// to serialise every appender against the shared audit chain. The value
// is a deterministic hash of the literal string "auditlog_chain" so
// every replica using this Store shares the same lock identity without
// requiring a configured key.
//
// Computed once at package load with hashtext('auditlog_chain') in
// Postgres: SELECT hashtext('auditlog_chain'). Hard-coding it here
// avoids a per-append SELECT and keeps the lock identity stable across
// Postgres major versions (hashtext is documented stable).
const chainLockKey int64 = 8_157_398_551_437_201_487

// Store is a pgx-backed [auditlog.Store]. It implements the
// tamper-evident chain contract the in-process [auditlog.Logger]
// expects:
//
//   - AppendChained holds pg_advisory_xact_lock for the duration of
//     the build+persist sequence so two concurrent appenders cannot
//     observe the same prev HMAC and fork the chain.
//   - Query returns events ordered by (occurred_at DESC, id DESC),
//     matching the [auditlog.Store] contract.
//   - RangeChain iterates by the monotonic append-order seq column,
//     not by timestamp, so a backfilled or clock-skewed event still
//     verifies under [Logger.VerifyChain].
//   - LastHMAC reads the tail HMAC for operator tooling.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store wrapping the given pool. Panics on a nil pool —
// pool absence would surface at the first append as a generic nil-deref;
// failing fast at construction keeps the misconfiguration obvious.
func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		panic("auditlog/postgres: New pool must not be nil")
	}
	return &Store{pool: pool}
}

// AppendChained runs build inside a transaction that holds the
// audit-chain advisory lock plus SELECT FOR UPDATE on the tail row.
// The combination serialises concurrent appenders even on an empty
// table — SELECT FOR UPDATE has nothing to lock until the first row
// exists, so the advisory lock carries the serialization until then.
func (s *Store) AppendChained(ctx context.Context, build func(prev []byte) (auditlog.Event, error)) error {
	if s == nil || s.pool == nil {
		return errors.New("auditlog/postgres: store not initialized")
	}
	if build == nil {
		return auditlog.ErrInvalidEvent
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("auditlog/postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", chainLockKey); err != nil {
		return fmt.Errorf("auditlog/postgres: advisory lock: %w", err)
	}

	prev, err := selectTailHMACForUpdate(ctx, tx)
	if err != nil {
		return err
	}

	event, err := build(prev)
	if err != nil {
		return err
	}
	if err := auditlog.ValidateEvent(event); err != nil {
		return err
	}

	if err := insertEvent(ctx, tx, event); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("auditlog/postgres: commit: %w", err)
	}
	return nil
}

// Query implements the [auditlog.Store] contract: returns events
// matching filter ordered by (occurred_at DESC, id DESC) with cursor
// pagination. The cursor format is opaque to callers — [Logger.List]
// wraps it in a signed envelope before exposing it to the request
// boundary, so the raw shape can evolve without breaking clients.
func (s *Store) Query(ctx context.Context, filter auditlog.Filter, cursor string, limit int) ([]auditlog.Event, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", errors.New("auditlog/postgres: store not initialized")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	// Defensive: mirror the Logger.List boundary check so callers that
	// reach the Store directly cannot bypass the limit guards.
	if limit < 0 {
		return nil, "", auditlog.ErrLimitNegative
	}
	if limit > auditlog.MaxPageLimit {
		return nil, "", auditlog.ErrLimitTooLarge
	}
	if limit == 0 {
		limit = defaultLimit
	}

	cursorTime, cursorID, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	args := make([]any, 0, 8)
	clauses := make([]string, 0, 8)
	add := func(expr string, val any) {
		args = append(args, val)
		clauses = append(clauses, fmt.Sprintf(expr, len(args)))
	}
	if filter.Actor != "" {
		add("actor = $%d", filter.Actor)
	}
	if filter.Action != "" {
		add("action = $%d", filter.Action)
	}
	if filter.Resource != "" {
		// MemoryStore's matchesFilter uses strings.HasPrefix; mirror that
		// for cross-Store consistency. text_pattern_ops index on resource
		// keeps the plan O(log n) for narrow prefixes.
		add("resource LIKE $%d", escapeLikePrefix(filter.Resource)+"%")
	}
	if filter.IPAddress != "" {
		add("ip_address = $%d", filter.IPAddress)
	}
	if !filter.Since.IsZero() {
		add("occurred_at >= $%d", filter.Since.UTC())
	}
	if !filter.Until.IsZero() {
		add("occurred_at <= $%d", filter.Until.UTC())
	}
	if cursor != "" {
		args = append(args, cursorTime, cursorID)
		clauses = append(clauses, fmt.Sprintf(
			"(occurred_at < $%d OR (occurred_at = $%d AND id < $%d))",
			len(args)-1, len(args)-1, len(args),
		))
	}

	sql := selectColumns + " FROM audit_log_events"
	if len(clauses) > 0 {
		sql += " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit+1)
	sql += fmt.Sprintf(" ORDER BY occurred_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("auditlog/postgres: query: %w", err)
	}
	defer rows.Close()

	out := make([]auditlog.Event, 0, limit)
	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return nil, "", fmt.Errorf("auditlog/postgres: query scan: %w", scanErr)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("auditlog/postgres: query iterate: %w", err)
	}

	var next string
	if len(out) > limit {
		marker := out[limit-1]
		next = encodeCursor(marker.Timestamp, marker.ID)
		out = out[:limit]
	}
	return out, next, nil
}

// RangeChain iterates every event in append order (seq ASC) so the
// chain verifier observes events in the order they were persisted,
// independent of caller-supplied [Event.Timestamp]. Rows stream from
// the cursor one at a time so the verifier's memory usage stays
// bounded over arbitrarily large chains.
func (s *Store) RangeChain(ctx context.Context, fn func(auditlog.Event) error) error {
	if s == nil || s.pool == nil {
		return errors.New("auditlog/postgres: store not initialized")
	}
	if fn == nil {
		return auditlog.ErrInvalidEvent
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	const q = selectColumns + " FROM audit_log_events ORDER BY seq ASC"
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("auditlog/postgres: range chain: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		event, scanErr := scanEvent(rows)
		if scanErr != nil {
			return fmt.Errorf("auditlog/postgres: range chain scan: %w", scanErr)
		}
		if err := fn(event); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("auditlog/postgres: range chain iterate: %w", err)
	}
	return nil
}

// LastHMAC returns the HMAC of the most recently persisted event, or
// nil when the chain is empty. Useful for operator tooling that wants
// to inspect the tail without holding the chain lock.
func (s *Store) LastHMAC(ctx context.Context) ([]byte, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("auditlog/postgres: store not initialized")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const q = "SELECT hmac FROM audit_log_events ORDER BY seq DESC LIMIT 1"
	var hmac []byte
	if err := s.pool.QueryRow(ctx, q).Scan(&hmac); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("auditlog/postgres: last hmac: %w", err)
	}
	if len(hmac) == 0 {
		return nil, nil
	}
	return hmac, nil
}

const selectColumns = `SELECT id, occurred_at, actor, action, resource, status,
       ip_address, trace_id, metadata, prev_hmac, hmac`

func selectTailHMACForUpdate(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	const q = "SELECT hmac FROM audit_log_events ORDER BY seq DESC LIMIT 1 FOR UPDATE"
	var hmac []byte
	if err := tx.QueryRow(ctx, q).Scan(&hmac); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("auditlog/postgres: select tail hmac: %w", err)
	}
	if len(hmac) == 0 {
		return nil, nil
	}
	return hmac, nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, e auditlog.Event) error {
	var metaRaw []byte
	if len(e.Metadata) > 0 {
		metaRaw = e.Metadata
	}
	const q = `
INSERT INTO audit_log_events
(id, occurred_at, actor, action, resource, status, ip_address, trace_id,
 metadata, prev_hmac, hmac)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`
	prev := e.PrevHMAC
	if prev == nil {
		prev = []byte{}
	}
	if _, err := tx.Exec(ctx, q,
		e.ID, e.Timestamp.UTC(), e.Actor, e.Action, e.Resource, e.Status,
		e.IPAddress, e.TraceID, metaRaw, prev, e.HMAC,
	); err != nil {
		return fmt.Errorf("auditlog/postgres: insert event: %w", err)
	}
	return nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanEvent(s scannable) (auditlog.Event, error) {
	var (
		e        auditlog.Event
		metaRaw  []byte
		prevHMAC []byte
		hmac     []byte
	)
	if err := s.Scan(
		&e.ID, &e.Timestamp, &e.Actor, &e.Action, &e.Resource, &e.Status,
		&e.IPAddress, &e.TraceID, &metaRaw, &prevHMAC, &hmac,
	); err != nil {
		return auditlog.Event{}, err
	}
	e.Timestamp = e.Timestamp.UTC()
	if len(metaRaw) > 0 {
		e.Metadata = json.RawMessage(metaRaw)
	}
	if len(prevHMAC) > 0 {
		e.PrevHMAC = append([]byte(nil), prevHMAC...)
	}
	if len(hmac) > 0 {
		e.HMAC = append([]byte(nil), hmac...)
	}
	return e, nil
}

// Cursor format: "<unix_nano_hex>:<event_id>". Opaque to callers
// (Logger.List wraps it in a signed envelope) and stable across
// Store instances so a service can fail over without invalidating
// in-flight pages.
func encodeCursor(t time.Time, id string) string {
	return fmt.Sprintf("%016x:%s", t.UTC().UnixNano(), id)
}

func decodeCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	sep := strings.IndexByte(cursor, ':')
	if sep <= 0 || sep == len(cursor)-1 {
		return time.Time{}, "", fmt.Errorf("auditlog/postgres: malformed cursor")
	}
	tsHex := cursor[:sep]
	if len(tsHex) != 16 {
		return time.Time{}, "", fmt.Errorf("auditlog/postgres: malformed cursor timestamp")
	}
	id := cursor[sep+1:]
	if len(id) > auditlog.MaxEventIDBytes {
		return time.Time{}, "", fmt.Errorf("auditlog/postgres: malformed cursor id")
	}
	rawTs, err := hex.DecodeString(tsHex)
	if err != nil || len(rawTs) != 8 {
		return time.Time{}, "", fmt.Errorf("auditlog/postgres: malformed cursor timestamp")
	}
	var nanos int64
	for _, b := range rawTs {
		nanos = (nanos << 8) | int64(b)
	}
	return time.Unix(0, nanos).UTC(), id, nil
}

// escapeLikePrefix escapes the LIKE metacharacters in a prefix so
// `'foo%bar' + '%'` does not become a wildcard within the prefix.
// Postgres' default LIKE escape is '\\'; the kit never alters that
// session default so escaping the four literals is sufficient.
func escapeLikePrefix(s string) string {
	if !strings.ContainsAny(s, "%_\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
