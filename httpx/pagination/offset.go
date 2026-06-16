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
// ParseOffset leaves the offset upper-bound to the caller: any non-negative
// client offset is accepted. For endpoints backed by a relational store this
// invites the classic deep-offset scan (e.g. ?offset=9223372036854775807),
// the offset-side equivalent of the runaway-limit bug. Use
// [ParseOffsetWithMax] when you want the kit to cap the offset too.
//
// Returns (limit, offset, error) in that order. The OffsetParams struct is
// also exposed for callers that prefer named fields.
func ParseOffset(r *http.Request, defaultLimit, defaultOffset, maxLimit int) (limit, offset int, err error) {
	return ParseOffsetWithMax(r, defaultLimit, defaultOffset, maxLimit, 0)
}

// ParseOffsetWithMax behaves like [ParseOffset] but additionally clamps the
// resolved offset to maxOffset. A maxOffset of 0 (or negative) disables the
// offset cap, making the call identical to [ParseOffset] — so existing
// behaviour is preserved when callers opt out.
//
// Capping the offset closes the deep-offset scan: a relational OFFSET N forces
// the engine to walk and discard N rows, so an unbounded client offset is a
// cheap denial-of-service against an otherwise well-behaved endpoint. Bound it
// the same way you bound limit.
//
// defaultOffset must itself be within [0, maxOffset] when a cap is set;
// a defaultOffset above maxOffset is a configuration error and returns
// [ErrInvalidOffsetConfig].
func ParseOffsetWithMax(r *http.Request, defaultLimit, defaultOffset, maxLimit, maxOffset int) (limit, offset int, err error) {
	if defaultLimit <= 0 {
		return 0, 0, fmt.Errorf("%w: defaultLimit must be positive", ErrInvalidLimitConfig)
	}
	if maxLimit <= 0 {
		return 0, 0, fmt.Errorf("%w: maxLimit must be positive", ErrInvalidLimitConfig)
	}
	if defaultOffset < 0 {
		return 0, 0, fmt.Errorf("%w: defaultOffset must be non-negative", ErrInvalidOffsetConfig)
	}
	if maxOffset > 0 && defaultOffset > maxOffset {
		return 0, 0, fmt.Errorf("%w: defaultOffset must not exceed maxOffset", ErrInvalidOffsetConfig)
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
	if maxOffset > 0 && offset > maxOffset {
		offset = maxOffset
	}

	return limit, offset, nil
}
