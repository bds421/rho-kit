//go:build integration

package redistest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/redis"
)

var (
	once      sync.Once
	sharedURL string
	initErr   error
)

// Start returns the connection URL of a shared Redis container. The container
// is created on the first call and reused for all subsequent calls within the
// same test process. Testcontainers' Ryuk sidecar automatically terminates
// the container when the process exits.
func Start(t *testing.T) string {
	t.Helper()

	once.Do(func() {
		ctx := context.Background()

		container, err := redis.Run(ctx, "redis:7.4-alpine")
		if err != nil {
			initErr = fmt.Errorf("start redis container: %w", err)
			return
		}

		url, err := container.ConnectionString(ctx)
		if err != nil {
			initErr = fmt.Errorf("get redis connection url: %w", err)
			return
		}

		sharedURL = url
	})

	if initErr != nil {
		t.Fatalf("redis setup: %v", initErr)
	}

	return sharedURL
}
