package localbackend

import (
	"errors"
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

// pathErr mimics os.PathError carrying a wrapped syscall.Errno so tests can
// drive the ENOSPC translation path without depending on real disk-fill.
type pathErr struct {
	op   string
	path string
	err  error
}

func (p *pathErr) Error() string { return p.op + " " + p.path + ": " + p.err.Error() }
func (p *pathErr) Unwrap() error { return p.err }
