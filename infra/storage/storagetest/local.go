package storagetest

import (
	"testing"

	"github.com/bds421/rho-kit/infra/storage/localbackend"
)

// NewLocalBackend creates a LocalBackend in t.TempDir().
// The directory and all contents are removed when the test ends.
func NewLocalBackend(t *testing.T, opts ...localbackend.Option) *localbackend.LocalBackend {
	t.Helper()
	b, err := localbackend.New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("storagetest: create local backend: %v", err)
	}
	return b
}
