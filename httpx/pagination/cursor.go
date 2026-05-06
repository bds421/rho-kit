package pagination

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

// ErrCursorInvalid is returned by [CursorSigner.Decode] when a cursor is
// malformed, has been tampered with, or was signed by a different secret.
var ErrCursorInvalid = errors.New("pagination: invalid or tampered cursor")

// CursorSigner HMAC-signs cursors so clients cannot forge them or enumerate
// IDs by guessing. The on-the-wire format is
// base64url(payload).base64url(hmac-sha256(secret, payload)).
//
// Without signing, the previous default returned raw last-PK as the
// next_cursor — letting any client pass an arbitrary ID (a foreign user's,
// a guessed one, an SQL fragment) to the next page. Signing closes the
// forgery path; combine with a real authorization check in ListFn for the
// "is this user allowed to see this row" check the cursor cannot replace.
//
// Wire across processes: every replica that issues or accepts cursors
// must share the same secret. Use [config.MustGetSecret] or your KMS of
// choice; never let each pod mint its own random secret.
type CursorSigner struct {
	secret []byte
}

// NewCursorSigner creates a CursorSigner. The secret must be at least 32
// bytes; shorter inputs are rejected.
func NewCursorSigner(secret []byte) (*CursorSigner, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("pagination: cursor signer secret must be at least 32 bytes (got %d)", len(secret))
	}
	cp := make([]byte, len(secret))
	copy(cp, secret)
	return &CursorSigner{secret: cp}, nil
}

// MustNewCursorSigner panics on construction error. Use in startup paths.
func MustNewCursorSigner(secret []byte) *CursorSigner {
	s, err := NewCursorSigner(secret)
	if err != nil {
		panic(err.Error())
	}
	return s
}

// Encode signs the raw cursor payload (typically a UUID or other PK string).
// Returns "" when payload is empty (first page).
func (s *CursorSigner) Encode(payload string) string {
	if payload == "" {
		return ""
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Decode verifies and decodes a signed cursor produced by [CursorSigner.Encode].
// Returns "" with nil error for the empty (first page) cursor.
// Returns [ErrCursorInvalid] for any malformed or tampered input — the kit's
// HandleCursorList maps that to 400 Bad Request.
//
// The HMAC comparison is constant-time so attackers can't probe the verify
// path with timing oracles.
func (s *CursorSigner) Decode(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	idx := strings.IndexByte(cursor, '.')
	if idx < 0 {
		return "", ErrCursorInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor[:idx])
	if err != nil {
		return "", ErrCursorInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(cursor[idx+1:])
	if err != nil {
		return "", ErrCursorInvalid
	}
	expected := hmac.New(sha256.New, s.secret)
	expected.Write(payload)
	if subtle.ConstantTimeCompare(sig, expected.Sum(nil)) != 1 {
		return "", ErrCursorInvalid
	}
	return string(payload), nil
}
