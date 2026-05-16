package storage

import (
	"github.com/bds421/rho-kit/infra/storage/storagetest/v2"
)

// NewLocalBackend creates a [localbackend.Backend] in t.TempDir(). The
// directory and all contents are removed when the test ends.
//
// This is a zero-cost re-export of [storagetest.NewLocalBackend].
var NewLocalBackend = storagetest.NewLocalBackend

// BackendSuite runs the standard compliance suite against any
// [storage.Storage] implementation.
//
// This is a zero-cost re-export of [storagetest.BackendSuite].
var BackendSuite = storagetest.BackendSuite

// ListerSuite runs the compliance tests for backends implementing
// [storage.Lister].
//
// This is a zero-cost re-export of [storagetest.ListerSuite].
var ListerSuite = storagetest.ListerSuite
