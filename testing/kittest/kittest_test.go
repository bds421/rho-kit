package kittest_test

import (
	"testing"

	"github.com/bds421/rho-kit/testing/kittest/v2/storage"
)

// TestStorageReExportsCompile is a smoke test that references the
// non-integration storage helpers to prove the kittest/storage re-exports
// compile and resolve to live symbols.
func TestStorageReExportsCompile(t *testing.T) {
	if storage.NewLocalBackend == nil {
		t.Fatalf("storage.NewLocalBackend should be a live function reference")
	}
	if storage.BackendSuite == nil {
		t.Fatalf("storage.BackendSuite should be a live function reference")
	}
	if storage.ListerSuite == nil {
		t.Fatalf("storage.ListerSuite should be a live function reference")
	}
}
