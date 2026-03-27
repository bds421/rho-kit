package outbox

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Compile-time interface check.
var _ Store = (*GormStore)(nil)

// GormStore implements Store using GORM with PostgreSQL.
// It uses SELECT ... FOR UPDATE SKIP LOCKED for safe concurrent polling.
type GormStore struct {
	db *gorm.DB
}

// NewGormStore creates a GormStore backed by the given GORM database.
// Panics if db is nil.
func NewGormStore(db *gorm.DB) *GormStore {
	if db == nil {
		panic("outbox: NewGormStore requires a non-nil *gorm.DB")
	}
	return &GormStore{db: db}
}

// Insert creates a new outbox entry within the provided transaction.
func (s *GormStore) Insert(_ context.Context, tx *gorm.DB, entry Entry) error {
	if err := tx.Create(&entry).Error; err != nil {
		return fmt.Errorf("outbox: insert entry: %w", err)
	}
	return nil
}

// FetchPending retrieves up to limit pending entries using SELECT FOR UPDATE
// SKIP LOCKED to allow multiple relay instances to poll concurrently without
// processing the same entries.
func (s *GormStore) FetchPending(ctx context.Context, limit int) ([]Entry, error) {
	var entries []Entry

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.
			Where("status = ?", StatusPending).
			Order("created_at ASC").
			Limit(limit).
			Clauses(clause.Locking{
				Strength: "UPDATE",
				Options:  "SKIP LOCKED",
			}).
			Find(&entries).Error
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: fetch pending: %w", err)
	}

	return entries, nil
}

// MarkPublished updates the entry status to published.
func (s *GormStore) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&Entry{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":       StatusPublished,
			"published_at": publishedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("outbox: mark published %s: %w", id, result.Error)
	}
	return nil
}

// MarkFailed updates the entry status to failed.
func (s *GormStore) MarkFailed(ctx context.Context, id string, lastError string) error {
	result := s.db.WithContext(ctx).
		Model(&Entry{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     StatusFailed,
			"last_error": lastError,
		})
	if result.Error != nil {
		return fmt.Errorf("outbox: mark failed %s: %w", id, result.Error)
	}
	return nil
}

// IncrementAttempts bumps the attempt counter and records the error.
func (s *GormStore) IncrementAttempts(ctx context.Context, id string, lastError string) error {
	result := s.db.WithContext(ctx).
		Model(&Entry{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"attempts":   gorm.Expr("attempts + 1"),
			"last_error": lastError,
		})
	if result.Error != nil {
		return fmt.Errorf("outbox: increment attempts %s: %w", id, result.Error)
	}
	return nil
}

// DeletePublishedBefore removes published entries older than the cutoff.
func (s *GormStore) DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("status = ? AND published_at < ?", StatusPublished, before).
		Delete(&Entry{})
	if result.Error != nil {
		return 0, fmt.Errorf("outbox: delete published: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// CountPending returns the number of pending entries.
func (s *GormStore) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&Entry{}).
		Where("status = ?", StatusPending).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("outbox: count pending: %w", err)
	}
	return count, nil
}
