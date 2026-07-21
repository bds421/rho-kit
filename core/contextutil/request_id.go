package contextutil

import "context"

// requestID is a named string type for request ID context uniqueness.
type requestID string

// ridKey is the context key for request IDs.
var ridKey = NewKey[requestID]("request_id")

// MaxRequestIDLen is an alias of [MaxCorrelationIDLen]: request and
// correlation identifiers share one length policy so middleware and
// context setters cannot drift. 128 bytes is comfortably above UUID,
// ULID, and conventional traceparent IDs.
//
// Prefer [MaxCorrelationIDLen] in new code; this name is retained for
// call sites that historically read the request-ID constant.
const MaxRequestIDLen = MaxCorrelationIDLen

// SetRequestID stores a request ID in the context. Empty IDs and IDs
// containing control characters or exceeding [MaxRequestIDLen] are
// silently dropped — surfacing them as panics would let an inbound
// header crash a service, and propagating them would let attackers
// influence log lines and metric labels.
//
// Wave 68 closed a hostile-review finding that arbitrary bytes flowed
// through this setter unchecked.
func SetRequestID(ctx context.Context, id string) context.Context {
	if !isValidContextID(id) {
		return ctx
	}
	return ridKey.Set(ctx, requestID(id))
}

// RequestID extracts the request ID from the context.
// Returns empty string if not set.
func RequestID(ctx context.Context) string {
	v, ok := ridKey.Get(ctx)
	if !ok {
		return ""
	}
	return string(v)
}

// isValidContextID is shared between request and correlation IDs and
// enforces the on-the-wire baseline. Empty IDs are rejected (so an
// empty header does not look "set"); bytes outside printable ASCII
// (excluding the space/control range) are rejected to keep IDs
// log/metric-safe.
//
// This baseline is intentionally a looser superset of
// [IsValidCorrelationToken] (which only admits [A-Za-z0-9._-]). The kit's
// own boundary middleware (httpx, grpcx) gates inbound headers on the
// stricter token policy before calling the setters; this baseline is the
// second line of defense for direct in-process callers, rejecting only the
// bytes that are unsafe in log lines and metric labels (control bytes,
// non-ASCII) while still permitting application-defined identifier formats.
func isValidContextID(id string) bool {
	if id == "" || len(id) > MaxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}
