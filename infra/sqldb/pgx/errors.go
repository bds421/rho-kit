package pgx

import (
	"context"
	"errors"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// transientClasses are the SQLSTATE classes of failures that pass on their
// own: connection exceptions (08), transaction rollbacks such as
// serialization failures and deadlocks (40), insufficient resources (53),
// operator intervention such as a server shutting down (57) and system
// errors (58).
var transientClasses = map[string]bool{"08": true, "40": true, "53": true, "57": true, "58": true}

// IsTransient reports whether err (or any error in its chain) is a failure
// that may pass if the same work is tried again unchanged: the context
// ended, the database could not be reached or timed out, the server
// refused the work for its own state (SQLSTATE classes 08, 40, 53, 57,
// 58), or pgx reports the statement was never sent. Every other database
// error — a data exception (22), an integrity violation (23), a syntax or
// permission error (42) — comes back on every try, so a consumer that
// retries only transient errors can set such work aside (dead-letter it)
// instead of retrying it forever. Returns false for nil.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return true
	}
	if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
		code := pg.SQLState()
		return len(code) == 5 && transientClasses[code[:2]]
	}
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}
	_, ok := errors.AsType[net.Error](err)
	return ok
}
