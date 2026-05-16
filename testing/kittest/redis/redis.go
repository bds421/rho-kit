//go:build integration

package redis

import (
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
)

// Start returns the connection URL of a shared Redis testcontainer. The
// container is created on the first call and reused for all subsequent calls
// within the same test process.
//
// This is a zero-cost re-export of [redistest.Start].
var Start = redistest.Start

// FlushDB removes every key from the shared test container. Call from a
// t.Cleanup hook (or at the start of a test that needs a clean namespace).
//
// This is a zero-cost re-export of [redistest.FlushDB].
var FlushDB = redistest.FlushDB
