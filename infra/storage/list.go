package storage

import (
	"context"
	"fmt"
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
// Check capability via [AsLister] so decorators with [Unwrapper] support are
// handled consistently:
//
//	if l, ok := storage.AsLister(backend); ok {
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

// ValidateListOptions checks list pagination controls before they reach a
// backend API. StartAfter is a storage-key cursor and MaxKeys must be
// non-negative; zero means unlimited.
func ValidateListOptions(opts ListOptions) error {
	if opts.MaxKeys < 0 {
		return fmt.Errorf("%w: storage list MaxKeys must be >= 0", ErrValidation)
	}
	if opts.StartAfter != "" {
		if err := ValidateKey(opts.StartAfter); err != nil {
			return fmt.Errorf("storage list StartAfter is invalid: %w", err)
		}
	}
	return nil
}
