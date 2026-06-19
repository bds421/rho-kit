package sftpbackend

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"path"
	"sort"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// errIterStopped is a sentinel error used to signal that the consumer
// stopped iteration (yield returned false). It must not be surfaced to callers.
var errIterStopped = errors.New("iteration stopped")

// Compile-time interface compliance check.
var _ storage.Lister = (*Backend)(nil)

// List returns an iterator over objects on the remote server whose keys start
// with prefix. Directories are walked recursively.
//
// Matching keys are collected across the directory walk, sorted lexicographically,
// and yielded in that order so keyset pagination via [storage.ListPage]
// (StartAfter) never skips objects — the remote pkg/sftp ReadDir returns entries
// in an arbitrary order, so the StartAfter cursor and MaxKeys truncation are only
// meaningful against a sorted result. This matches localbackend and membackend.
func (b *Backend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		if err := storage.ValidatePrefix(prefix); err != nil {
			yield(storage.ObjectInfo{}, redact.WrapError("sftpbackend", err))
			return
		}
		if err := storage.ValidateListOptions(opts); err != nil {
			yield(storage.ObjectInfo{}, redact.WrapError("sftpbackend", err))
			return
		}

		_, span := otel.Tracer(tracerName).Start(ctx, "sftp.List")
		defer span.End()
		span.SetAttributes(attribute.Int("storage.prefix_len", len(prefix)))

		client, err := b.getClient(ctx)
		if err != nil {
			opErr := storage.WrapSafe("sftpbackend: list connection failed", err)
			span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
			yield(storage.ObjectInfo{}, opErr)
			return
		}
		if err := b.rejectSymlinkPath(client, b.cfg.RootPath); err != nil {
			if isNotExist(err) {
				return
			}
			opErr := redact.WrapError("sftpbackend: unsafe root", err)
			span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
			yield(storage.ObjectInfo{}, opErr)
			return
		}

		start := now()
		objects, walkErr := b.collectObjects(ctx, client, b.cfg.RootPath, prefix, yield, 0)

		// Don't report the sentinel as a real error.
		if errors.Is(walkErr, errIterStopped) {
			walkErr = nil
		}
		b.metrics.observeOp(b.instance, "list", start, walkErr)

		if walkErr != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(walkErr))
			return
		}

		// Sort so StartAfter pagination is deterministic and never skips keys.
		sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })

		count := 0
		for _, info := range objects {
			if ctx.Err() != nil {
				return
			}
			if opts.StartAfter != "" && info.Key <= opts.StartAfter {
				continue
			}
			count++
			if !yield(info, nil) {
				return
			}
			if opts.MaxKeys > 0 && count >= opts.MaxKeys {
				return
			}
		}
	}
}

// maxWalkDepth caps the recursion depth of the directory walk. A
// confused or hostile SFTP server (or a filesystem with symlink loops
// the kit has not yet rejected) could otherwise return a directory
// tree thousands of levels deep and stack-overflow the listing
// goroutine. 32 levels comfortably accommodates any realistic prefix
// hierarchy while a hard stop short of stack exhaustion.
const maxWalkDepth = 32

// collectObjects recursively walks a remote directory, appending ObjectInfo for
// files matching the prefix to out. It does not apply StartAfter/MaxKeys —
// those are applied by List after the collected keys are sorted, because the
// remote ReadDir order is arbitrary and an inline cursor/limit would skip keys.
// Walk-level errors (symlink objects, readdir failures, depth overflow) are
// surfaced via yield and abort the walk. Returns an error if the walk was
// aborted; [errIterStopped] signals the consumer stopped iteration. depth tracks
// recursion level so an attacker-controlled tree cannot exhaust the goroutine
// stack.
func (b *Backend) collectObjects(
	ctx context.Context,
	client Client,
	dir string,
	prefix string,
	yield func(storage.ObjectInfo, error) bool,
	depth int,
) ([]storage.ObjectInfo, error) {
	var out []storage.ObjectInfo
	err := b.walkDir(ctx, client, dir, prefix, yield, depth, &out)
	return out, err
}

func (b *Backend) walkDir(
	ctx context.Context,
	client Client,
	dir string,
	prefix string,
	yield func(storage.ObjectInfo, error) bool,
	depth int,
	out *[]storage.ObjectInfo,
) error {
	if ctx.Err() != nil {
		return nil
	}
	if depth >= maxWalkDepth {
		err := fmt.Errorf("sftpbackend: directory walk exceeded depth %d at %q", maxWalkDepth, dir)
		yield(storage.ObjectInfo{}, err)
		return err
	}

	entries, err := client.ReadDir(dir)
	if err != nil {
		if isNotExist(err) {
			return nil // prefix directory doesn't exist — empty result
		}
		opErr := sftpRemoteError("readdir", err)
		yield(storage.ObjectInfo{}, opErr)
		return opErr
	}

	// pkg/sftp's client.ReadDir yields entries in server (protocol) order,
	// which is not guaranteed to be sorted. Sort by name so keys are yielded
	// in lexicographic order; storage.ListOptions.StartAfter and
	// storage.ListPage's NextStartAfter cursor are documented as lexicographic
	// and silently skip or duplicate objects across pages otherwise. Sorting by
	// entry name (rather than full key) is sufficient here because all keys in a
	// directory share the same parent prefix, and the depth-first recursion
	// descends into a subdirectory before processing later siblings.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		entryPath := path.Join(dir, entry.Name())
		if entry.Mode()&fs.ModeSymlink != 0 {
			err := fmt.Errorf("sftpbackend: refusing symlink object")
			if !yield(storage.ObjectInfo{}, err) {
				return errIterStopped
			}
			return err
		}

		if entry.IsDir() {
			// Only descend if the directory could contain matching keys.
			dirKey := b.toKey(entryPath) + "/"
			if prefix != "" && !strings.HasPrefix(dirKey, prefix) && !strings.HasPrefix(prefix, dirKey) {
				continue
			}
			if err := b.walkDir(ctx, client, entryPath, prefix, yield, depth+1, out); err != nil {
				return err
			}
			continue
		}

		key := b.toKey(entryPath)

		// Apply prefix filter.
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}

		*out = append(*out, storage.ObjectInfo{
			Key:     key,
			Size:    entry.Size(),
			ModTime: entry.ModTime(),
		})
	}

	return nil
}

// toKey converts a remote absolute path back to a storage key (relative to root).
func (b *Backend) toKey(remotePath string) string {
	rel, _ := relPath(b.cfg.RootPath, remotePath)
	return rel
}

// relPath returns the relative path from base to target using path (POSIX) semantics.
func relPath(base, target string) (string, error) {
	// Clean both paths for consistent comparison.
	base = path.Clean(base)
	target = path.Clean(target)

	// Ensure base ends with "/" for proper prefix matching so that
	// base="/data" does not incorrectly match target="/data2/file".
	basePrefix := base
	if !strings.HasSuffix(basePrefix, "/") {
		basePrefix += "/"
	}

	if target == base {
		return ".", nil
	}

	if !strings.HasPrefix(target, basePrefix) {
		return target, fmt.Errorf("target is not under base")
	}

	return strings.TrimPrefix(target, basePrefix), nil
}
