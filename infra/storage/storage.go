// asvs: V12.1.1, V12.3.1
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrObjectNotFound is returned when a requested key does not exist in the backend.
var ErrObjectNotFound = errors.New("storage: object not found")

// ErrValidation is returned when storage input validation fails (e.g. invalid
// keys, disallowed MIME type, file too large). Wrap with fmt.Errorf to add
// context; unwrap with errors.Is.
var ErrValidation = errors.New("storage: validation failed")

// ErrBatchTooLarge is returned when a batch storage helper is asked to process
// too many keys in one call.
var ErrBatchTooLarge = errors.New("storage: batch operation exceeds maximum item count")

// MaxKeyLen is the maximum allowed length for storage keys.
const MaxKeyLen = 1024

// MaxBatchKeys caps shared storage batch helpers. The limit matches the
// portable single-request ceiling of common object stores such as S3
// DeleteObjects.
const MaxBatchKeys = 1000

// ValidateKey checks that a storage key is safe for use across all backends.
// This prevents:
//   - Empty keys: always a programming error
//   - Invalid UTF-8: corrupts logs and provider/debug output
//   - Whitespace/control characters: can break logs, CLIs, and protocol framing
//   - Path traversal: ".." components could escape the storage root
//   - Leading slashes: absolute paths are never valid keys
//   - Excessively long keys: waste memory and may exceed backend limits
//
// All Storage implementations should call this in their public methods to
// ensure consistent validation behavior between backends.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: storage key must not be empty", ErrValidation)
	}
	if len(key) > MaxKeyLen {
		return fmt.Errorf("%w: storage key exceeds maximum length of %d bytes", ErrValidation, MaxKeyLen)
	}
	if containsInvalidKeyRune(key) {
		return fmt.Errorf("%w: storage key contains invalid characters", ErrValidation)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: storage key must not start with a slash", ErrValidation)
	}
	if strings.ContainsRune(key, '\\') {
		return fmt.Errorf("%w: storage key must not contain backslashes", ErrValidation)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" {
			return fmt.Errorf("%w: storage key must not contain empty path segments", ErrValidation)
		}
		if seg == ".." || seg == "." {
			return fmt.Errorf("%w: storage key must not contain path traversal components", ErrValidation)
		}
	}
	return nil
}

// ValidatePrefix checks that a list prefix is safe. It applies the same
// rules as ValidateKey except it allows a trailing slash (common for
// directory-like prefixes) and does not require non-empty.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaxKeyLen {
		return fmt.Errorf("%w: storage prefix exceeds maximum length of %d bytes", ErrValidation, MaxKeyLen)
	}
	if containsInvalidKeyRune(prefix) {
		return fmt.Errorf("%w: storage prefix contains invalid characters", ErrValidation)
	}
	if strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("%w: storage prefix must not start with a slash", ErrValidation)
	}
	if strings.ContainsRune(prefix, '\\') {
		return fmt.Errorf("%w: storage prefix must not contain backslashes", ErrValidation)
	}
	for _, seg := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if seg == "" {
			return fmt.Errorf("%w: storage prefix must not contain empty path segments", ErrValidation)
		}
		if seg == ".." || seg == "." {
			return fmt.Errorf("%w: storage prefix must not contain path traversal components", ErrValidation)
		}
	}
	return nil
}

func containsInvalidKeyRune(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// ObjectMeta carries metadata associated with a stored object.
// It is intentionally a plain value type so callers can construct it inline.
type ObjectMeta struct {
	// ContentType is the MIME type, e.g. "image/jpeg".
	// When empty during Put, backends should attempt detection or
	// default to "application/octet-stream".
	ContentType string

	// Size is the content length in bytes. Zero means unknown.
	// Set this when known (e.g. from multipart form header) so
	// backends can set Content-Length on the upload request.
	Size int64

	// ETag is an opaque identifier for a specific version of the object.
	// S3 returns this from GetObject/HeadObject. Used for conditional-GET
	// (If-None-Match) in [storagehttp.ServeFile].
	ETag string

	// LastModified is the last modification time of the object.
	// Used for conditional-GET (If-Modified-Since) in [storagehttp.ServeFile].
	LastModified time.Time

	// Custom holds arbitrary key-value metadata.
	// Keys must be valid HTTP header name suffixes: non-empty ASCII
	// alphanumeric strings with optional internal hyphens. Values must be
	// printable ASCII. The total metadata size is bounded by ValidateObjectMeta
	// to keep behavior portable across providers that store these as headers
	// (for example S3 x-amz-meta-<key>).
	Custom map[string]string
}

// Storage defines a backend-agnostic object storage interface.
// Implementations must be safe for concurrent use.
//
// Keys are arbitrary non-empty strings (e.g. "uploads/2026/01/avatar.png").
// All methods accept context for cancellation and tracing propagation.
type Storage interface {
	// Put stores the content from r at key, using meta for Content-Type and
	// any backend-specific headers. The reader is consumed exactly once.
	// Returns an error wrapping ErrValidation if a Validator rejects the content.
	Put(ctx context.Context, key string, r io.Reader, meta ObjectMeta) error

	// Get retrieves object content. The caller must close the returned
	// ReadCloser after consumption. Returns ErrObjectNotFound if key is absent.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error)

	// Delete removes an object. Returns nil if the key does not exist
	// (idempotent delete). Returns a wrapped error on infrastructure failure.
	Delete(ctx context.Context, key string) error

	// Exists reports whether the key exists without downloading content.
	Exists(ctx context.Context, key string) (bool, error)
}

// PresignedStore is an optional extension implemented by backends that
// support pre-signed URLs (e.g. S3). Call-site code checks capability:
//
//	if ps, ok := storage.AsPresigned(backend); ok {
//	    url, err := ps.PresignGetURL(ctx, key, 15*time.Minute)
//	}
type PresignedStore interface {
	// PresignGetURL generates a time-limited URL for unauthenticated GET access.
	PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)

	// PresignPutURL generates a time-limited URL for unauthenticated PUT upload.
	// The caller is responsible for sending the correct Content-Type header.
	PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error)
}
