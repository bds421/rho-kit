package storagetest

import (
	"testing"
)

func TestLocalBackendCompliance(t *testing.T) {
	t.Parallel()
	backend := NewLocalBackend(t)
	BackendSuite(t, backend)
}

func TestLocalBackendListerCompliance(t *testing.T) {
	t.Parallel()
	backend := NewLocalBackend(t)
	ListerSuite(t, backend, backend)
}
