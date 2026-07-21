package localbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.Storage = (*Backend)(nil)

// Backend implements [storage.Storage] using the local filesystem.
// Keys are converted to relative paths within the root directory.
// Directory components are created automatically on Put.
//
// All filesystem access goes through an [os.Root] opened on the backend's
// root directory. os.Root confines every operation beneath the root and
// refuses to traverse a symlink that escapes it, re-validating each path
// component at the time of the syscall. This closes the symlink TOCTOU race
// that a check-then-act guard (Lstat the path, then MkdirAll/Open/Rename a
// separate path) cannot: an attacker swapping a component for an escaping
// symlink between the check and the syscall is rejected by the kernel-anchored
// os.Root rather than slipping through a stale check.
//
// Safe for concurrent use — the OS-level filesystem syscalls used by
// each method are goroutine-safe and the Backend itself holds no
// mutable in-process state. The os.Root is opened per operation (the root
// directory may be swapped out from under a long-lived handle, and a fresh
// open keeps the symlinked-root rejection effective) and closed before the
// method returns.
type Backend struct {
	root       string
	validators []storage.Validator
}

// Option configures a Backend.
type Option func(*Backend)

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	copied := storage.CloneValidators(validators...)
	return func(b *Backend) {
		b.validators = storage.AppendValidators(b.validators, copied...)
	}
}

// New creates a Backend rooted at dir. The directory is created if it
// does not exist. Panics if dir is empty — this catches misconfigured tests.
func New(dir string, opts ...Option) (*Backend, error) {
	if dir == "" {
		panic("localbackend: New root directory must not be empty")
	}
	absRoot, err := filepath.Abs(dir)
	if err != nil {
		return nil, localPathError("resolve root dir")
	}
	if err := os.MkdirAll(absRoot, 0o750); err != nil {
		return nil, localFileError("create root dir", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, localFileError("resolve root symlinks", err)
	}
	b := &Backend{root: realRoot}
	for _, o := range opts {
		if o == nil {
			panic("localbackend: New option must not be nil")
		}
		o(b)
	}
	return b, nil
}

// Close releases any resources. The local filesystem backend has no
// long-lived handles, so this is a documented no-op for uniform
// interface implementation.
func (b *Backend) Close() error { return nil }

// Put writes content from r to <root>/<key>. Uses atomic write via temp file
// and rename to prevent partial writes on crash.
//
// All filesystem work is performed through an [os.Root] confined to the
// backend's root, so directory creation, the temp-file write, and the final
// rename cannot traverse a symlink escaping the root even if a path component
// is swapped concurrently.
//
// meta is validated (and may be mutated by validators) but is NOT persisted:
// this backend stores only the object bytes. ContentType and Custom are
// dropped — see the package doc for the metadata limitation.
//
// Honours context cancellation symmetrically with remote backends: ctx.Err
// is checked at method entry and again before the body copy, so a cancelled
// caller does not pay for the validator/MkdirAll/temp-file work.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(ctx, r, &meta, b.validators)
	if err != nil {
		return err
	}
	if len(b.validators) > 0 {
		defer func() { _ = storage.CloseValidatedReader(validated) }()
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return err
	}

	rel, err := b.keyRel(key)
	if err != nil {
		return err
	}

	root, err := b.openRoot()
	if err != nil {
		return redact.WrapError("localbackend: unsafe root", err)
	}
	defer func() { _ = root.Close() }()

	dirRoot, err := b.openDestDir(root, path.Dir(rel))
	if err != nil {
		return err
	}
	defer func() { _ = dirRoot.Close() }()

	// Re-check cancellation after the mkdir prep work: a long-running
	// validator chain may have run and we should not burn an fsync if the
	// caller is already gone.
	if err := ctxErr(ctx); err != nil {
		return err
	}

	// Atomic write: write to a temp file within the (now confirmed-in-root)
	// destination directory, then rename. dirRoot is an os.Root re-anchored on
	// the destination directory, so the temp create and rename cannot traverse
	// a symlink swapped into the path.
	base := path.Base(rel)
	tmpName, tmp, err := createTempIn(dirRoot)
	if err != nil {
		return mapUnsafeOrFileError("create temp file", err)
	}

	if _, err := io.Copy(tmp, validated); err != nil {
		_ = tmp.Close()
		_ = dirRoot.Remove(tmpName)
		if errors.Is(err, syscall.ENOSPC) {
			return wrapInsufficientCapacity("write object", err)
		}
		return localFileError("write object", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = dirRoot.Remove(tmpName)
		if errors.Is(err, syscall.ENOSPC) {
			return wrapInsufficientCapacity("sync object", err)
		}
		return localFileError("sync object", err)
	}
	if err := tmp.Close(); err != nil {
		_ = dirRoot.Remove(tmpName)
		return localFileError("close object", err)
	}
	if err := dirRoot.Rename(tmpName, base); err != nil {
		_ = dirRoot.Remove(tmpName)
		return localFileError("rename object", err)
	}
	// rename(2) on Linux is durable across crashes only if the containing
	// directory is also fsynced. Without this step a crash after rename but
	// before the directory entry is flushed can leave the file with stale or
	// zero contents — silent data loss for an operation that just returned ok.
	if err := fsyncDir(dirRoot); err != nil {
		return localFileError("fsync object dir", err)
	}

	return nil
}

// openDestDir creates the directory tree dir within root and returns an os.Root
// re-anchored on it. Both the MkdirAll and the re-anchoring refuse a path that
// traverses a symlink escaping the root (including an existing component that is
// itself an escaping symlink), surfacing the redacted "unsafe parent" error the
// symlink-rejection contract requires. Performing the subsequent temp create
// and rename relative to this confirmed-in-root directory closes the TOCTOU
// window between checking the parent and writing into it.
func (b *Backend) openDestDir(root *os.Root, dir string) (*os.Root, error) {
	switch err := root.MkdirAll(dir, 0o750); {
	case err == nil:
		// Directory tree created (or already a real directory).
	case isEscapeError(err):
		// MkdirAll refused to traverse a symlink escaping the root.
		return nil, redact.WrapError("localbackend: unsafe parent", err)
	case errors.Is(err, os.ErrExist):
		// A component exists but is not a real directory — typically an
		// existing escaping symlink. The OpenRoot below re-checks and maps it
		// to "unsafe parent", so fall through rather than aborting here.
	default:
		// A genuine filesystem failure (permissions, ENOSPC, ...).
		return nil, mapUnsafeOrFileError("create dirs", err)
	}

	dirRoot, err := root.OpenRoot(dir)
	if err != nil {
		// An escaping or symlinked destination directory is refused here.
		return nil, redact.WrapError("localbackend: unsafe parent", err)
	}
	return dirRoot, nil
}

// createTempIn creates a uniquely named temp file directly within dirRoot using
// O_EXCL so a name collision (or a swapped component) cannot truncate an
// existing file. It returns the file's name (relative to dirRoot) and handle.
func createTempIn(dirRoot *os.Root) (string, *os.File, error) {
	for attempt := 0; attempt < 1000; attempt++ {
		name := ".tmp-" + nextTempSuffix()
		f, err := dirRoot.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, f, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return "", nil, err
	}
	return "", nil, os.ErrExist
}

// tempCounter feeds a process-monotonic suffix so concurrent Put/Copy calls in
// the same directory do not collide on temp-file names. Combined with the O_EXCL
// retry loop this avoids any reliance on a shared PRNG.
var tempCounter atomic.Uint64

func nextTempSuffix() string {
	n := tempCounter.Add(1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(n, 36)
}

// fsyncDir opens dirRoot's anchor directory read-only and calls Sync on it.
// Best-effort on platforms where directory fsync isn't required (or is a no-op).
func fsyncDir(dirRoot *os.Root) error {
	d, err := dirRoot.Open(".")
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// Get opens <root>/<key> for reading. Caller must close the returned ReadCloser.
// The returned [storage.ObjectMeta] carries only Size; ContentType and Custom
// are not stored by this backend (see the package doc for the metadata
// limitation). Implicit directory keys (e.g. "a" after Put("a/b")) and symlinks
// are not objects and return [storage.ErrObjectNotFound].
//
// The open is performed through an [os.Root]: a key whose path traverses a
// symlink escaping the root is refused (treated as not found) rather than
// reading bytes from outside the root.
//
// Honours context cancellation: ctx.Err is checked before the open syscall so
// memory/local wirings agree with remote backends about what a cancelled
// caller observes.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	rel, err := b.keyRel(key)
	if err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	root, err := b.openRoot()
	if err != nil {
		return nil, storage.ObjectMeta{}, redact.WrapError("localbackend: unsafe root", err)
	}
	defer func() { _ = root.Close() }()

	if err := b.ensureRegular(root, rel); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get: %w", storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, err
	}

	f, err := root.Open(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("localbackend: get: %w", storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, localFileError("get object", err)
	}

	meta := storage.ObjectMeta{}
	if info, statErr := f.Stat(); statErr == nil {
		meta.Size = info.Size()
	}

	return f, meta, nil
}

// Stat returns object metadata without opening the body for reading.
// Honours context cancellation: ctx.Err is checked at entry.
func (b *Backend) Stat(ctx context.Context, key string) (storage.ObjectMeta, error) {
	if err := ctxErr(ctx); err != nil {
		return storage.ObjectMeta{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectMeta{}, err
	}
	rel, err := b.keyRel(key)
	if err != nil {
		return storage.ObjectMeta{}, err
	}
	root, err := b.openRoot()
	if err != nil {
		return storage.ObjectMeta{}, redact.WrapError("localbackend: unsafe root", err)
	}
	defer func() { _ = root.Close() }()
	if err := b.ensureRegular(root, rel); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.ObjectMeta{}, fmt.Errorf("localbackend: stat: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectMeta{}, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.ObjectMeta{}, fmt.Errorf("localbackend: stat: %w", storage.ErrObjectNotFound)
		}
		return storage.ObjectMeta{}, localFileError("stat object", err)
	}
	return storage.ObjectMeta{
		Size:         info.Size(),
		LastModified: info.ModTime(),
	}, nil
}

// Delete removes <root>/<key>. Returns nil if the file does not exist (idempotent).
// The unlink is performed through an [os.Root], so a key whose path traverses a
// symlink escaping the root cannot unlink a file outside the root.
// Honours context cancellation: ctx.Err is checked before the unlink syscall.
func (b *Backend) Delete(ctx context.Context, key string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	rel, err := b.keyRel(key)
	if err != nil {
		return err
	}

	root, err := b.openRoot()
	if err != nil {
		return redact.WrapError("localbackend: unsafe root", err)
	}
	defer func() { _ = root.Close() }()

	err = root.Remove(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		// A path that escapes the root (e.g. through a swapped symlink
		// component) is unreachable, not deletable: treat it like a missing
		// object so Delete stays idempotent without unlinking outside the root.
		if isEscapeError(err) {
			return nil
		}
		return localFileError("delete object", err)
	}
	return nil
}

// Exists reports whether <root>/<key> exists on disk as a regular object.
// Resolution is performed through an [os.Root]: an object reachable only by
// traversing a symlink escaping the root reports false.
// Honours context cancellation: ctx.Err is checked before the stat syscall.
func (b *Backend) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctxErr(ctx); err != nil {
		return false, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	rel, err := b.keyRel(key)
	if err != nil {
		return false, err
	}

	root, err := b.openRoot()
	if err != nil {
		return false, redact.WrapError("localbackend: unsafe root", err)
	}
	defer func() { _ = root.Close() }()

	if err := b.ensureRegular(root, rel); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// keyRel converts a validated storage key into a clean root-relative path for
// use with os.Root methods. ValidateKey has already rejected absolute keys and
// ".."/"." segments, so the result is always contained; os.Root enforces this
// again at syscall time.
func (b *Backend) keyRel(key string) (string, error) {
	rel := filepath.FromSlash(key)
	if rel == "" || rel == "." {
		return "", fmt.Errorf("localbackend: %w", os.ErrInvalid)
	}
	return rel, nil
}

// ensureRegular verifies that rel resolves, through root, to a regular file
// that is not itself a symlink. Symlink objects are refused (preserving the
// historical "refusing symlink object" contract) and non-regular files (e.g.
// implicit directory keys) are surfaced as os.ErrNotExist so Get/Exists/Copy
// agree with the in-memory and S3 backends.
func (b *Backend) ensureRegular(root *os.Root, rel string) error {
	info, err := root.Lstat(rel)
	if err != nil {
		if isEscapeError(err) {
			// The final component is reachable only by escaping the root:
			// no such object as far as this backend is concerned.
			return fmt.Errorf("localbackend: inspect object: %w", os.ErrNotExist)
		}
		return localFileError("inspect object", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("localbackend: refusing symlink object")
	}
	// Implicit directory keys (e.g. "a" after Put("a/b")) are not objects.
	// membackend and S3 surface these as ErrObjectNotFound; treat a
	// non-regular file the same way so Get/Exists/Copy agree across backends
	// and never hand back a directory handle (whose first Read leaks a raw
	// *os.PathError carrying the absolute root path).
	if !info.Mode().IsRegular() {
		return fmt.Errorf("localbackend: inspect object: %w", os.ErrNotExist)
	}
	return nil
}

// openRoot opens an [os.Root] confined to the backend's root directory, while
// refusing a root that is itself a symlink (which os.OpenRoot would otherwise
// transparently follow). It does so by opening the root's parent as an os.Root
// and then re-opening the final component through it: if that component is a
// symlink escaping the parent — including the test that swaps the whole root
// directory for a symlink — the open is refused race-free. When the root has no
// usable parent (a filesystem root), it falls back to a direct open guarded by
// a symlink Lstat check.
func (b *Backend) openRoot() (*os.Root, error) {
	parent := filepath.Dir(b.root)
	base := filepath.Base(b.root)
	if parent == b.root || base == "." || base == string(filepath.Separator) {
		info, err := os.Lstat(b.root)
		if err != nil {
			return nil, localFileError("inspect root", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("root directory is a symlink")
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("root path is not a directory")
		}
		root, err := os.OpenRoot(b.root)
		if err != nil {
			return nil, localFileError("open root", err)
		}
		return root, nil
	}

	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return nil, localFileError("open root parent", err)
	}
	defer func() { _ = parentRoot.Close() }()

	root, err := parentRoot.OpenRoot(base)
	if err != nil {
		// A symlinked or escaping root component is refused here; map it to a
		// redacted error so the root path never leaks.
		return nil, localFileError("open root", err)
	}
	return root, nil
}

// errPathEscapesText is the message os.Root attaches (inside an *os.PathError)
// when a path component is a symlink that would resolve outside the root. The
// stdlib does not export a sentinel for it, so escape detection matches on this
// stable message.
const errPathEscapesText = "path escapes from parent"

// isEscapeError reports whether err is os.Root's "path escapes from parent"
// rejection (or a wrapper thereof). os.Root returns this when a path component
// is a symlink that would resolve outside the root; callers treat it as an
// unreachable object rather than a hard filesystem failure.
func isEscapeError(err error) bool {
	if err == nil {
		return false
	}
	var pe *os.PathError
	if errors.As(err, &pe) && pe.Err != nil && pe.Err.Error() == errPathEscapesText {
		return true
	}
	return false
}

// mapUnsafeOrFileError maps an os.Root path-escape rejection to the redacted
// "unsafe parent" error used by the symlink-rejection tests, and any other
// filesystem error to the redacted localFileError mapping. Used by mutating
// operations (Put/Copy) where an escaping component must surface as an
// "unsafe" parent rather than a generic failure.
func mapUnsafeOrFileError(op string, err error) error {
	if isEscapeError(err) {
		return redact.WrapError("localbackend: unsafe parent", err)
	}
	return localFileError(op, err)
}

func localFileError(op string, err error) error {
	switch {
	case errors.Is(err, storage.ErrValidation):
		return redact.WrapError("localbackend", err)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrPermission)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrNotExist)
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrExist)
	case errors.Is(err, os.ErrClosed):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrClosed)
	case errors.Is(err, os.ErrInvalid):
		return fmt.Errorf("localbackend: %s: %w", op, os.ErrInvalid)
	default:
		// Preserve the cause in the unwrap chain (so errors.Is/As can
		// reach non-sentinel failures such as EIO, EDQUOT, or arbitrary
		// reader errors during io.Copy) while still redacting the
		// message, matching membackend's chain-preserving wrap behaviour.
		return redact.WrapError(fmt.Sprintf("localbackend: %s failed", op), err)
	}
}

func localPathError(op string) error {
	return fmt.Errorf("localbackend: %s failed", op)
}

// ctxErr returns ctx.Err() for non-nil ctx, or nil otherwise.
// Matches the kit-wide convention used by remote backends: nil ctx is
// treated as context.Background() rather than rejected.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// wrapInsufficientCapacity wraps an ENOSPC-bearing error so callers can
// match both the kit-level sentinel ([storage.ErrInsufficientCapacity],
// which carries the 507 code) and the original syscall.ENOSPC via
// errors.Is. Two %w arguments preserve both targets in the error chain.
func wrapInsufficientCapacity(op string, cause error) error {
	return fmt.Errorf("localbackend: %s: %w (cause: %w)", op, storage.ErrInsufficientCapacity, cause)
}
