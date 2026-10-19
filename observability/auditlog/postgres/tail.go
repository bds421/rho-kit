package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

// Tail implements [auditlog.Tailer] over the monotonic seq column, the
// same order the HMAC chain is built in. Retention deletes a contiguous
// head, so a position older than the retained head simply resumes at it.
func (s *Store) Tail(ctx context.Context, tenant string, after auditlog.Position, limit int) ([]auditlog.Event, auditlog.Position, error) {
	if s == nil || s.pool == nil {
		return nil, after, errors.New("auditlog/postgres: store not initialized")
	}
	const q = `SELECT seq, id, occurred_at, actor, tenant, action, resource, status,
       ip_address, trace_id, metadata, signed_metadata, prev_hmac, hmac
  FROM audit_log_events
 WHERE seq > $1 AND ($2 = '' OR tenant = $2)
 ORDER BY seq ASC
 LIMIT $3`
	rows, err := s.pool.Query(ctx, q, int64(after), tenant, limit)
	if err != nil {
		return nil, after, fmt.Errorf("auditlog/postgres: tail: %w", err)
	}
	defer rows.Close()
	out := make([]auditlog.Event, 0, limit)
	last := after
	for rows.Next() {
		var seq int64
		event, err := scanEvent(withSeq{rows, &seq})
		if err != nil {
			return nil, after, fmt.Errorf("auditlog/postgres: tail scan: %w", err)
		}
		out = append(out, event)
		last = auditlog.Position(seq)
	}
	if err := rows.Err(); err != nil {
		return nil, after, fmt.Errorf("auditlog/postgres: tail iterate: %w", err)
	}
	return out, last, nil
}

type withSeq struct {
	rows pgx.Rows
	seq  *int64
}

func (w withSeq) Scan(dest ...any) error {
	return w.rows.Scan(append([]any{w.seq}, dest...)...)
}
