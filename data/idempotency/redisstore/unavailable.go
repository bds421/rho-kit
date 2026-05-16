package redisstore

import (
	"errors"
	"net"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrStoreUnavailable is returned by [Store] when the underlying Redis
// connection is reachable but cannot service the request — typically because
// a Sentinel/Cluster failover demoted the primary to a replica (READONLY
// reply), the connection pool is exhausted, or the client returned a
// transport-level dial error.
//
// The sentinel is an [apperror.UnavailableError] tagged with the
// "idempotency" dependency name. Each call site that returns
// "store unavailable" wraps a fresh instance carrying the original cause;
// use [IsStoreUnavailable] (or apperror.IsUnavailable / apperror.AsUnavailable
// inspecting Dependency=="idempotency") to detect it. Transport adapters
// render it as 502 + Retry-After without exposing internal details.
var ErrStoreUnavailable = apperror.NewDependencyUnavailable(
	"idempotency",
	"idempotency store is temporarily unavailable",
	nil,
)

// IsStoreUnavailable reports whether err is (or wraps) an
// [apperror.UnavailableError] produced by this store. It matches both the
// exported [ErrStoreUnavailable] sentinel and per-call instances created
// during error translation.
func IsStoreUnavailable(err error) bool {
	if err == nil {
		return false
	}
	ue, ok := apperror.AsUnavailable(err)
	if !ok {
		return false
	}
	return ue.Dependency == "idempotency"
}

// isReadOnlyError reports whether err originated from a Redis "READONLY"
// server reply (primary demoted to replica during failover). The check is
// duplicated here so the redisstore module avoids a hard dependency on the
// infra/redis package.
func isReadOnlyError(err error) bool {
	if err == nil {
		return false
	}
	var rerr goredis.Error
	if errors.As(err, &rerr) {
		msg := strings.TrimPrefix(rerr.Error(), "ERR ")
		if strings.HasPrefix(msg, "READONLY") {
			return true
		}
	}
	if strings.HasPrefix(err.Error(), "READONLY") {
		return true
	}
	return false
}

// isConnectionUnavailable reports whether err indicates the Redis client
// cannot reach the server at all (closed pool, dial failure, timeout, or
// EOF). These errors are operationally indistinguishable from a brief
// outage and are mapped to [ErrStoreUnavailable].
func isConnectionUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, goredis.ErrClosed) {
		return true
	}
	if errors.Is(err, goredis.ErrPoolTimeout) {
		return true
	}
	if errors.Is(err, goredis.ErrPoolExhausted) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// translateUnavailable converts transport-level unavailability into
// [ErrStoreUnavailable] while passing other errors through unchanged. The
// original cause is wrapped via apperror.NewDependencyUnavailable so
// errors.Is(translated, ErrStoreUnavailable) holds and the cause remains
// available to operators via errors.Unwrap.
//
// Implementation note: ErrStoreUnavailable is a singleton *UnavailableError
// pointer, so errors.Is would not match a freshly-allocated
// NewDependencyUnavailable. The fix is to double-wrap with %w so the
// wrap chain contains both the sentinel pointer (for errors.Is) and the
// kit-classified UnavailableError (so HTTP/gRPC adapters still map to 503
// via the apperror.UnavailableError type assertion).
func translateUnavailable(err error) error {
	if err == nil {
		return nil
	}
	if isReadOnlyError(err) || isConnectionUnavailable(err) {
		classified := apperror.NewDependencyUnavailable(
			"idempotency",
			"idempotency store is temporarily unavailable",
			err,
		)
		return redact.WrapSentinel(ErrStoreUnavailable, classified)
	}
	return err
}
