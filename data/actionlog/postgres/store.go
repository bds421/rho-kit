package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/bds421/rho-kit/data/actionlog"
)

const defaultLimit = 100

// row is the GORM model for the action_log_entries table.
//
// Metadata is stored as JSONB. We marshal/unmarshal on the boundary so
// the store interface can pass map[string]any cleanly; the canonical
// signing form is the Logger's responsibility, not the store's.
type row struct {
	ID             string          `gorm:"primaryKey;size:36"`
	TenantID       string          `gorm:"size:255;not null;index:idx_action_log_entries_tenant_occurred,priority:1;uniqueIndex:idx_action_log_entries_tenant_seq,priority:1"`
	Actor          string          `gorm:"size:255;not null;index"`
	Action         string          `gorm:"size:255;not null;index"`
	Resource       string          `gorm:"size:500;not null;default:''"`
	Outcome        string          `gorm:"size:20;not null"`
	Reason         string          `gorm:"type:text;not null;default:''"`
	Metadata       json.RawMessage `gorm:"type:jsonb"`
	OccurredAt     time.Time       `gorm:"not null;index:idx_action_log_entries_tenant_occurred,priority:2,sort:desc"`
	SignatureKeyID string          `gorm:"size:64;not null;column:signature_key_id"`
	Seq            int64           `gorm:"not null;default:0;uniqueIndex:idx_action_log_entries_tenant_seq,priority:2"`
	PrevHash       string          `gorm:"size:64;not null;default:'';column:prev_hash"`
	Signature      string          `gorm:"size:128;not null"`
}

func (row) TableName() string { return "action_log_entries" }

// Store is a GORM-backed [actionlog.Store].
type Store struct {
	db *gorm.DB
}

// New returns a Store. Panics on a nil db — fail fast at startup so
// the failure is visible at boot rather than at first append.
func New(db *gorm.DB) *Store {
	if db == nil {
		panic("actionlog/postgres: db must not be nil")
	}
	return &Store{db: db}
}

// AppendChained runs build inside a transaction that holds a
// per-tenant advisory lock plus SELECT FOR UPDATE on the latest row
// for tenantID, persisting the resulting entry under the same lock
// so concurrent appends serialise — including the tenant's first
// append, where there is no row yet for SELECT FOR UPDATE to lock.
func (s *Store) AppendChained(ctx context.Context, tenantID string, build func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error)) (actionlog.Entry, error) {
	if tenantID == "" {
		return actionlog.Entry{}, actionlog.ErrInvalidEntry
	}
	isPostgres := s.db.Dialector.Name() == "postgres"
	var out actionlog.Entry
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// pg_advisory_xact_lock serialises concurrent first-appends for
		// the same tenant: SELECT FOR UPDATE has nothing to lock when
		// the tenant has zero rows, so two concurrent first-append calls
		// would otherwise both build seq=1 and one would fail the
		// (tenant_id, seq) unique constraint. The lock is released at
		// commit/rollback, so it never escapes this transaction.
		// Skipped on non-Postgres dialects (sqlite memdb tests); on
		// SQLite the connection-level serialisation plus the unique
		// index keep correctness.
		if isPostgres {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", tenantID).Error; err != nil {
				return fmt.Errorf("actionlog/postgres: advisory lock: %w", err)
			}
		}
		var latest row
		// SELECT FOR UPDATE the highest-Seq row for this tenant. On
		// dialects that elide row locking (sqlite via memdb), the
		// per-tenant unique index on (tenant_id, seq) still prevents
		// duplicate Seq values — the second concurrent insert fails
		// the unique constraint and the caller can retry.
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ?", tenantID).
			Order("seq DESC").
			Limit(1).
			Take(&latest).Error
		var (
			prev    actionlog.Entry
			prevSeq int64
		)
		if err == nil {
			prev, err = fromRow(latest)
			if err != nil {
				return err
			}
			prevSeq = latest.Seq
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		entry, err := build(prev, prevSeq)
		if err != nil {
			return err
		}
		r, err := toRow(entry)
		if err != nil {
			return err
		}
		if err := tx.Create(&r).Error; err != nil {
			return fmt.Errorf("actionlog/postgres: append: %w", err)
		}
		out = entry
		return nil
	})
	if err != nil {
		return actionlog.Entry{}, err
	}
	return out, nil
}

// Get returns the entry by id. Returns [actionlog.ErrNotFound] when
// no row matches.
func (s *Store) Get(ctx context.Context, id string) (actionlog.Entry, error) {
	var r row
	err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return actionlog.Entry{}, actionlog.ErrNotFound
		}
		return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: get: %w", err)
	}
	return fromRow(r)
}

// List returns entries matching q, ordered by occurred_at DESC, id DESC.
func (s *Store) List(ctx context.Context, q actionlog.Query) ([]actionlog.Entry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	tx := s.db.WithContext(ctx).Model(&row{}).Order("occurred_at DESC, id DESC")
	if q.TenantID != "" {
		tx = tx.Where("tenant_id = ?", q.TenantID)
	}
	if q.Actor != "" {
		tx = tx.Where("actor = ?", q.Actor)
	}
	if q.Action != "" {
		tx = tx.Where("action = ?", q.Action)
	}
	if !q.Since.IsZero() {
		tx = tx.Where("occurred_at >= ?", q.Since)
	}
	if !q.Until.IsZero() {
		tx = tx.Where("occurred_at <= ?", q.Until)
	}

	var rows []row
	if err := tx.Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("actionlog/postgres: list: %w", err)
	}

	out := make([]actionlog.Entry, 0, len(rows))
	for _, r := range rows {
		e, err := fromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// ListByTenantSeq returns every entry for tenantID ordered by Seq ASC.
// No limit is applied — VerifyChain needs the full chain.
func (s *Store) ListByTenantSeq(ctx context.Context, tenantID string) ([]actionlog.Entry, error) {
	var rows []row
	err := s.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("seq ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("actionlog/postgres: list by tenant seq: %w", err)
	}
	out := make([]actionlog.Entry, 0, len(rows))
	for _, r := range rows {
		e, err := fromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func toRow(e actionlog.Entry) (row, error) {
	var meta json.RawMessage
	if len(e.Metadata) > 0 {
		b, err := json.Marshal(e.Metadata)
		if err != nil {
			return row{}, fmt.Errorf("actionlog/postgres: marshal metadata: %w", err)
		}
		meta = b
	}
	return row{
		ID:             e.ID,
		TenantID:       e.TenantID,
		Actor:          e.Actor,
		Action:         e.Action,
		Resource:       e.Resource,
		Outcome:        string(e.Outcome),
		Reason:         e.Reason,
		Metadata:       meta,
		OccurredAt:     e.OccurredAt.UTC(),
		SignatureKeyID: e.SignatureKeyID,
		Seq:            e.Seq,
		PrevHash:       e.PrevHash,
		Signature:      e.Signature,
	}, nil
}

func fromRow(r row) (actionlog.Entry, error) {
	var meta map[string]any
	if len(r.Metadata) > 0 {
		if err := json.Unmarshal(r.Metadata, &meta); err != nil {
			return actionlog.Entry{}, fmt.Errorf("actionlog/postgres: unmarshal metadata: %w", err)
		}
	}
	// Storing UTC, but a sqlite roundtrip can come back without a
	// location. Force UTC so signature recomputation matches.
	return actionlog.Entry{
		ID:             r.ID,
		TenantID:       r.TenantID,
		Actor:          r.Actor,
		Action:         r.Action,
		Resource:       r.Resource,
		Outcome:        actionlog.Outcome(r.Outcome),
		Reason:         r.Reason,
		Metadata:       meta,
		OccurredAt:     r.OccurredAt.UTC(),
		SignatureKeyID: r.SignatureKeyID,
		Seq:            r.Seq,
		PrevHash:       r.PrevHash,
		Signature:      r.Signature,
	}, nil
}
