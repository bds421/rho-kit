package localbackend

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// TestWrapInsufficientCapacity verifies the helper wraps an ENOSPC error such
// that both [storage.ErrInsufficientCapacity] (kit sentinel + 507 mapping)
// and the original syscall.ENOSPC remain reachable via errors.Is.
func TestWrapInsufficientCapacity(t *testing.T) {
	enospc := &pathErr{op: "write", path: "/tmp", err: syscall.ENOSPC}
	wrapped := wrapInsufficientCapacity("write object", enospc)

	if !errors.Is(wrapped, storage.ErrInsufficientCapacity) {
		t.Fatalf("expected wrapped to chain to ErrInsufficientCapacity, got %v", wrapped)
	}
	if !errors.Is(wrapped, syscall.ENOSPC) {
		t.Fatalf("expected wrapped to chain to syscall.ENOSPC, got %v", wrapped)
	}
	if !apperror.IsStorageFull(wrapped) {
		t.Fatalf("expected IsStorageFull true, got false")
	}
}

// TestLocalFileError_DefaultBranchPreservesCause verifies that a non-sentinel
// cause (e.g. EIO, EDQUOT, or an arbitrary reader error during io.Copy) routed
// through localFileError's default branch remains reachable via errors.Is while
// the rendered message stays redacted. Matching membackend's chain-preserving
// wrap keeps observability uniform across sibling backends: the old default
// branch dropped the cause (no %w), so errors.Is/As could not reach it.
func TestLocalFileError_DefaultBranchPreservesCause(t *testing.T) {
	// EIO is not one of the named os.Err* sentinels, so it falls through to
	// the default branch.
	cause := &pathErr{op: "read", path: "/secret/internal/path", err: syscall.EIO}
	mapped := localFileError("write object", cause)

	if !errors.Is(mapped, cause) {
		t.Fatalf("expected mapped error to chain to the original cause, got %v", mapped)
	}
	if !errors.Is(mapped, syscall.EIO) {
		t.Fatalf("expected mapped error to chain to syscall.EIO, got %v", mapped)
	}
	// The redacted message must not leak the cause's sensitive text (the
	// internal path) verbatim.
	if msg := mapped.Error(); strings.Contains(msg, "/secret/internal/path") {
		t.Fatalf("redacted message leaked cause text: %q", msg)
	}
}

// pathErr mimics os.PathError carrying a wrapped syscall.Errno so tests can
// drive the ENOSPC translation path without depending on real disk-fill.
type pathErr struct {
	op   string
	path string
	err  error
}

func (p *pathErr) Error() string { return p.op + " " + p.path + ": " + p.err.Error() }
func (p *pathErr) Unwrap() error { return p.err }
