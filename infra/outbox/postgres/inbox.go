package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/health"
)

const (
	maxConsumerNameBytes = 128
	maxMessageIDBytes    = 255
)

var (
	ErrInvalidConsumer = errors.New("inbox/postgres: invalid consumer name")
	ErrInvalidMessage  = errors.New("inbox/postgres: invalid message id")
)

// Result describes the durable claim outcome. Duplicate is a normal success:
// the callback was not run because this consumer already committed the message.
type InboxResult struct {
	Duplicate bool
}

// Handler performs local work using the transaction carried by ctx. It must
// return an error rather than acknowledging a broker delivery itself; callers
// ACK only after Process returns a committed non-error result.
type InboxHandler func(ctx context.Context) error

type inboxPool interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Store is safe for concurrent use. The backing inbox_entries table is shared
// across replicas; PostgreSQL's unique key serializes competing first claims.
type Inbox struct {
	pool    inboxPool
	metrics *InboxMetrics
}

// InboxOption configures an Inbox.
type InboxOption func(*Inbox)

// WithInboxMetrics supplies dedicated durable-inbox metrics.
func WithInboxMetrics(metrics *InboxMetrics) InboxOption {
	if metrics == nil {
		panic("outbox/postgres: WithInboxMetrics requires non-nil metrics")
	}
	return func(s *Inbox) { s.metrics = metrics }
}

// New constructs a Store from a pgx pool. Panics on nil so a missing durable
// store fails during service wiring rather than on the first delivery.
func NewInbox(p *pgxpool.Pool, opts ...InboxOption) *Inbox {
	if p == nil {
		panic("outbox/postgres: NewInbox requires a non-nil pool")
	}
	s := &Inbox{pool: p, metrics: NewInboxMetrics()}
	for _, opt := range opts {
		if opt == nil {
			panic("outbox/postgres: Inbox option must not be nil")
		}
		opt(s)
	}
	return s
}

func newInboxWithPool(p inboxPool) *Inbox { return &Inbox{pool: p, metrics: NewInboxMetrics()} }

// Process atomically records a delivery and runs handler. It joins an existing
// outbox/postgres transaction when one is present; in that mode the caller owns
// commit/rollback. Otherwise Process owns a transaction and commits only after
// the handler succeeds.
func (s *Inbox) Process(ctx context.Context, consumer, messageID string, handler InboxHandler) (InboxResult, error) {
	if err := s.ready(); err != nil {
		return InboxResult{}, err
	}
	if err := validate(consumer, messageID, handler); err != nil {
		return InboxResult{}, err
	}
	if tx, ok := TxFromContext(ctx); ok {
		return s.processInTx(ctx, tx, consumer, messageID, handler)
	}
	if ctx == nil {
		return InboxResult{}, errors.New("outbox/postgres: inbox context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return InboxResult{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InboxResult{}, redact.WrapError("outbox/postgres: inbox begin transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	result, err := s.processInTx(WithTx(ctx, tx), tx, consumer, messageID, handler)
	if err != nil {
		s.metrics.failure()
		return result, err
	}
	if result.Duplicate {
		s.metrics.duplicate()
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		s.metrics.failure()
		return InboxResult{}, redact.WrapError("outbox/postgres: inbox commit transaction", err)
	}
	committed = true
	s.metrics.processed()
	return result, nil
}

// HealthCheck reports whether the inbox database is reachable. It is critical
// because a consumer must not ACK new deliveries when it cannot durably record
// their receipt and local side effects.
func (s *Inbox) HealthCheck() health.DependencyCheck {
	return health.DependencyCheck{
		Name: "inbox-postgres",
		Check: func(ctx context.Context) string {
			if s == nil || s.pool == nil || ctx == nil {
				return health.StatusUnhealthy
			}
			var one int
			if err := s.pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
		Critical: true,
	}
}

// ProcessInTx requires a context carrying a pgx transaction installed with
// outbox/postgres.WithTx. It does not commit or roll back; it is for callers
// that already own a wider business transaction.
func (s *Inbox) ProcessInTx(ctx context.Context, consumer, messageID string, handler InboxHandler) (InboxResult, error) {
	if err := s.ready(); err != nil {
		return InboxResult{}, err
	}
	if err := validate(consumer, messageID, handler); err != nil {
		return InboxResult{}, err
	}
	tx, ok := TxFromContext(ctx)
	if !ok {
		return InboxResult{}, ErrNoTx
	}
	return s.processInTx(ctx, tx, consumer, messageID, handler)
}

func (s *Inbox) processInTx(ctx context.Context, tx pgx.Tx, consumer, messageID string, handler InboxHandler) (InboxResult, error) {
	const claim = `
INSERT INTO inbox_entries (consumer_name, message_id, received_at)
VALUES ($1, $2, NOW())
ON CONFLICT (consumer_name, message_id) DO NOTHING`
	tag, err := tx.Exec(ctx, claim, consumer, messageID)
	if err != nil {
		return InboxResult{}, redact.WrapError("outbox/postgres: inbox claim delivery", err)
	}
	if tag.RowsAffected() == 0 {
		return InboxResult{Duplicate: true}, nil
	}
	if err := handler(ctx); err != nil {
		return InboxResult{}, err
	}
	return InboxResult{}, nil
}

// PruneBefore deletes committed delivery receipts older than before. It uses
// the pool rather than a caller transaction because retention is a janitor
// concern and must not accidentally become part of message processing.
func (s *Inbox) PruneBefore(ctx context.Context, beforeTime time.Time) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if ctx == nil {
		return 0, errors.New("outbox/postgres: inbox context must not be nil")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM inbox_entries WHERE received_at < $1`, beforeTime.UTC())
	if err != nil {
		return 0, redact.WrapError("outbox/postgres: inbox prune", err)
	}
	return tag.RowsAffected(), nil
}

// Count returns the number of retained delivery receipts. It is principally
// useful for retention monitoring and integration assertions.
func (s *Inbox) Count(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if ctx == nil {
		return 0, errors.New("outbox/postgres: inbox context must not be nil")
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM inbox_entries`).Scan(&count); err != nil {
		return 0, redact.WrapError("outbox/postgres: inbox count", err)
	}
	return count, nil
}

func (s *Inbox) ready() error {
	if s == nil || s.pool == nil {
		return errors.New("outbox/postgres: inbox not initialized")
	}
	return nil
}

func validate(consumer, messageID string, handler InboxHandler) error {
	if handler == nil {
		return errors.New("outbox/postgres: inbox handler must not be nil")
	}
	if err := validateToken(consumer, maxConsumerNameBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConsumer, err)
	}
	if err := validateToken(messageID, maxMessageIDBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	return nil
}

func validateToken(value string, max int) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	if len(value) > max {
		return fmt.Errorf("exceeds %d bytes", max)
	}
	if !utf8.ValidString(value) {
		return errors.New("must be valid UTF-8")
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("contains whitespace or control characters")
		}
	}
	return nil
}
