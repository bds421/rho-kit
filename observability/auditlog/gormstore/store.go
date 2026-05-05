package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

// encodeCursor packs (timestamp, id) into the opaque pagination cursor.
// Composite cursor is necessary because the query orders by (timestamp DESC,
// id DESC) — using id alone caused rows to be skipped or duplicated whenever
// timestamps tied or IDs were not perfectly correlated with timestamps.
func encodeCursor(ts time.Time, id string) string {
	return strconv.FormatInt(ts.UnixNano(), 10) + "|" + id
}

// decodeCursor parses a cursor produced by encodeCursor. Returns (zero, "",
// error) for malformed input so the caller can fail closed.
func decodeCursor(cursor string) (time.Time, string, error) {
	idx := strings.IndexByte(cursor, '|')
	if idx < 0 {
		return time.Time{}, "", fmt.Errorf("gormstore: invalid cursor format")
	}
	nanos, err := strconv.ParseInt(cursor[:idx], 10, 64)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("gormstore: invalid cursor timestamp: %w", err)
	}
	return time.Unix(0, nanos).UTC(), cursor[idx+1:], nil
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
}

// New creates a GormStore backed by the given database.
func New(db *gorm.DB) *GormStore {
	if db == nil {
		panic("gormstore: db must not be nil")
	}
	return &GormStore{db: db}
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
		ts, id, err := decodeCursor(cursor)
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
		nextCursor = encodeCursor(last.Timestamp, last.ID)
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
func (s *GormStore) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		result := s.db.WithContext(ctx).
			Where("timestamp < ?", before).
			Limit(retentionBatchSize).
			Delete(&auditEvent{})
		if result.Error != nil {
			return total, fmt.Errorf("gormstore: delete before: %w", result.Error)
		}
		total += result.RowsAffected
		if result.RowsAffected < retentionBatchSize {
			break
		}
	}
	return total, nil
}
