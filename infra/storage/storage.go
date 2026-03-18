package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrObjectNotFound is returned when a requested key does not exist in the backend.
var ErrObjectNotFound = errors.New("storage: object not found")

// ErrValidation is returned when upload validation fails (e.g. disallowed MIME type,
// file too large). Wrap with fmt.Errorf to add context; unwrap with errors.Is.
var ErrValidation = errors.New("storage: validation failed")

// MaxKeyLen is the maximum allowed length for storage keys.
const MaxKeyLen = 1024

// ValidateKey checks that a storage key is safe for use across all backends.
// This prevents:
//   - Empty keys: always a programming error
//   - Null bytes: can truncate C strings in some backends
//   - Newlines/carriage returns: can break protocol framing
//   - Path traversal: ".." components could escape the storage root
//   - Leading slashes: absolute paths are never valid keys
//   - Excessively long keys: waste memory and may exceed backend limits
//
// All Storage implementations should call this in their public methods to
// ensure consistent validation behavior between backends.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("storage key must not be empty")
	}
	if len(key) > MaxKeyLen {
		return fmt.Errorf("storage key exceeds maximum length of %d bytes", MaxKeyLen)
	}
	if strings.ContainsAny(key, "\x00\n\r") {
		return fmt.Errorf("storage key contains invalid characters (null byte, newline, or carriage return)")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("storage key must not start with a slash")
	}
	if strings.ContainsRune(key, '\\') {
		return fmt.Errorf("storage key must not contain backslashes")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("storage key must not contain path traversal components (%q)", seg)
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
		return fmt.Errorf("storage prefix exceeds maximum length of %d bytes", MaxKeyLen)
	}
	if strings.ContainsAny(prefix, "\x00\n\r") {
		return fmt.Errorf("storage prefix contains invalid characters")
	}
	if strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("storage prefix must not start with a slash")
	}
	if strings.ContainsRune(prefix, '\\') {
		return fmt.Errorf("storage prefix must not contain backslashes")
	}
	for _, seg := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("storage prefix must not contain path traversal components (%q)", seg)
		}
	}
	return nil
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
	// Keys must be valid HTTP header name suffixes (alphanumeric + hyphen).
	// S3 stores these as x-amz-meta-<key> headers.
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
//	if ps, ok := backend.(storage.PresignedStore); ok {
//	    url, err := ps.PresignGetURL(ctx, key, 15*time.Minute)
//	}
type PresignedStore interface {
	// PresignGetURL generates a time-limited URL for unauthenticated GET access.
	PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)

	// PresignPutURL generates a time-limited URL for unauthenticated PUT upload.
	// The caller is responsible for sending the correct Content-Type header.
	PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error)
}
