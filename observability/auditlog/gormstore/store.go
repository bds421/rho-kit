package gormstore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/observability/auditlog"
)

// likeEscape escapes the LIKE metacharacters %, _, and \ in s so the input
// is treated as a literal prefix when interpolated into a `LIKE ? ESCAPE '\'`
// clause. Without this, a caller-controlled filter.Resource of `users/%/secrets`
// would match any resource path under "users/" — an audit-log information
// disclosure even though the SQL is parameterised.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ErrCursorInvalid is returned when a cursor is malformed, has been tampered
// with, or was minted by a different secret. Callers receiving this should
// reject the request rather than restart pagination from the beginning —
// silently restarting masks attempted forgeries.
var ErrCursorInvalid = errors.New("gormstore: invalid or tampered audit cursor")

// encodeCursor packs (timestamp, id) into an HMAC-signed opaque pagination
// token. Format: base64url(payload) "." base64url(signature) where payload
// is "{nanos}|{id}". Signing prevents callers from hand-editing the cursor
// to skip arbitrary spans of the audit trail (the attack the audit calls
// out — a forged timestamp of "year 3000" would otherwise bypass any
// page-by-page review).
//
// Composite cursor is necessary because the query orders by
// (timestamp DESC, id DESC) — using id alone caused rows to be skipped or
// duplicated whenever timestamps tied or IDs were not perfectly correlated
// with timestamps.
func (s *GormStore) encodeCursor(ts time.Time, id string) string {
	payload := strconv.FormatInt(ts.UnixNano(), 10) + "|" + id
	sig := s.cursorMAC(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// decodeCursor parses + verifies a cursor produced by encodeCursor. Returns
// ErrCursorInvalid for malformed input or HMAC mismatch so the caller can
// fail closed.
func (s *GormStore) decodeCursor(cursor string) (time.Time, string, error) {
	idx := strings.IndexByte(cursor, '.')
	if idx < 0 {
		return time.Time{}, "", ErrCursorInvalid
	}
	payloadB64 := cursor[:idx]
	sigB64 := cursor[idx+1:]

	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return time.Time{}, "", ErrCursorInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return time.Time{}, "", ErrCursorInvalid
	}
	expected := s.cursorMAC(string(payload))
	if subtle.ConstantTimeCompare(sig, expected) != 1 {
		return time.Time{}, "", ErrCursorInvalid
	}

	pipeIdx := strings.IndexByte(string(payload), '|')
	if pipeIdx < 0 {
		return time.Time{}, "", ErrCursorInvalid
	}
	nanos, err := strconv.ParseInt(string(payload[:pipeIdx]), 10, 64)
	if err != nil {
		return time.Time{}, "", ErrCursorInvalid
	}
	return time.Unix(0, nanos).UTC(), string(payload[pipeIdx+1:]), nil
}

// cursorMAC returns the HMAC-SHA256 of payload using the store's cursor
// secret. The first call lazily generates a process-local secret if none
// was supplied via [WithCursorSecret]; a one-time warning is emitted so
// operators see that cross-process pagination won't survive restart.
func (s *GormStore) cursorMAC(payload string) []byte {
	s.cursorSecretOnce.Do(func() {
		if s.cursorSecret != nil {
			return
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			panic("gormstore: failed to generate cursor secret: " + err.Error())
		}
		s.cursorSecret = key
		slog.Warn("gormstore: no cursor secret configured; generated process-local key (cursors will not survive process restart). Set WithCursorSecret in production for cross-process pagination.")
	})
	mac := hmac.New(sha256.New, s.cursorSecret)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// auditEvent is the GORM model for the audit_events table.
type auditEvent struct {
	ID        string          `gorm:"primaryKey;size:36"`
	Timestamp time.Time       `gorm:"index;not null"`
	Actor     string          `gorm:"index;size:255;not null"`
	Action    string          `gorm:"index;size:100;not null"`
	Resource  string          `gorm:"index;size:500;not null"`
	Status    string          `gorm:"size:50;not null"`
	Metadata  json.RawMessage `gorm:"type:jsonb"`
	TraceID   string          `gorm:"size:64"`
	IPAddress string          `gorm:"size:45"`
}

func (auditEvent) TableName() string { return "audit_events" }

// GormStore persists audit events in a relational database via GORM.
// It implements both [auditlog.Store] and [auditlog.RetentionStore].
type GormStore struct {
	db *gorm.DB

	cursorSecretOnce sync.Once
	cursorSecret     []byte
}

// Option configures a GormStore.
type Option func(*GormStore)

// WithCursorSecret sets the HMAC key used to sign pagination cursors.
// Cursors signed by one process can be verified by another that holds the
// same secret — required for any deployment where pagination requests may
// land on different replicas.
//
// Without this option, the store generates a per-process random secret on
// first use and emits a one-time warning. That's safe but means cursors
// produced by pod A are rejected by pod B; pagination will appear to break
// after a redeploy or load-balancer rotation.
//
// Panics if key is shorter than 32 bytes.
func WithCursorSecret(key []byte) Option {
	if len(key) < 32 {
		panic("gormstore: cursor secret must be at least 32 bytes")
	}
	return func(s *GormStore) {
		dup := make([]byte, len(key))
		copy(dup, key)
		s.cursorSecret = dup
	}
}

// New creates a GormStore backed by the given database.
func New(db *gorm.DB, opts ...Option) *GormStore {
	if db == nil {
		panic("gormstore: db must not be nil")
	}
	s := &GormStore{db: db}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Append persists an audit event.
func (s *GormStore) Append(ctx context.Context, event auditlog.Event) error {
	row := auditEvent{
		ID:        event.ID,
		Timestamp: event.Timestamp,
		Actor:     event.Actor,
		Action:    event.Action,
		Resource:  event.Resource,
		Status:    event.Status,
		Metadata:  event.Metadata,
		TraceID:   event.TraceID,
		IPAddress: event.IPAddress,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("gormstore: append: %w", err)
	}
	return nil
}

// Query returns events matching the filter with cursor-based pagination.
func (s *GormStore) Query(ctx context.Context, filter auditlog.Filter, cursor string, limit int) ([]auditlog.Event, string, error) {
	if limit <= 0 {
		limit = 50
	}

	q := s.db.WithContext(ctx).Model(&auditEvent{}).Order("timestamp DESC, id DESC")

	if filter.Actor != "" {
		q = q.Where("actor = ?", filter.Actor)
	}
	if filter.Action != "" {
		q = q.Where("action = ?", filter.Action)
	}
	if filter.Resource != "" {
		q = q.Where(`resource LIKE ? ESCAPE '\'`, likeEscape(filter.Resource)+"%")
	}
	if !filter.Since.IsZero() {
		q = q.Where("timestamp >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		q = q.Where("timestamp <= ?", filter.Until)
	}
	if filter.IPAddress != "" {
		q = q.Where("ip_address = ?", filter.IPAddress)
	}
	if cursor != "" {
		ts, id, err := s.decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		// Composite (timestamp, id) DESC pagination. Avoid the row-tuple
		// `(timestamp, id) < (?, ?)` form because not every dialect (notably
		// older MySQL) supports it consistently; the OR-expanded form below
		// is dialect-portable.
		q = q.Where("timestamp < ? OR (timestamp = ? AND id < ?)", ts, ts, id)
	}

	// Fetch limit+1 to detect if there are more pages.
	var rows []auditEvent
	if err := q.Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("gormstore: query: %w", err)
	}

	var nextCursor string
	if len(rows) > limit {
		last := rows[limit-1]
		nextCursor = s.encodeCursor(last.Timestamp, last.ID)
		rows = rows[:limit]
	}

	events := make([]auditlog.Event, len(rows))
	for i, r := range rows {
		events[i] = auditlog.Event{
			ID:        r.ID,
			Timestamp: r.Timestamp,
			Actor:     r.Actor,
			Action:    r.Action,
			Resource:  r.Resource,
			Status:    r.Status,
			Metadata:  r.Metadata,
			TraceID:   r.TraceID,
			IPAddress: r.IPAddress,
		}
	}
	return events, nextCursor, nil
}

// retentionBatchSize is the maximum number of events deleted per batch
// to avoid long-running transactions.
const retentionBatchSize = 1000

// DeleteBefore removes all events with a timestamp before the given time.
// Deletes in batches to avoid long-running transactions.
//
// Uses SELECT-then-DELETE-by-id rather than `DELETE ... LIMIT`: PostgreSQL
// does not support DELETE with LIMIT, and GORM's Limit clause silently
// drops on dialects that reject it — letting a single DeleteBefore wipe
// the whole eligible range in one transaction. The two-step form is
// dialect-portable and guarantees the configured batch size.
func (s *GormStore) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}

		var ids []string
		if err := s.db.WithContext(ctx).
			Model(&auditEvent{}).
			Where("timestamp < ?", before).
			Order("timestamp ASC").
			Limit(retentionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, fmt.Errorf("gormstore: delete before (select): %w", err)
		}
		if len(ids) == 0 {
			break
		}

		result := s.db.WithContext(ctx).
			Where("id IN ?", ids).
			Delete(&auditEvent{})
		if result.Error != nil {
			return total, fmt.Errorf("gormstore: delete before: %w", result.Error)
		}
		total += result.RowsAffected
		if len(ids) < retentionBatchSize {
			break
		}
	}
	return total, nil
}
