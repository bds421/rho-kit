package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/bds421/rho-kit/data/approval"
)

const defaultLimit = 100

// row is the GORM model for the approval_requests table.
type row struct {
	ID        string          `gorm:"primaryKey;size:36"`
	TenantID  string          `gorm:"size:255;not null;index:idx_approval_requests_tenant_state,priority:1"`
	Actor     string          `gorm:"size:255;not null;index"`
	Action    string          `gorm:"size:255;not null"`
	Resource  string          `gorm:"size:500;not null;default:''"`
	Payload   json.RawMessage `gorm:"type:jsonb"`
	State     string          `gorm:"size:20;not null;index:idx_approval_requests_tenant_state,priority:2;index:idx_approval_requests_state_expires,priority:1"`
	DecidedBy string          `gorm:"size:255;not null;default:''"`
	DecidedAt *time.Time      `gorm:""`
	Reason    string          `gorm:"type:text;not null;default:''"`
	CreatedAt time.Time       `gorm:"not null"`
	ExpiresAt time.Time       `gorm:"not null;index:idx_approval_requests_state_expires,priority:2"`
}

func (row) TableName() string { return "approval_requests" }

// Store is a GORM-backed [approval.Store].
type Store struct {
	db    *gorm.DB
	clock func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock overrides the wall-clock used for the auto-expire branch
// inside Decide. Tests use this to make the late-approval branch
// deterministic. Panics on a nil fn.
func WithClock(fn func() time.Time) Option {
	if fn == nil {
		panic("approval/postgres: WithClock requires a non-nil function")
	}
	return func(s *Store) { s.clock = fn }
}

// New returns a Store backed by db. Panics on a nil db.
func New(db *gorm.DB, opts ...Option) *Store {
	if db == nil {
		panic("approval/postgres: db must not be nil")
	}
	s := &Store{db: db, clock: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create persists a new request in StatePending. Rejects requests
// without a future ExpiresAt — see validateForCreate.
func (s *Store) Create(ctx context.Context, r approval.Request) (approval.Request, error) {
	if err := validateForCreate(r, s.clock()); err != nil {
		return approval.Request{}, err
	}
	r.State = approval.StatePending
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.clock()
	}
	r.CreatedAt = r.CreatedAt.UTC()
	r.ExpiresAt = r.ExpiresAt.UTC()

	rr := toRow(r)
	if err := s.db.WithContext(ctx).Create(&rr).Error; err != nil {
		return approval.Request{}, fmt.Errorf("approval/postgres: create: %w", err)
	}
	return fromRow(rr), nil
}

// Get returns the request by id.
func (s *Store) Get(ctx context.Context, id string) (approval.Request, error) {
	var r row
	err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return approval.Request{}, approval.ErrNotFound
		}
		return approval.Request{}, fmt.Errorf("approval/postgres: get: %w", err)
	}
	return fromRow(r), nil
}

// List returns matching requests newest-first.
func (s *Store) List(ctx context.Context, q approval.Query) ([]approval.Request, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	tx := s.db.WithContext(ctx).Model(&row{}).Order("created_at DESC, id DESC")
	if q.TenantID != "" {
		tx = tx.Where("tenant_id = ?", q.TenantID)
	}
	if q.Actor != "" {
		tx = tx.Where("actor = ?", q.Actor)
	}
	if q.Action != "" {
		tx = tx.Where("action = ?", q.Action)
	}
	if q.State != "" {
		tx = tx.Where("state = ?", string(q.State))
	}
	if !q.Since.IsZero() {
		tx = tx.Where("created_at >= ?", q.Since)
	}
	if !q.Until.IsZero() {
		tx = tx.Where("created_at <= ?", q.Until)
	}
	var rows []row
	if err := tx.Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("approval/postgres: list: %w", err)
	}
	out := make([]approval.Request, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

// Decide records an approver's decision atomically.
//
// The state-transition logic mirrors data/approval/memory: idempotent
// for the same decision, refuses to flip a recorded decision, refuses
// to move out of a terminal state, auto-expires past-deadline pending
// requests.
func (s *Store) Decide(ctx context.Context, id, decidedBy, reason string, approve bool) (approval.Request, error) {
	if decidedBy == "" {
		return approval.Request{}, approval.ErrInvalidApprover
	}
	target := approval.StateApproved
	if !approve {
		target = approval.StateRejected
	}

	var (
		out        approval.Request
		expiredErr error
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r row
		// SELECT FOR UPDATE on supporting dialects; GORM elides for
		// sqlite. Either way, a concurrent Decide that lands on the
		// same row will serialise via the row lock or via the
		// optimistic re-read after commit.
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, "id = ?", id).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return approval.ErrNotFound
			}
			return err
		}

		now := s.clock().UTC()

		// Auto-expire branch: persist the state flip to expired AND
		// surface ErrInvalidTransition to the caller. We commit the
		// state change (return nil from the transaction) and stash
		// the error to return after the transaction succeeds — if
		// we returned the error from inside, GORM would roll back
		// the expired-row write and the next Decide would re-flip
		// it, which would defeat the implicit-expiry contract.
		if approval.State(r.State) == approval.StatePending && !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
			r.State = string(approval.StateExpired)
			r.DecidedAt = &now
			if err := tx.Save(&r).Error; err != nil {
				return err
			}
			expiredErr = fmt.Errorf("%w: request expired at %s", approval.ErrInvalidTransition, r.ExpiresAt.UTC().Format(time.RFC3339))
			return nil
		}

		current := approval.State(r.State)

		if current == target {
			// Idempotent; refresh decider/reason metadata.
			r.DecidedBy = decidedBy
			r.Reason = reason
			if err := tx.Save(&r).Error; err != nil {
				return err
			}
			out = fromRow(r)
			return nil
		}

		if current.IsTerminal() {
			return fmt.Errorf("%w: cannot transition out of %s", approval.ErrInvalidTransition, current)
		}

		if current == approval.StateApproved || current == approval.StateRejected {
			return fmt.Errorf("%w: cannot flip decision once recorded", approval.ErrInvalidTransition)
		}

		r.State = string(target)
		r.DecidedBy = decidedBy
		r.Reason = reason
		r.DecidedAt = &now

		if err := tx.Save(&r).Error; err != nil {
			return err
		}
		out = fromRow(r)
		return nil
	})

	if err != nil {
		if errors.Is(err, approval.ErrNotFound) || errors.Is(err, approval.ErrInvalidTransition) {
			return approval.Request{}, err
		}
		return approval.Request{}, fmt.Errorf("approval/postgres: decide: %w", err)
	}
	if expiredErr != nil {
		return approval.Request{}, expiredErr
	}
	return out, nil
}

// MarkExecuted moves an approved request to executed.
func (s *Store) MarkExecuted(ctx context.Context, id string) (approval.Request, error) {
	var out approval.Request
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r row
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, "id = ?", id).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return approval.ErrNotFound
			}
			return err
		}
		current := approval.State(r.State)
		if current == approval.StateExecuted {
			out = fromRow(r)
			return nil
		}
		if current != approval.StateApproved {
			return fmt.Errorf("%w: MarkExecuted requires source state %s, got %s", approval.ErrInvalidTransition, approval.StateApproved, current)
		}
		r.State = string(approval.StateExecuted)
		if err := tx.Save(&r).Error; err != nil {
			return err
		}
		out = fromRow(r)
		return nil
	})
	if err != nil {
		if errors.Is(err, approval.ErrNotFound) || errors.Is(err, approval.ErrInvalidTransition) {
			return approval.Request{}, err
		}
		return approval.Request{}, fmt.Errorf("approval/postgres: mark executed: %w", err)
	}
	return out, nil
}

func toRow(r approval.Request) row {
	var decidedAt *time.Time
	if !r.DecidedAt.IsZero() {
		t := r.DecidedAt.UTC()
		decidedAt = &t
	}
	return row{
		ID:        r.ID,
		TenantID:  r.TenantID,
		Actor:     r.Actor,
		Action:    r.Action,
		Resource:  r.Resource,
		Payload:   r.Payload,
		State:     string(r.State),
		DecidedBy: r.DecidedBy,
		DecidedAt: decidedAt,
		Reason:    r.Reason,
		CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: r.ExpiresAt.UTC(),
	}
}

func fromRow(r row) approval.Request {
	var decidedAt time.Time
	if r.DecidedAt != nil {
		decidedAt = r.DecidedAt.UTC()
	}
	return approval.Request{
		ID:        r.ID,
		TenantID:  r.TenantID,
		Actor:     r.Actor,
		Action:    r.Action,
		Resource:  r.Resource,
		Payload:   r.Payload,
		State:     approval.State(r.State),
		DecidedBy: r.DecidedBy,
		DecidedAt: decidedAt,
		Reason:    r.Reason,
		CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: r.ExpiresAt.UTC(),
	}
}

func validateForCreate(r approval.Request, now time.Time) error {
	if r.ID == "" || r.TenantID == "" || r.Actor == "" || r.Action == "" {
		return approval.ErrInvalidRequest
	}
	if r.State != "" && r.State != approval.StatePending {
		return approval.ErrInvalidRequest
	}
	if r.ExpiresAt.IsZero() {
		return approval.ErrInvalidRequest
	}
	if !r.ExpiresAt.After(now) {
		return approval.ErrInvalidRequest
	}
	return nil
}
