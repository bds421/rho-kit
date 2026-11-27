package storagetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
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

// The suites run against shared backends too, so a run must leave no key.
func TestSuitesRemoveWhatTheyWrite(t *testing.T) {
	t.Parallel()
	backend := NewLocalBackend(t)
	t.Run("backend", func(t *testing.T) { BackendSuite(t, backend) })
	t.Run("lister", func(t *testing.T) { ListerSuite(t, backend, backend) })

	var left []string
	for info, err := range backend.List(t.Context(), "", storage.ListOptions{}) {
		require.NoError(t, err)
		left = append(left, info.Key)
	}
	assert.Empty(t, left)
}
