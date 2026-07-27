package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxObjectVersionLen = 2048

var (
	// ErrExactVersionUnavailable means the backend cannot provide a stable
	// provider generation for the requested object. Callers that require
	// exact deletion must fail closed rather than falling back to key-only
	// deletion.
	ErrExactVersionUnavailable = errors.New("storage: exact object version unavailable")

	// ErrExactVersionStillExists means DeleteVersion returned from the
	// provider but the requested generation remained visible during the
	// required verification probe.
	ErrExactVersionStillExists = errors.New("storage: exact object version still exists")
)

// ObjectVersion is one exact physical object generation. Version is opaque to
// callers: backends map it to S3 VersionId, GCS generation, Azure VersionID, or
// a persisted immutable-key generation ID.
type ObjectVersion struct {
	Key     string
	Version string
}

// Validate rejects identities that cannot be carried safely through provider
// APIs, logs, and durable deletion plans.
func (value ObjectVersion) Validate() error {
	if err := ValidateKey(value.Key); err != nil {
		return err
	}
	if value.Version == "" || len(value.Version) > maxObjectVersionLen ||
		!utf8.ValidString(value.Version) {
		return fmt.Errorf("%w: object version is invalid", ErrValidation)
	}
	for _, r := range value.Version {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%w: object version contains invalid characters", ErrValidation)
		}
	}
	if strings.TrimSpace(value.Version) != value.Version {
		return fmt.Errorf("%w: object version contains surrounding whitespace", ErrValidation)
	}
	return nil
}

// ExactVersionStore is an optional capability for generation-pinned object
// access and deletion. DeleteVersion is idempotent when the requested
// generation is already absent. A successful return guarantees that a
// generation-pinned verification probe no longer finds that generation; it
// must never delete a different or newer generation at the same key.
//
// CurrentVersion resolves the provider's current generation into a durable
// ObjectVersion. Backends that cannot expose a stable generation return
// ErrExactVersionUnavailable instead of inventing one.
type ExactVersionStore interface {
	CurrentVersion(ctx context.Context, key string) (ObjectVersion, ObjectMeta, error)
	StatVersion(ctx context.Context, object ObjectVersion) (ObjectMeta, error)
	GetVersion(ctx context.Context, object ObjectVersion) (io.ReadCloser, ObjectMeta, error)
	DeleteVersion(ctx context.Context, object ObjectVersion) error
}
