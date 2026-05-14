package pagination

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/bds421/rho-kit/core/v2/secret"
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

const (
	// MaxCursorLen caps the byte length of an incoming cursor query
	// parameter. Real cursors are short (a row ID, a base64-encoded
	// timestamp pair); a multi-megabyte cursor is invariably abuse.
	// Setting a cap protects downstream validators from spending CPU
	// scanning a giant string before they realise it's malformed.
	MaxCursorLen = 4096

	minCursorSignerSecretLen = 32
)

var (
	// ErrInvalidRequest is returned by pagination parsers when the request
	// or request URL is nil.
	ErrInvalidRequest = errors.New("pagination: invalid request")
	// ErrCursorTooLong is returned by [ParseCursorParams] when the cursor
	// query parameter exceeds [MaxCursorLen].
	ErrCursorTooLong = errors.New("pagination: cursor exceeds maximum length")
	// ErrAmbiguousQueryParam is returned by pagination parsers when a
	// pagination query parameter appears more than once.
	ErrAmbiguousQueryParam = errors.New("pagination: ambiguous query parameter")
	// ErrInvalidLimitConfig is returned by pagination parsers when the caller
	// passes non-positive default or maximum limits.
	ErrInvalidLimitConfig = errors.New("pagination: invalid limit configuration")
)

// ParseCursorParams extracts cursor and limit from query parameters.
// It clamps limit to maxLimit, defaults empty/invalid/non-positive client
// limits to defaultLimit, and rejects cursor values longer than [MaxCursorLen].
func ParseCursorParams(r *http.Request, defaultLimit, maxLimit int) (CursorParams, error) {
	if defaultLimit <= 0 {
		return CursorParams{}, fmt.Errorf("%w: defaultLimit must be positive", ErrInvalidLimitConfig)
	}
	if maxLimit <= 0 {
		return CursorParams{}, fmt.Errorf("%w: maxLimit must be positive", ErrInvalidLimitConfig)
	}

	q, err := requestQuery(r)
	if err != nil {
		return CursorParams{}, err
	}

	rawLimit, err := singleQueryValue(q, "limit")
	if err != nil {
		return CursorParams{}, err
	}
	limit, _ := strconv.Atoi(rawLimit)
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	cursor, err := singleQueryValue(q, "cursor")
	if err != nil {
		return CursorParams{}, err
	}
	if len(cursor) > MaxCursorLen {
		return CursorParams{}, ErrCursorTooLong
	}

	return CursorParams{
		Cursor: cursor,
		Limit:  limit,
	}, nil
}

func requestQuery(r *http.Request) (url.Values, error) {
	if r == nil || r.URL == nil {
		return nil, ErrInvalidRequest
	}
	return r.URL.Query(), nil
}

func singleQueryValue(q url.Values, key string) (string, error) {
	values := q[key]
	if len(values) == 0 {
		return "", nil
	}
	if len(values) > 1 {
		return "", ErrAmbiguousQueryParam
	}
	return values[0], nil
}

// BuildResult constructs a CursorResult from a slice fetched with limit+1.
// extractID returns the cursor value from an item (typically the ID field).
//
// Negative limits are clamped to 0 — wave 68 closed a hostile-review
// finding that a negative limit forced an out-of-range slice of items
// and panicked at runtime. A clamped-to-zero result reports HasMore
// when the caller has at least one item, surfacing the wiring bug
// without crashing the request.
func BuildResult[T any](items []T, limit int, extractID func(T) string) CursorResult[T] {
	if limit < 0 {
		limit = 0
	}
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
		return fmt.Errorf("invalid cursor format: must be a valid UUID")
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
//
// CursorSigner wraps its HMAC secret in [secret.String] so the raw bytes
// can be zeroed at shutdown via [CursorSigner.Close]. The cryptographic
// hot path reveals into a stack-local copy via [secret.String.Use] so
// no long-lived []byte aliases the key.
type CursorSigner struct {
	secret    *secret.String
	secretLen int
	closed    atomic.Bool
}

// NewCursorSigner creates a CursorSigner. The secret must be at least 32
// bytes; shorter inputs are rejected.
func NewCursorSigner(s []byte) (*CursorSigner, error) {
	if len(s) < minCursorSignerSecretLen {
		return nil, fmt.Errorf("pagination: cursor signer secret must be at least 32 bytes")
	}
	return &CursorSigner{secret: secret.New(s), secretLen: len(s)}, nil
}

// MustNewCursorSigner panics on construction error. Use in startup paths.
func MustNewCursorSigner(secret []byte) *CursorSigner {
	s, err := NewCursorSigner(secret)
	if err != nil {
		panic("pagination: cursor signer secret is invalid")
	}
	return s
}

func (s *CursorSigner) ready() bool {
	return s != nil && !s.closed.Load() && s.secretLen >= minCursorSignerSecretLen && s.secret != nil && !s.secret.IsEmpty()
}

// Close zeroes the wrapped HMAC secret. Subsequent Encode calls return
// "" and Decode returns [ErrCursorInvalid]. Idempotent.
func (s *CursorSigner) Close() error {
	if s == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.secret != nil {
		s.secret.Zero()
	}
	return nil
}

// Encode signs the raw cursor payload (typically a UUID or other PK string).
// Returns "" when payload is empty (first page), or when the signer was not
// constructed with [NewCursorSigner].
func (s *CursorSigner) Encode(payload string) string {
	if payload == "" || !s.ready() {
		return ""
	}
	var sum []byte
	s.secret.Use(func(k []byte) {
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte(payload))
		sum = mac.Sum(nil)
	})
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sum)
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
	if !s.ready() {
		return "", ErrCursorInvalid
	}
	// Cap the input before any base64 decode work. Wave 68 closed a
	// hostile-review finding that direct callers (not going through
	// ParseCursorParams) could submit unbounded cursors and force
	// large base64 allocations before the HMAC compare.
	if len(cursor) > MaxCursorLen {
		return "", ErrCursorInvalid
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
	var match bool
	s.secret.Use(func(k []byte) {
		expected := hmac.New(sha256.New, k)
		expected.Write(payload)
		match = subtle.ConstantTimeCompare(sig, expected.Sum(nil)) == 1
	})
	if !match {
		return "", ErrCursorInvalid
	}
	return string(payload), nil
}
