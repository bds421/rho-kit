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
//
// The iterator stops at MaxKeys but does not signal "more available". Callers
// implementing keyset pagination should use [ListPage] instead — it fetches
// MaxKeys+1 internally and returns an explicit truncation flag plus the next
// cursor key.
type Lister interface {
	// List returns an iterator over objects whose keys start with prefix.
	// The iterator yields (ObjectInfo, nil) for each object, or
	// (ObjectInfo{}, error) on failure. Iteration stops on first error
	// or when all matching objects have been yielded.
	//
	// Pass an empty prefix to list all objects.
	List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error]
}

// Page is one bounded slice of a [Lister.List] result, returned by [ListPage].
// Callers wire NextStartAfter back into the next call's [ListOptions.StartAfter]
// to continue iteration; Truncated reports whether the backend held more
// matching keys past this page.
type Page struct {
	// Objects holds at most ListOptions.MaxKeys results in the order the
	// backend yielded them (typically lexicographic by key).
	Objects []ObjectInfo

	// NextStartAfter is the cursor to pass as ListOptions.StartAfter on
	// the next call. Empty when Truncated is false.
	NextStartAfter string

	// Truncated reports whether at least one matching object exists beyond
	// this page. False when the backend ran out of results within MaxKeys.
	Truncated bool
}

// ListPage wraps a Lister with explicit-truncation paging. It fetches
// opts.MaxKeys+1 items so the helper can distinguish "page was exactly full
// but no more remain" from "page was full and more remain", a signal the
// raw [Lister.List] iterator does not expose.
//
// When opts.MaxKeys is zero (unlimited), ListPage forwards every yielded
// object and returns Truncated=false; the caller is responsible for memory
// bounds in that case.
//
// Errors yielded by the iterator surface as the returned error, partial
// page contents are discarded — callers should not act on partial results
// after a paging failure.
func ListPage(ctx context.Context, l Lister, prefix string, opts ListOptions) (Page, error) {
	if l == nil {
		return Page{}, fmt.Errorf("storage: ListPage requires a non-nil Lister")
	}
	if err := ValidateListOptions(opts); err != nil {
		return Page{}, err
	}

	// Unlimited mode: forward results as-is and report Truncated=false.
	if opts.MaxKeys <= 0 {
		var page Page
		for info, err := range l.List(ctx, prefix, opts) {
			if err != nil {
				return Page{}, err
			}
			page.Objects = append(page.Objects, info)
		}
		return page, nil
	}

	probe := opts
	probe.MaxKeys = opts.MaxKeys + 1
	page := Page{Objects: make([]ObjectInfo, 0, opts.MaxKeys)}
	for info, err := range l.List(ctx, prefix, probe) {
		if err != nil {
			return Page{}, err
		}
		if len(page.Objects) == opts.MaxKeys {
			// The (MaxKeys+1)-th object proves at least one more exists.
			page.Truncated = true
			page.NextStartAfter = page.Objects[opts.MaxKeys-1].Key
			break
		}
		page.Objects = append(page.Objects, info)
	}
	return page, nil
}

// MaxListPageSize bounds [ListPage] MaxKeys so a single request cannot
// allocate an unbounded objects buffer or probe the backend with a
// MaxKeys+1 value that would overflow on math.MaxInt64. Operators
// that legitimately need larger pages can paginate via StartAfter.
const MaxListPageSize = 1 << 20

// ValidateListOptions checks list pagination controls before they reach a
// backend API. StartAfter is a storage-key cursor and MaxKeys must be
// non-negative; zero means unlimited.
func ValidateListOptions(opts ListOptions) error {
	if opts.MaxKeys < 0 {
		return fmt.Errorf("%w: storage list MaxKeys must be >= 0", ErrValidation)
	}
	if opts.MaxKeys > MaxListPageSize {
		// Wave 68 added an upper cap so a hostile or buggy caller
		// cannot make ListPage allocate ~MaxKeys * ObjectInfo bytes
		// per request, and so MaxKeys+1 (used to detect truncation)
		// never overflows.
		return fmt.Errorf("%w: storage list MaxKeys exceeds %d", ErrValidation, MaxListPageSize)
	}
	if opts.StartAfter != "" {
		if err := ValidateKey(opts.StartAfter); err != nil {
			return fmt.Errorf("storage list StartAfter is invalid: %w", err)
		}
	}
	return nil
}
