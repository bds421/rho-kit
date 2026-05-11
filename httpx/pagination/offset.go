package pagination

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// OffsetParams holds offset-based pagination parameters parsed from a
// request's query string.
type OffsetParams struct {
	Limit  int
	Offset int
}

// ErrInvalidOffsetConfig is returned by [ParseOffset] when the caller passes
// a negative default offset.
var ErrInvalidOffsetConfig = errors.New("pagination: invalid offset configuration")

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
// Returns (limit, offset, error) in that order. The OffsetParams struct is
// also exposed for callers that prefer named fields.
func ParseOffset(r *http.Request, defaultLimit, defaultOffset, maxLimit int) (limit, offset int, err error) {
	if defaultLimit <= 0 {
		return 0, 0, fmt.Errorf("%w: defaultLimit must be positive", ErrInvalidLimitConfig)
	}
	if maxLimit <= 0 {
		return 0, 0, fmt.Errorf("%w: maxLimit must be positive", ErrInvalidLimitConfig)
	}
	if defaultOffset < 0 {
		return 0, 0, fmt.Errorf("%w: defaultOffset must be non-negative", ErrInvalidOffsetConfig)
	}

	q, err := requestQuery(r)
	if err != nil {
		return 0, 0, err
	}

	rawLimit, err := singleQueryValue(q, "limit")
	if err != nil {
		return 0, 0, err
	}
	parsedLimit, parseErr := strconv.Atoi(rawLimit)
	if parseErr != nil || parsedLimit <= 0 {
		limit = defaultLimit
	} else {
		limit = parsedLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	rawOffset, err := singleQueryValue(q, "offset")
	if err != nil {
		return 0, 0, err
	}
	parsedOffset, parseErr := strconv.Atoi(rawOffset)
	if parseErr != nil {
		offset = defaultOffset
	} else {
		offset = parsedOffset
	}
	if offset < 0 {
		offset = 0
	}

	return limit, offset, nil
}
