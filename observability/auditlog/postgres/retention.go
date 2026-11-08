package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

// RetentionRecord reads the sealed record of what [Store.ApplyCut] removed
// from audit_log_retention: zero when nothing was removed yet,
// [auditlog.ErrRecordUnsealed] when its seal does not hold under key, an
// error for a key too short to seal. A
// role that only reads the chain may read it.
func (s *Store) RetentionRecord(ctx context.Context, key []byte) (auditlog.RetentionRecord, error) {
	if s == nil || s.pool == nil {
		return auditlog.RetentionRecord{}, errors.New("auditlog/postgres: store not initialized")
	}
	return readRecord(ctx, s.pool, key, false)
}

// ApplyCut removes the events c names and replaces the sealed record in
// one transaction, as [auditlog.RetentionStore] describes. It holds the
// chain lock and locks the record and the rows it checks. When ctx carries
// a transaction from [WithTx], it runs inside it and the chain lock is held
// until that ends. The role needs DELETE on audit_log_events and INSERT and
// UPDATE on audit_log_retention; nothing else should have them.
func (s *Store) ApplyCut(ctx context.Context, c auditlog.Cut, key []byte, minimum time.Duration, now time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("auditlog/postgres: store not initialized")
	}
	if err := c.Removable(minimum, now); err != nil {
		return 0, err
	}
	if joined, ok := auditlog.Joined(ctx); ok {
		tx, ok := joined.(pgx.Tx)
		if !ok {
			return 0, fmt.Errorf("auditlog/postgres: the joined transaction is a %T, not a pgx.Tx", joined)
		}
		return s.applyCut(ctx, tx, c, key, now)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("auditlog/postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	removed, err := s.applyCut(ctx, tx, c, key, now)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("auditlog/postgres: commit: %w", err)
	}
	return removed, nil
}

func (s *Store) applyCut(ctx context.Context, tx pgx.Tx, c auditlog.Cut, key []byte, now time.Time) (int64, error) {
	if err := s.lock(ctx, tx); err != nil {
		return 0, err
	}
	earlier, err := readRecord(ctx, tx, key, true)
	if err != nil || c.Events == 0 {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT seq, hmac FROM audit_log_events ORDER BY seq LIMIT $1 FOR UPDATE`, c.Events+1)
	if err != nil {
		return 0, fmt.Errorf("auditlog/postgres: read the cut: %w", err)
	}
	var seqs []int64
	var hmacs [][]byte
	for rows.Next() {
		var seq int64
		var mac []byte
		if err := rows.Scan(&seq, &mac); err != nil {
			rows.Close()
			return 0, fmt.Errorf("auditlog/postgres: read the cut: %w", err)
		}
		seqs, hmacs = append(seqs, seq), append(hmacs, mac)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("auditlog/postgres: read the cut: %w", err)
	}
	if err := c.Holds(hmacs); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM audit_log_events WHERE seq <= $1`, seqs[c.Events-1])
	if err != nil {
		return 0, fmt.Errorf("auditlog/postgres: remove the cut: %w", err)
	}
	if tag.RowsAffected() != c.Events {
		return 0, auditlog.ErrCutRefused
	}
	next := earlier.After(c, now)
	seal, err := next.Seal(key)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO audit_log_retention (id, watermark, removed, pruned_before, pruned_at, seal)
VALUES (1, $1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET watermark = $1, removed = $2, pruned_before = $3, pruned_at = $4, seal = $5`,
		next.Watermark, next.Removed, next.PrunedBefore, next.PrunedAt, seal); err != nil {
		return 0, fmt.Errorf("auditlog/postgres: write the retention record: %w", err)
	}
	return c.Events, nil
}

func readRecord(ctx context.Context, q rowQuerier, key []byte, lock bool) (auditlog.RetentionRecord, error) {
	query := `SELECT watermark, removed, pruned_before, pruned_at, seal FROM audit_log_retention WHERE id = 1`
	if lock {
		query += ` FOR UPDATE`
	}
	var r auditlog.RetentionRecord
	var seal []byte
	err := q.QueryRow(ctx, query).Scan(&r.Watermark, &r.Removed, &r.PrunedBefore, &r.PrunedAt, &seal)
	if errors.Is(err, pgx.ErrNoRows) {
		return auditlog.RetentionRecord{}, nil
	}
	if err != nil {
		return auditlog.RetentionRecord{}, fmt.Errorf("auditlog/postgres: read the retention record: %w", err)
	}
	r.PrunedBefore, r.PrunedAt = r.PrunedBefore.UTC(), r.PrunedAt.UTC()
	if err := r.CheckSeal(key, seal); err != nil {
		return auditlog.RetentionRecord{}, err
	}
	return r, nil
}
