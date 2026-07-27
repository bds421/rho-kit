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

const (
	maxObjectVersionLen = 2048

	// MaxExactVersionListEntries is the portable upper bound for one exact
	// generation enumeration request. Backends must fail closed instead of
	// silently truncating above this bound.
	MaxExactVersionListEntries = 4096
)

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

// ExactVersionLister is the bounded maintenance-side complement to
// ExactVersionStore. Versions returns every retained content generation for
// one exact key, oldest or newest ordering being backend-defined. It must fail
// closed instead of truncating when more than limit generations exist.
//
// Delete markers and provider tombstones contain no object body and are not
// returned. Callers use this capability to seal an immutable deletion plan;
// they must re-enumerate and require an empty result before activating a final
// legal-erasure receipt.
type ExactVersionLister interface {
	Versions(ctx context.Context, key string, limit int) ([]ObjectVersion, error)
}

// ExactVersionPrefixLister enumerates every retained content generation below
// one exact key prefix. It is the bounded legal-erasure complement to
// [ExactVersionLister]: implementations must count provider tombstones and
// fail closed instead of truncating when more than limit provider entries
// exist, while returning only content-bearing generations.
type ExactVersionPrefixLister interface {
	VersionsByPrefix(
		ctx context.Context,
		prefix string,
		limit int,
	) ([]ObjectVersion, error)
}

// ValidateExactVersionListLimit enforces the provider-neutral bound shared by
// exact-key and exact-prefix generation enumeration.
func ValidateExactVersionListLimit(limit int) error {
	if limit < 1 || limit > MaxExactVersionListEntries {
		return ErrBatchTooLarge
	}
	return nil
}
