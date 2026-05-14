package contextutil

import "context"

// requestID is a named string type for request ID context uniqueness.
type requestID string

// ridKey is the context key for request IDs.
var ridKey = NewKey[requestID]("request_id")

// MaxRequestIDLen caps the byte length of a request ID at the
// boundary so a hostile inbound header cannot carry an unbounded
// string through the kit's log/metric paths. 128 bytes is comfortably
// above UUID, ULID, and conventional traceparent IDs.
const MaxRequestIDLen = 128

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
