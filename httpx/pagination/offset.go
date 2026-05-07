package pagination

import (
	"net/http"
	"strconv"
)

// OffsetParams holds offset-based pagination parameters parsed from a
// request's query string.
type OffsetParams struct {
	Limit  int
	Offset int
}

// ParseOffset reads ?limit= and ?offset= from r and applies bounds:
//
//   - limit defaults to defaultLimit when missing, non-positive, or
//     unparseable, and is clamped to maxLimit
//   - offset defaults to defaultOffset when missing or unparseable, and is
//     clamped to a non-negative value
//
// Bounds-clamping is intentional: classical offset pagination bug reports
// always trace back to a missing or zero-or-negative limit allowing a
// runaway scan, or a maxLimit that wasn't enforced.
//
// Returns (limit, offset) in that order. The OffsetParams struct is also
// exposed for callers that prefer named fields.
func ParseOffset(r *http.Request, defaultLimit, defaultOffset, maxLimit int) (limit, offset int) {
	q := r.URL.Query()

	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = defaultLimit
	}
	if maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}

	offset, err = strconv.Atoi(q.Get("offset"))
	if err != nil {
		offset = defaultOffset
	}
	if offset < 0 {
		offset = 0
	}

	return limit, offset
}
