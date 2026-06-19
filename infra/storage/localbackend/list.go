package localbackend

import (
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

		objects, walkErr := b.collectObjects(ctx, root, walkRoot, prefix, yield)
		if walkErr != nil {
			// A yield(...) inside the walk callback already returned the error
			// to the caller and signalled stop; nothing more to do.
			return
		}

		sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })

		count := 0
		for _, obj := range objects {
			if err := ctxErr(ctx); err != nil {
				yield(storage.ObjectInfo{}, err)
				return
			}
			if opts.StartAfter != "" && obj.Key <= opts.StartAfter {
				continue
			}
			count++
			if !yield(obj, nil) {
				return
			}
			if opts.MaxKeys > 0 && count >= opts.MaxKeys {
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
// files whose keys start with prefix. Walk-level errors (e.g. permission
// denied) and symlink-object refusals are surfaced via yield; if the caller
// stops or an error is yielded, collectObjects returns a non-nil error so List
// exits.
func (b *Backend) collectObjects(
	ctx context.Context,
	root *os.Root,
	walkRoot, prefix string,
	yield func(storage.ObjectInfo, error) bool,
) ([]storage.ObjectInfo, error) {
	var objects []storage.ObjectInfo
	err := fs.WalkDir(root.FS(), walkRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			// Surface cancellation as an error rather than ending the walk
			// cleanly, so a mid-listing cancel is never mistaken for a
			// complete result. The post-walk sort/yield loop also re-checks.
			return ctx.Err()
		}
		if walkErr != nil {
			// If the walk root doesn't exist, return no results.
			if d == nil {
				return fs.SkipAll
			}
			// Surface permission errors and other non-trivial walk errors
			// so callers can distinguish "no results" from "access denied".
			if !yield(storage.ObjectInfo{}, localFileError("walk object", walkErr)) {
				return errStopWalk
			}
			return nil
		}

		// Skip directories — only yield files.
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if !yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: refusing symlink object")) {
				return errStopWalk
			}
			return nil
		}

		// fs.WalkDir over the os.Root FS yields root-relative slash paths,
		// which are exactly storage keys.
		key := p

		// Skip internal atomic-write temp files (".tmp-*"): in-flight Put/Copy
		// temporaries and crash-orphaned leftovers are not objects.
		if strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}

		// Apply string-prefix filter (walkRoot may be an ancestor directory).
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return localFileError("inspect object", err)
		}

		objects = append(objects, storage.ObjectInfo{
			Key:     key,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})

	switch {
	case err == nil:
		return objects, nil
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

// errStopWalk is a sentinel returned from the WalkDir callback to stop the
// walk after an error has already been yielded to the caller.
var errStopWalk = errors.New("localbackend: stop walk")
