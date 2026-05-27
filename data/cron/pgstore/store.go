package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/runtime/v2/cron"
)

// ErrScheduleNotFound is returned by Get when the named schedule does
// not exist.
var ErrScheduleNotFound = errors.New("pgstore: schedule not found")

// validIdent matches the same shape the kit's idempotency pgstore
// accepts for table names: alphanumeric + underscore, optional schema
// prefix.
var validIdent = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// validName matches job names: lowercase letters, digits, hyphens,
// underscores. Same alphabet as Prometheus label values to keep
// metric cardinality predictable.
var validName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,127}$`)

// ScheduleRecord is one persisted schedule.
type ScheduleRecord struct {
	Name        string
	Spec        string
	Enabled     bool
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store persists cron schedules to Postgres.
type Store struct {
	db    *sql.DB
	table string
}

// Option configures the Store.
type Option func(*Store)

// WithTableName overrides the default table "cron_schedules". Panics
// if the name is not a safe SQL identifier.
func WithTableName(name string) Option {
	if !validIdent.MatchString(name) {
		panic("pgstore: WithTableName requires a valid identifier")
	}
	return func(s *Store) { s.table = name }
}

// New constructs a Store. Panics on nil db (programmer error).
func New(db *sql.DB, opts ...Option) *Store {
	if db == nil {
		panic("pgstore: New requires a non-nil *sql.DB")
	}
	s := &Store{db: db, table: "cron_schedules"}
	for _, opt := range opts {
		if opt == nil {
			panic("pgstore: New option must not be nil")
		}
		opt(s)
	}
	return s
}

// Add inserts a new schedule. Returns an error if the name already
// exists (use [Store.Upsert] for create-or-update).
func (s *Store) Add(ctx context.Context, rec ScheduleRecord) error {
	if err := s.validate(rec); err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT INTO %s (name, spec, enabled, description)
		VALUES ($1, $2, $3, $4)`, s.table)
	_, err := s.db.ExecContext(ctx, query, rec.Name, rec.Spec, rec.Enabled, nullString(rec.Description))
	if err != nil {
		return redact.WrapError("pgstore: Add", err)
	}
	return nil
}

// Upsert inserts or updates a schedule by name.
func (s *Store) Upsert(ctx context.Context, rec ScheduleRecord) error {
	if err := s.validate(rec); err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT INTO %s (name, spec, enabled, description)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE SET
		  spec        = EXCLUDED.spec,
		  enabled     = EXCLUDED.enabled,
		  description = EXCLUDED.description,
		  updated_at  = now()`, s.table)
	_, err := s.db.ExecContext(ctx, query, rec.Name, rec.Spec, rec.Enabled, nullString(rec.Description))
	if err != nil {
		return redact.WrapError("pgstore: Upsert", err)
	}
	return nil
}

// Remove deletes a schedule by name. Returns nil on no-op (record did
// not exist).
func (s *Store) Remove(ctx context.Context, name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("pgstore: invalid name %q", name)
	}
	query := fmt.Sprintf(`DELETE FROM %s WHERE name = $1`, s.table)
	_, err := s.db.ExecContext(ctx, query, name)
	if err != nil {
		return redact.WrapError("pgstore: Remove", err)
	}
	return nil
}

// Enable sets the enabled flag on a schedule.
func (s *Store) Enable(ctx context.Context, name string, enabled bool) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("pgstore: invalid name %q", name)
	}
	query := fmt.Sprintf(`UPDATE %s SET enabled = $1, updated_at = now() WHERE name = $2`, s.table)
	result, err := s.db.ExecContext(ctx, query, enabled, name)
	if err != nil {
		return redact.WrapError("pgstore: Enable", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return redact.WrapError("pgstore: Enable rows", err)
	}
	if rows == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// Get returns a single schedule by name.
func (s *Store) Get(ctx context.Context, name string) (ScheduleRecord, error) {
	if !validName.MatchString(name) {
		return ScheduleRecord{}, fmt.Errorf("pgstore: invalid name %q", name)
	}
	query := fmt.Sprintf(`SELECT name, spec, enabled, COALESCE(description, ''), created_at, updated_at
		FROM %s WHERE name = $1`, s.table)
	row := s.db.QueryRowContext(ctx, query, name)
	var rec ScheduleRecord
	err := row.Scan(&rec.Name, &rec.Spec, &rec.Enabled, &rec.Description, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ScheduleRecord{}, ErrScheduleNotFound
		}
		return ScheduleRecord{}, redact.WrapError("pgstore: Get", err)
	}
	return rec, nil
}

// List returns all schedules in name-ASCII order.
func (s *Store) List(ctx context.Context) ([]ScheduleRecord, error) {
	query := fmt.Sprintf(`SELECT name, spec, enabled, COALESCE(description, ''), created_at, updated_at
		FROM %s ORDER BY name ASC`, s.table)
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, redact.WrapError("pgstore: List", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ScheduleRecord
	for rows.Next() {
		var rec ScheduleRecord
		if err := rows.Scan(&rec.Name, &rec.Spec, &rec.Enabled, &rec.Description, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, redact.WrapError("pgstore: List scan", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, redact.WrapError("pgstore: List rows", err)
	}
	return out, nil
}

// JobFunc is the handler signature runtime/cron.Scheduler.Add accepts.
// Re-exported here so callers building the jobs map don't need a
// runtime/cron import for type declarations alone.
type JobFunc = func(ctx context.Context) error

// ApplyTo registers every enabled schedule whose name appears in jobs
// on the supplied scheduler. Records with names absent from jobs are
// skipped with a warning channel — the scheduler is the source of
// truth on which job names the binary KNOWS, the store is the source
// of truth on which schedules are ACTIVE.
//
// Returns the names of stored-but-unknown schedules so the caller can
// log them (typically a CI/CD smoke check that catches "operator
// added a schedule for a job this binary doesn't ship").
func (s *Store) ApplyTo(ctx context.Context, scheduler *cron.Scheduler, jobs map[string]JobFunc) ([]string, error) {
	if scheduler == nil {
		return nil, errors.New("pgstore: ApplyTo requires non-nil scheduler")
	}
	if jobs == nil {
		return nil, errors.New("pgstore: ApplyTo requires non-nil jobs map")
	}
	records, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var unknown []string
	for _, rec := range records {
		if !rec.Enabled {
			continue
		}
		fn, ok := jobs[rec.Name]
		if !ok {
			unknown = append(unknown, rec.Name)
			continue
		}
		scheduler.Add(rec.Name, rec.Spec, fn)
	}
	return unknown, nil
}

func (s *Store) validate(rec ScheduleRecord) error {
	if !validName.MatchString(rec.Name) {
		return fmt.Errorf("pgstore: invalid Name %q (lowercase alphanumeric + - _, max 128)", rec.Name)
	}
	if rec.Spec == "" {
		return errors.New("pgstore: Spec is required")
	}
	if len(rec.Spec) > 128 {
		return errors.New("pgstore: Spec exceeds 128 chars")
	}
	return nil
}

func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
