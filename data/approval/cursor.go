package approval

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// ErrInvalidCursor is returned by [Store.List] when [Query.Cursor] is
// malformed or its timestamp cannot be parsed. Callers should drop
// the cursor and restart the listing from the head.
var ErrInvalidCursor = errors.New("approval: query cursor is invalid")

// MaxCursorLen caps the encoded cursor length DecodeCursor will accept.
// Mirrors actionlog.MaxCursorLen — see that constant for rationale.
const MaxCursorLen = 4096

// EncodeCursor renders the keyset position (createdAt, id) as an
// opaque, URL-safe string. Stores call this when more results remain
// past a returned page. Stable across backend implementations so a
// caller can migrate stores without rewriting clients.
func EncodeCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor produced by [EncodeCursor]. Returns
// [ErrInvalidCursor] on malformed input; an empty cursor decodes to
// (zero time, ""), which stores treat as "start from the head".
func DecodeCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	if len(cursor) > MaxCursorLen {
		return time.Time{}, "", ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	sep := strings.IndexByte(string(raw), '|')
	if sep <= 0 || sep == len(raw)-1 {
		return time.Time{}, "", ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, string(raw[:sep]))
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return ts.UTC(), string(raw[sep+1:]), nil
}
