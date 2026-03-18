package storage

import (
	"context"
	"iter"
	"time"
)

// ObjectInfo describes an object returned by [Lister.List].
type ObjectInfo struct {
	// Key is the storage key (same format as Put/Get/Delete).
	Key string

	// Size is the content length in bytes.
	Size int64

	// ContentType is the MIME type, if available from the backend.
	// May be empty for backends that don't store MIME types (e.g. SFTP).
	ContentType string

	// ModTime is the last modification time, if available.
	ModTime time.Time
}

// ListOptions configures a List call.
type ListOptions struct {
	// MaxKeys limits the number of results. Zero means unlimited.
	MaxKeys int

	// StartAfter is an exclusive pagination cursor. Only objects with
	// keys lexicographically after this value are returned.
	StartAfter string
}

// Lister is an optional extension for backends that support listing objects.
// Check capability via type assertion:
//
//	if l, ok := backend.(storage.Lister); ok {
//	    for info, err := range l.List(ctx, "uploads/", storage.ListOptions{}) {
//	        // ...
//	    }
//	}
type Lister interface {
	// List returns an iterator over objects whose keys start with prefix.
	// The iterator yields (ObjectInfo, nil) for each object, or
	// (ObjectInfo{}, error) on failure. Iteration stops on first error
	// or when all matching objects have been yielded.
	//
	// Pass an empty prefix to list all objects.
	List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error]
}
