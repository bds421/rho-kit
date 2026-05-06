//go:build integration

package redistest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	goredis "github.com/redis/go-redis/v9"
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
//
// IMPORTANT: every test using this URL shares the same key namespace. Tests
// that rely on key uniqueness should call [FlushDB] in a t.Cleanup hook (or
// at the start of the test) to avoid order-dependence and -shuffle=on
// failures from leftover keys.
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

// FlushDB removes every key from the shared test container. Call from a
// t.Cleanup hook (or at the start of a test that needs a clean namespace)
// to isolate tests from each other when the shared container would
// otherwise pollute results.
//
// This is heavyweight — wipes the entire DB — so test files that share state
// intentionally should NOT call it. For per-test scoping with multiple
// concurrent tests, prefix keys with t.Name() instead.
func FlushDB(t *testing.T) {
	t.Helper()
	url := Start(t)
	opt, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatalf("redistest: parse URL: %v", err)
	}
	client := goredis.NewClient(opt)
	defer func() { _ = client.Close() }()
	if err := client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("redistest: FlushDB: %v", err)
	}
}
