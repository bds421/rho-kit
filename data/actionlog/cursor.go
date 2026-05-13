package actionlog

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// ErrInvalidCursor is returned by [Logger.List] / store List
// implementations when the supplied [Query.Cursor] is malformed or its
// timestamp cannot be parsed. Callers should drop the cursor and
// restart the listing from the head.
var ErrInvalidCursor = errors.New("actionlog: query cursor is invalid")

// EncodeCursor renders the keyset position (occurredAt, id) as an
// opaque, URL-safe string. Stores call this when more results remain
// past a returned page. Stable across implementations so callers that
// migrate between memory and Postgres backends keep working.
func EncodeCursor(occurredAt time.Time, id string) string {
	raw := occurredAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor produced by [EncodeCursor]. Returns
// [ErrInvalidCursor] on malformed input; an empty cursor decodes to
// (zero time, ""), which stores treat as "start from the head".
func DecodeCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
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
