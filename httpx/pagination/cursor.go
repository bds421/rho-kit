package pagination

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// CursorParams holds cursor-based pagination parameters.
type CursorParams struct {
	Cursor string // last ID from previous page (empty = first page)
	Limit  int
}

// CursorResult is the standard cursor-based pagination response envelope.
type CursorResult[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// ParseCursorParams extracts cursor and limit from query parameters.
// Clamps limit between 1 and maxLimit, defaulting to defaultLimit.
func ParseCursorParams(r *http.Request, defaultLimit, maxLimit int) CursorParams {
	q := r.URL.Query()

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	return CursorParams{
		Cursor: q.Get("cursor"),
		Limit:  limit,
	}
}

// BuildResult constructs a CursorResult from a slice fetched with limit+1.
// extractID returns the cursor value from an item (typically the ID field).
func BuildResult[T any](items []T, limit int, extractID func(T) string) CursorResult[T] {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = extractID(items[len(items)-1])
	}

	return CursorResult[T]{
		Data:       items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}
}

// CursorValidator is a function that validates cursor strings.
// Return nil if the cursor is valid, or an error describing the issue.
type CursorValidator func(cursor string) error

// ValidateCursorUUID validates that a cursor string is a valid UUID.
// Returns nil for empty cursors (first page). Returns an error for malformed values.
// This is the default validator for UUID-based primary keys.
func ValidateCursorUUID(cursor string) error {
	if cursor == "" {
		return nil
	}
	if _, err := uuid.Parse(cursor); err != nil {
		return fmt.Errorf("invalid cursor format: must be a valid UUID: %w", err)
	}
	return nil
}
