package storage

import (
	"context"
	"io"
	"iter"
	"time"
)

// ObjectVersion describes a specific version of a stored object.
type ObjectVersion struct {
	// Key is the storage key.
	Key string

	// VersionID is the backend-specific identifier for this version.
	VersionID string

	// Size is the content length in bytes.
	Size int64

	// ModTime is the timestamp when this version was created.
	ModTime time.Time

	// IsLatest indicates whether this is the current version.
	IsLatest bool

	// IsDeleteMarker indicates that this version represents a deletion
	// (S3-specific concept; always false for backends without delete markers).
	IsDeleteMarker bool
}

// Versioner is an optional extension for backends that support
// object versioning (e.g. S3 with versioning enabled).
// Check capability via type assertion:
//
//	if v, ok := backend.(storage.Versioner); ok {
//	    for ver, err := range v.ListVersions(ctx, "doc.pdf") {
//	        // ...
//	    }
//	}
type Versioner interface {
	// ListVersions returns an iterator over all versions of a key,
	// newest first. Yields (ObjectVersion, nil) per version, or
	// (ObjectVersion{}, error) on failure.
	ListVersions(ctx context.Context, key string) iter.Seq2[ObjectVersion, error]

	// GetVersion retrieves a specific version of an object.
	// Returns ErrObjectNotFound if the key or version does not exist.
	GetVersion(ctx context.Context, key string, versionID string) (io.ReadCloser, ObjectMeta, error)

	// DeleteVersion deletes a specific version of an object.
	// Returns nil if the version does not exist (idempotent).
	DeleteVersion(ctx context.Context, key string, versionID string) error
}
