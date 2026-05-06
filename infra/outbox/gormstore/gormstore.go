// Package gormstore provides a GORM-backed implementation of [outbox.Store].
// It works with any database supported by GORM (PostgreSQL, MySQL 8.0+,
// SQLite). Concurrent relay operation uses SELECT FOR UPDATE SKIP LOCKED
// which is supported by PostgreSQL 9.5+ and MySQL 8.0+.
//
// # Transaction Participation
//
// [Store.Insert] transparently participates in ambient GORM transactions via
// [gormdb.DBFromContext]. To guarantee atomicity between domain writes and
// outbox entries, wrap both in a single transaction using [gormdb.ContextWithTx]:
//
//	err := db.Transaction(func(tx *gorm.DB) error {
//	    ctx := gormdb.ContextWithTx(ctx, tx)
//
//	    // Domain write — uses the same transaction.
//	    if err := tx.Create(&order).Error; err != nil {
//	        return err
//	    }
//
//	    // Outbox write — also uses the same transaction.
//	    return writer.Write(ctx, outbox.WriteParams{
//	        Topic:       "orders",
//	        RoutingKey:  "order.created",
//	        MessageID:   msg.ID,
//	        MessageType: "order.created",
//	        Payload:     payload,
//	    })
//	})
//
// When no transaction is in context, Insert uses the root DB connection.
// All other store methods (FetchPending, MarkPublished, etc.) always use
// the root DB connection because they are called by the relay, not by
// user code inside transactions.
package gormstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/bds421/rho-kit/infra/outbox"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

// entry is the GORM model for an outbox row. Tags use portable types
// compatible with PostgreSQL, MySQL, and SQLite.
type entry struct {
	ID          uuid.UUID       `gorm:"type:varchar(36);primaryKey"`
	Topic       string          `gorm:"not null"`
	RoutingKey  string          `gorm:"not null;column:routing_key"`
	MessageID   string          `gorm:"not null;column:message_id"`
	MessageType string          `gorm:"not null;column:message_type"`
	Payload     json.RawMessage `gorm:"type:text;not null"`
	Headers     json.RawMessage `gorm:"type:text"`
	Status      outbox.Status   `gorm:"type:varchar(20);not null;default:pending;index:idx_outbox_pending,priority:1,where:status='pending'"`
	Attempts    int             `gorm:"not null;default:0"`
	LastError   *string         `gorm:"column:last_error"`
	CreatedAt   time.Time       `gorm:"not null;autoCreateTime"`
	UpdatedAt   time.Time       `gorm:"not null;autoUpdateTime"`
	PublishedAt *time.Time      `gorm:"column:published_at"`
	NextRetryAt *time.Time      `gorm:"column:next_retry_at"`
}

// TableName returns the database table name for GORM.
func (entry) TableName() string {
	return "outbox_entries"
}

// Compile-time interface check.
var _ outbox.Store = (*Store)(nil)

// Store implements outbox.Store using GORM. It works with any database
// supported by GORM (PostgreSQL, MySQL 8.0+, SQLite). It uses SELECT FOR
// UPDATE SKIP LOCKED with an atomic claim pattern to prevent concurrent
// relay instances from processing the same entries.
type Store struct {
	db       *gorm.DB
	logger   *slog.Logger
	isSQLite bool
	sqliteMu sync.Mutex // serialises FetchPending on SQLite (no SKIP LOCKED)
}

// StoreOption configures the Store. None are required for default behaviour.
type StoreOption func(*Store)

// WithLogger sets the logger used for one-off warnings (e.g. SQLite
// dialect detection). Defaults to slog.Default().
func WithLogger(l *slog.Logger) StoreOption {
	return func(s *Store) {
		if l != nil {
			s.logger = l
		}
	}
}

// New creates a Store backed by the given GORM database.
// Panics if db is nil.
//
// Auto-detects SQLite to enable a process-local FetchPending mutex —
// SQLite ignores SELECT FOR UPDATE SKIP LOCKED so two relay instances
// against the same SQLite file would each fetch the full pending set
// and double-publish. The mutex is in-process; SQLite is fundamentally
// unsuitable for cross-process relays anyway, so the kit emits a loud
// warning when SQLite is detected.
func New(db *gorm.DB, opts ...StoreOption) *Store {
	if db == nil {
		panic("gormstore: New requires a non-nil *gorm.DB")
	}
	s := &Store{db: db, logger: slog.Default()}
	for _, opt := range opts {
		opt(s)
	}
	if name := db.Dialector.Name(); name == "sqlite" || name == "sqlite3" {
		s.isSQLite = true
		s.logger.Warn("outbox/gormstore: SQLite detected — SKIP LOCKED is a no-op on SQLite. " +
			"FetchPending is serialised by an in-process mutex; multiple OS processes against the " +
			"same SQLite file WILL double-publish. Use Postgres or MySQL 8.0+ for multi-relay deployments.")
	}
	return s
}

// Insert creates a new outbox entry. If the context carries a GORM
// transaction (via gormdb.ContextWithTx), the insert happens within that
// transaction — guaranteeing atomicity with the caller's business data.
// When no transaction is in context, it falls back to the root DB connection.
func (s *Store) Insert(ctx context.Context, e outbox.Entry) error {
	db := gormdb.DBFromContext(ctx, s.db)
	row := toRow(e)
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("gormstore: insert entry: %w", err)
	}
	return nil
}

// FetchPending atomically claims up to limit pending entries by setting their
// status to "processing" within a single transaction. It uses SELECT FOR UPDATE
// SKIP LOCKED to allow multiple relay instances to poll concurrently without
// processing the same entries.
//
// On SQLite the SKIP LOCKED clause is silently ignored, so the kit
// serialises FetchPending with a process-local mutex (see [New]). The
// guard prevents two in-process relays from colliding; cross-process
// SQLite relays will still double-process and should not be used.
func (s *Store) FetchPending(ctx context.Context, limit int) ([]outbox.Entry, error) {
	if s.isSQLite {
		s.sqliteMu.Lock()
		defer s.sqliteMu.Unlock()
	}

	var rows []entry

	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", outbox.StatusPending, now).
			Order("created_at ASC").
			Limit(limit).
			Clauses(clause.Locking{
				Strength: "UPDATE",
				Options:  "SKIP LOCKED",
			}).
			Find(&rows).Error; err != nil {
			return err
		}

		if len(rows) == 0 {
			return nil
		}

		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}

		if err := tx.
			Model(&entry{}).
			Where("id IN ?", ids).
			Update("status", outbox.StatusProcessing).Error; err != nil {
			return err
		}

		// Reflect the database state in the in-memory rows.
		for i := range rows {
			rows[i].Status = outbox.StatusProcessing
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gormstore: fetch pending: %w", err)
	}

	return toEntries(rows), nil
}

// MarkPublished updates the entry status to published.
func (s *Store) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&entry{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":       outbox.StatusPublished,
			"published_at": publishedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("gormstore: mark published %s: %w", id, result.Error)
	}
	return nil
}

// MarkFailed updates the entry status to failed.
func (s *Store) MarkFailed(ctx context.Context, id string, lastError string) error {
	result := s.db.WithContext(ctx).
		Model(&entry{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     outbox.StatusFailed,
			"last_error": lastError,
		})
	if result.Error != nil {
		return fmt.Errorf("gormstore: mark failed %s: %w", id, result.Error)
	}
	return nil
}

// IncrementAttempts bumps the attempt counter, records the error, schedules
// the next retry attempt at nextRetryAt, and resets the entry status to
// pending. FetchPending will skip the row until that timestamp passes, giving
// the relay exponential backoff behavior across consecutive failures.
func (s *Store) IncrementAttempts(ctx context.Context, id string, lastError string, nextRetryAt time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&entry{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":        outbox.StatusPending,
			"attempts":      gorm.Expr("attempts + 1"),
			"last_error":    lastError,
			"next_retry_at": nextRetryAt.UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("gormstore: increment attempts %s: %w", id, result.Error)
	}
	return nil
}

// DeletePublishedBefore removes published entries older than the cutoff.
func (s *Store) DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("status = ? AND published_at < ?", outbox.StatusPublished, before).
		Delete(&entry{})
	if result.Error != nil {
		return 0, fmt.Errorf("gormstore: delete published: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// DeleteFailedBefore removes entries that are stuck in StatusFailed (i.e. the
// relay exhausted max attempts) older than the cutoff. Without this, failed
// entries accumulate forever and bloat the table + indexes.
func (s *Store) DeleteFailedBefore(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("status = ? AND updated_at < ?", outbox.StatusFailed, before).
		Delete(&entry{})
	if result.Error != nil {
		return 0, fmt.Errorf("gormstore: delete failed: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// ResetStaleProcessing resets entries stuck in "processing" status back to
// "pending" if they have been in that state longer than staleDuration.
func (s *Store) ResetStaleProcessing(ctx context.Context, staleDuration time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-staleDuration)
	result := s.db.WithContext(ctx).
		Model(&entry{}).
		Where("status = ? AND updated_at < ?", outbox.StatusProcessing, cutoff).
		Update("status", outbox.StatusPending)
	if result.Error != nil {
		return 0, fmt.Errorf("gormstore: reset stale processing: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// CountPending returns the number of pending entries.
func (s *Store) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&entry{}).
		Where("status = ?", outbox.StatusPending).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("gormstore: count pending: %w", err)
	}
	return count, nil
}

// toRow converts an outbox.Entry to the GORM model.
func toRow(e outbox.Entry) entry {
	return entry{
		ID:          e.ID,
		Topic:       e.Topic,
		RoutingKey:  e.RoutingKey,
		MessageID:   e.MessageID,
		MessageType: e.MessageType,
		Payload:     e.Payload,
		Headers:     e.Headers,
		Status:      e.Status,
		Attempts:    e.Attempts,
		CreatedAt:   e.CreatedAt,
		PublishedAt: e.PublishedAt,
		NextRetryAt: e.NextRetryAt,
		LastError:   e.LastError,
	}
}

// toEntry converts a GORM model to an outbox.Entry.
func toEntry(r entry) outbox.Entry {
	return outbox.Entry{
		ID:          r.ID,
		Topic:       r.Topic,
		RoutingKey:  r.RoutingKey,
		MessageID:   r.MessageID,
		MessageType: r.MessageType,
		Payload:     r.Payload,
		Headers:     r.Headers,
		Status:      r.Status,
		Attempts:    r.Attempts,
		CreatedAt:   r.CreatedAt,
		PublishedAt: r.PublishedAt,
		NextRetryAt: r.NextRetryAt,
		LastError:   r.LastError,
	}
}

// toEntries converts a slice of GORM models to outbox.Entry values.
func toEntries(rows []entry) []outbox.Entry {
	entries := make([]outbox.Entry, len(rows))
	for i, r := range rows {
		entries[i] = toEntry(r)
	}
	return entries
}
