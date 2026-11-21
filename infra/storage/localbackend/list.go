package localbackend

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"sort"
	"strings"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Lister = (*Backend)(nil)

// List returns an iterator over objects whose keys start with prefix.
//
// The prefix is treated as a string prefix (matching membackend and S3), not
// as a directory path: e.g. prefix "logs/2026-06-" matches "logs/2026-06-01/a"
// and prefix "foo" matches the sibling object "foobar.txt". Objects are
// discovered via [fs.WalkDir] over an [os.Root] FS rooted at the backend's
// directory, under the deepest complete directory component of the prefix (or
// the root), then filtered by string prefix.
//
// Walking through the os.Root FS confines the listing to the root: a symlinked
// directory component is not traversed and a symlinked object is refused, so a
// listing can never enumerate or read paths outside the configured root.
//
// Matching keys are collected, sorted lexicographically, and yielded in that
// order so keyset pagination via [storage.ListPage] (StartAfter) never skips
// objects. Context cancellation is honoured at entry and before every yield,
// so a ctx cancelled mid-listing surfaces ctx.Err() rather than returning a
// truncated complete-looking result.
func (b *Backend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		if err := ctxErr(ctx); err != nil {
			yield(storage.ObjectInfo{}, err)
			return
		}
		if err := storage.ValidatePrefix(prefix); err != nil {
			yield(storage.ObjectInfo{}, redact.WrapError("localbackend", err))
			return
		}
		if err := storage.ValidateListOptions(opts); err != nil {
			yield(storage.ObjectInfo{}, redact.WrapError("localbackend", err))
			return
		}

		root, err := b.openRoot()
		if err != nil {
			yield(storage.ObjectInfo{}, redact.WrapError("localbackend: unsafe root", err))
			return
		}
		defer func() { _ = root.Close() }()

		// Walk the deepest complete directory component of the prefix so a
		// partial last segment (e.g. "2026-06-") does not cause us to walk a
		// nonexistent path and miss matching keys. The string-prefix filter
		// below still narrows results to keys that start with prefix.
		walkRoot := prefixWalkRoot(prefix)

		objects, walkErr := b.collectObjects(ctx, root, walkRoot, prefix, opts, yield)
		if walkErr != nil {
			// A yield(...) inside the walk callback already returned the error
			// to the caller and signalled stop; nothing more to do.
			return
		}

		sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })

		for _, obj := range objects {
			if err := ctxErr(ctx); err != nil {
				yield(storage.ObjectInfo{}, err)
				return
			}
			if !yield(obj, nil) {
				return
			}
		}
	}
}

// prefixWalkRoot returns the root-relative directory (a slash path, or "." for
// the root) under which [fs.WalkDir] should run for the given prefix: the
// deepest complete directory component of prefix, or the root when prefix has
// no directory component. Partial trailing segments (everything after the last
// "/") are ignored here and handled by the string-prefix filter in
// collectObjects.
func prefixWalkRoot(prefix string) string {
	if prefix == "" {
		return "."
	}
	// Everything up to (but not including) the last "/" is a complete directory
	// path; the trailing segment is a partial key match within it.
	idx := strings.LastIndex(prefix, "/")
	if idx < 0 {
		return "."
	}
	dirKey := prefix[:idx]
	if dirKey == "" {
		return "."
	}
	return dirKey
}

// collectObjects walks walkRoot within root, returning ObjectInfos for regular
// files whose keys start with prefix. When MaxKeys > 0, only the MaxKeys
// lexicographically-smallest keys after StartAfter are retained (bounded
// max-heap), so memory scales with the page size rather than the whole tree.
// Walk-level errors and symlink-object refusals are surfaced via yield.
func (b *Backend) collectObjects(
	ctx context.Context,
	root *os.Root,
	walkRoot, prefix string,
	opts storage.ListOptions,
	yield func(storage.ObjectInfo, error) bool,
) ([]storage.ObjectInfo, error) {
	collected := objectCollector{maxKeys: opts.MaxKeys}
	err := fs.WalkDir(root.FS(), walkRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			// Surface cancellation as an error rather than ending the walk
			// cleanly, so a mid-listing cancel is never mistaken for a
			// complete result. The post-walk sort/yield loop also re-checks.
			return ctx.Err()
		}
		if walkErr != nil {
			return walkFailure(d, walkErr, yield)
		}
		if d.Type()&fs.ModeSymlink != 0 && !d.IsDir() {
			yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: refusing symlink object"))
			return errStopWalk
		}
		obj, ok, err := listedObject(p, d, prefix, opts.StartAfter)
		if err != nil || !ok {
			return err
		}
		collected.add(obj)
		return nil
	})

	switch {
	case err == nil:
		return collected.sorted(), nil
	case errors.Is(err, errStopWalk):
		// Caller stopped iteration after a yielded error.
		return nil, err
	default:
		// Cancellation or inspect error not already surfaced via yield —
		// report it once.
		yield(storage.ObjectInfo{}, err)
		return nil, err
	}
}

// walkFailure handles an error fs.WalkDir hands the callback. A missing walk
// root is an empty listing (prefix has no objects yet). Any other root-level
// Stat failure (permission, EIO, ELOOP) must surface — fs.WalkDir sets d==nil
// for all root Stat errors, not only ErrNotExist — and so must a mid-walk
// error, once, after which the walk stops (Lister contract: iteration stops on
// first error).
func walkFailure(d fs.DirEntry, walkErr error, yield func(storage.ObjectInfo, error) bool) error {
	if d == nil && errors.Is(walkErr, fs.ErrNotExist) {
		return fs.SkipAll
	}
	yield(storage.ObjectInfo{}, localFileError("walk object", walkErr))
	return errStopWalk
}

// listedObject returns the object at walk path p, or false for an entry the
// listing skips: directories, internal atomic-write temp files (".tmp-*":
// in-flight Put/Copy temporaries and crash-orphaned leftovers), keys outside
// prefix (walkRoot may be an ancestor directory) and keys up to startAfter.
// fs.WalkDir over the os.Root FS yields root-relative slash paths, which are
// exactly storage keys.
func listedObject(p string, d fs.DirEntry, prefix, startAfter string) (storage.ObjectInfo, bool, error) {
	if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
		return storage.ObjectInfo{}, false, nil
	}
	if prefix != "" && !strings.HasPrefix(p, prefix) {
		return storage.ObjectInfo{}, false, nil
	}
	info, err := d.Info()
	if err != nil {
		return storage.ObjectInfo{}, false, localFileError("inspect object", err)
	}
	if startAfter != "" && p <= startAfter {
		return storage.ObjectInfo{}, false, nil
	}
	return storage.ObjectInfo{Key: p, Size: info.Size(), ModTime: info.ModTime()}, true, nil
}

// objectCollector keeps every listed object, or with maxKeys > 0 only the
// maxKeys smallest keys via a max-heap (largest of the kept set at the root).
type objectCollector struct {
	maxKeys int
	objects []storage.ObjectInfo
	top     objectMaxHeap
}

func (c *objectCollector) add(obj storage.ObjectInfo) {
	if c.maxKeys <= 0 {
		c.objects = append(c.objects, obj)
		return
	}
	// When full, drop candidates larger than the current max.
	if c.top.Len() < c.maxKeys {
		heap.Push(&c.top, obj)
		return
	}
	if obj.Key < c.top[0].Key {
		heap.Pop(&c.top)
		heap.Push(&c.top, obj)
	}
}

// sorted returns the kept objects; a bounded set comes out in key order, an
// unbounded one in walk order.
func (c *objectCollector) sorted() []storage.ObjectInfo {
	if c.maxKeys <= 0 {
		return c.objects
	}
	objects := make([]storage.ObjectInfo, c.top.Len())
	for i := len(objects) - 1; i >= 0; i-- {
		objects[i] = heap.Pop(&c.top).(storage.ObjectInfo)
	}
	return objects
}

// errStopWalk is a sentinel returned from the WalkDir callback to stop the
// walk after an error has already been yielded to the caller.
var errStopWalk = errors.New("localbackend: stop walk")

// objectMaxHeap is a max-heap of ObjectInfo ordered by Key (largest root).
// Used to retain only the MaxKeys smallest keys while walking.
type objectMaxHeap []storage.ObjectInfo

func (h objectMaxHeap) Len() int           { return len(h) }
func (h objectMaxHeap) Less(i, j int) bool { return h[i].Key > h[j].Key } // max-heap
func (h objectMaxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *objectMaxHeap) Push(x any)        { *h = append(*h, x.(storage.ObjectInfo)) }
func (h *objectMaxHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
