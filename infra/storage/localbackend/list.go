package localbackend

import (
	"context"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Lister = (*LocalBackend)(nil)

// List returns an iterator over objects whose keys start with prefix.
// Objects are discovered via filepath.WalkDir under the root directory.
func (b *LocalBackend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		if err := storage.ValidatePrefix(prefix); err != nil {
			yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: %w", err))
			return
		}
		if err := storage.ValidateListOptions(opts); err != nil {
			yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: %w", err))
			return
		}

		walkRoot := b.root
		if err := b.rejectSymlinkPath(walkRoot); err != nil {
			yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: unsafe root: %w", err))
			return
		}
		if prefix != "" {
			var err error
			walkRoot, err = b.keyPath(strings.TrimSuffix(prefix, "/"))
			if err != nil {
				yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: %w", err))
				return
			}
			if err := b.rejectSymlinkPath(walkRoot); err != nil {
				if os.IsNotExist(err) {
					return
				}
				yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: unsafe prefix: %w", err))
				return
			}
		}

		count := 0
		stopped := false
		err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return fs.SkipAll
			}
			if walkErr != nil {
				// If the walk root doesn't exist, return no results.
				if d == nil {
					return fs.SkipAll
				}
				// Surface permission errors and other non-trivial walk errors
				// so callers can distinguish "no results" from "access denied".
				if !yield(storage.ObjectInfo{}, localFileError("walk object", walkErr)) {
					stopped = true
					return fs.SkipAll
				}
				return nil
			}

			// Skip directories — only yield files.
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if !yield(storage.ObjectInfo{}, fmt.Errorf("localbackend: refusing symlink object")) {
					stopped = true
					return fs.SkipAll
				}
				return nil
			}

			// Convert absolute path back to storage key.
			rel, err := filepath.Rel(b.root, path)
			if err != nil {
				return localPathError("resolve object path")
			}
			key := filepath.ToSlash(rel)

			// Apply prefix filter (needed when walkRoot is the root dir).
			if prefix != "" && !strings.HasPrefix(key, prefix) {
				return nil
			}

			// Apply StartAfter cursor.
			if opts.StartAfter != "" && key <= opts.StartAfter {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return localFileError("inspect object", err)
			}

			obj := storage.ObjectInfo{
				Key:     key,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			}

			count++
			if !yield(obj, nil) {
				stopped = true
				return fs.SkipAll
			}

			if opts.MaxKeys > 0 && count >= opts.MaxKeys {
				stopped = true
				return fs.SkipAll
			}

			return nil
		})

		if err != nil && !stopped {
			yield(storage.ObjectInfo{}, err)
		}
	}
}
