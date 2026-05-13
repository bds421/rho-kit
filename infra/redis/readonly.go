package redis

import (
	"errors"
	"strings"

	goredis "github.com/redis/go-redis/v9"
)

// ErrPrimaryReadOnly is returned when a Redis primary has been demoted to a
// replica (or otherwise marked READONLY) and rejects a write command with the
// "READONLY ..." reply.
//
// In a Sentinel / Cluster topology this is the canonical signal that a
// failover is in progress: the node accepting commands no longer holds the
// master role. Callers can match this with errors.Is so degradation
// strategies (read-only fallback, fail-fast on the write path) can react
// without parsing error strings themselves.
var ErrPrimaryReadOnly = errors.New("redis: primary is read-only")

// IsReadOnlyError reports whether err originated from a Redis "READONLY ..."
// server reply. The check is performed against the wrapped [goredis.Error]
// chain when possible, falling back to a prefix scan of the rendered
// message — server replies and connection-pool reconstructions both reach
// callers as goredis.Error values.
func IsReadOnlyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPrimaryReadOnly) {
		return true
	}
	var rerr goredis.Error
	if errors.As(err, &rerr) {
		msg := strings.TrimPrefix(rerr.Error(), "ERR ")
		if strings.HasPrefix(msg, "READONLY") {
			return true
		}
	}
	// Defensive fallback: some client wrappers stringify the Redis reply
	// into a plain error before reaching us.
	if strings.HasPrefix(err.Error(), "READONLY") {
		return true
	}
	return false
}
