package sftpbackend

import (
	"container/heap"
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
//
// When MaxKeys > 0 the walk retains at most MaxKeys candidates that sort after
// StartAfter (bounded max-heap), so a page request does not buffer the entire
// remote tree. When MaxKeys is 0 (unbounded), the full matching set is still
// buffered — prefer an explicit MaxKeys for large roots.
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
		objects, walkErr := b.collectObjects(ctx, client, b.cfg.RootPath, prefix, opts, yield, 0)

		// Consumer stopped iteration mid-walk (e.g. broke on a symlink
		// error yield). Must NOT clear the sentinel and continue to the
		// sorted pass — Go's range-over-func panics if yield is called
		// again after it returned false.
		if errors.Is(walkErr, errIterStopped) {
			b.metrics.observeOp(b.instance, "list", start, nil)
			return
		}
		b.metrics.observeOp(b.instance, "list", start, walkErr)

		if walkErr != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(walkErr))
			return
		}

		// Sort so StartAfter pagination is deterministic and never skips keys.
		// (When MaxKeys > 0 collectObjects already filtered StartAfter.)
		sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })

		for _, info := range objects {
			if err := ctx.Err(); err != nil {
				// Cancellation mid-yield must surface as an error so callers
				// cannot treat a truncated listing as complete (review-19).
				yield(storage.ObjectInfo{}, err)
				return
			}
			if opts.StartAfter != "" && info.Key <= opts.StartAfter {
				continue
			}
			if !yield(info, nil) {
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

// collectObjects recursively walks a remote directory, collecting ObjectInfo for
// files matching the prefix. When opts.MaxKeys > 0 only the MaxKeys smallest
// keys after StartAfter are retained (max-heap), bounding memory for paged
// listings. When MaxKeys is 0 the full matching set is buffered.
// Walk-level errors are surfaced via yield and abort the walk. Returns an
// error if the walk was aborted; [errIterStopped] signals the consumer stopped
// iteration. depth tracks recursion level so an attacker-controlled tree cannot
// exhaust the goroutine stack.
func (b *Backend) collectObjects(
	ctx context.Context,
	client Client,
	dir string,
	prefix string,
	opts storage.ListOptions,
	yield func(storage.ObjectInfo, error) bool,
	depth int,
) ([]storage.ObjectInfo, error) {
	acc := &listAccumulator{
		maxKeys:    opts.MaxKeys,
		startAfter: opts.StartAfter,
	}
	err := b.walkDir(ctx, client, dir, prefix, yield, depth, acc)
	if err != nil {
		return nil, err
	}
	return acc.snapshot(), nil
}

// listAccumulator collects matching objects. With maxKeys > 0 it keeps only
// the maxKeys smallest keys > startAfter via a max-heap.
type listAccumulator struct {
	maxKeys    int
	startAfter string
	// unbounded path
	all []storage.ObjectInfo
	// bounded path: max-heap by Key (largest at root)
	top objectMaxHeap
}

func (a *listAccumulator) add(info storage.ObjectInfo) {
	if a.startAfter != "" && info.Key <= a.startAfter {
		return
	}
	if a.maxKeys <= 0 {
		a.all = append(a.all, info)
		return
	}
	if a.top.Len() < a.maxKeys {
		heap.Push(&a.top, info)
		return
	}
	// Heap is full: only keep this key if it sorts before the current max.
	if info.Key < a.top[0].Key {
		heap.Pop(&a.top)
		heap.Push(&a.top, info)
	}
}

func (a *listAccumulator) snapshot() []storage.ObjectInfo {
	if a.maxKeys <= 0 {
		return a.all
	}
	out := make([]storage.ObjectInfo, len(a.top))
	copy(out, a.top)
	return out
}

// objectMaxHeap is a max-heap of ObjectInfo ordered by Key.
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

func (b *Backend) walkDir(
	ctx context.Context,
	client Client,
	dir string,
	prefix string,
	yield func(storage.ObjectInfo, error) bool,
	depth int,
	acc *listAccumulator,
) error {
	if err := ctx.Err(); err != nil {
		// Surface cancellation so List does not return a partial listing
		// that looks complete (review-19).
		yield(storage.ObjectInfo{}, err)
		return err
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
		name := entry.Name()
		// Never trust server-supplied "." / ".." (or path.Base-reduced
		// equivalents): a hostile SFTP server can return ".." to walk above
		// RootPath (review-19 path-ascent).
		if name == "." || name == ".." || name == "" {
			continue
		}
		// Skip internal atomic-write temp files. Put stages to
		// remotePath+".tmp-"+16hex then renames; local-style ".tmp-*"
		// basenames are also reserved by storage.ValidateKey.
		if isAtomicPutTempName(name) {
			continue
		}
		if strings.ContainsAny(name, `/\`) {
			// Refuse path separators in entry names — containment relies on
			// single-component joins under RootPath.
			continue
		}
		entryPath := path.Join(dir, name)
		if err := b.ensureRemotePathUnderRoot(entryPath); err != nil {
			// Escaped path: skip rather than emit absolute keys via toKey.
			continue
		}

		// Prefix prune before symlink handling so an unrelated symlink
		// elsewhere in the tree cannot abort listings for other prefixes.
		if entry.IsDir() {
			dirKey, ok := b.toKeyOK(entryPath)
			if !ok {
				continue
			}
			dirKey = dirKey + "/"
			if prefix != "" && !strings.HasPrefix(dirKey, prefix) && !strings.HasPrefix(prefix, dirKey) {
				continue
			}
		} else {
			key, ok := b.toKeyOK(entryPath)
			if !ok {
				continue
			}
			if prefix != "" && !strings.HasPrefix(key, prefix) {
				continue
			}
		}

		if entry.Mode()&fs.ModeSymlink != 0 {
			// Skip symlinks rather than aborting the whole List: a single
			// legacy symlink must not deny listing of unrelated keys.
			continue
		}

		if entry.IsDir() {
			if err := b.walkDir(ctx, client, entryPath, prefix, yield, depth+1, acc); err != nil {
				return err
			}
			continue
		}

		key, ok := b.toKeyOK(entryPath)
		if !ok {
			continue
		}
		acc.add(storage.ObjectInfo{
			Key:     key,
			Size:    entry.Size(),
			ModTime: entry.ModTime(),
		})
	}

	return nil
}

// toKey converts a remote absolute path back to a storage key (relative to root).
// Paths that escape RootPath return empty string; prefer [toKeyOK] when the
// escape must be distinguished from a root-relative empty key.
func (b *Backend) toKey(remotePath string) string {
	rel, ok := b.toKeyOK(remotePath)
	if !ok {
		return ""
	}
	return rel
}

// toKeyOK is like toKey but reports whether the path stayed under RootPath.
func (b *Backend) toKeyOK(remotePath string) (string, bool) {
	rel, err := relPath(b.cfg.RootPath, remotePath)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", false
	}
	return rel, true
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

// isAtomicPutTempName reports whether name is an atomic Put staging file.
// Matches:
//   - basenames starting with ".tmp-" (ValidateKey-reserved; localbackend style)
//   - basenames ending with ".tmp-" + 16 lowercase hex chars (sftpbackend Put)
func isAtomicPutTempName(name string) bool {
	if strings.HasPrefix(name, ".tmp-") {
		return true
	}
	const hexSuffixLen = 16 // hex.EncodeToString of 8 random bytes
	const marker = ".tmp-"
	i := strings.LastIndex(name, marker)
	if i < 0 {
		return false
	}
	suffix := name[i+len(marker):]
	if len(suffix) != hexSuffixLen {
		return false
	}
	for _, c := range suffix {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
