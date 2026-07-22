//go:build integration

package redis_test

import (
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	redisoauth "github.com/bds421/rho-kit/auth/oauth2/redis/v2"
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
)

// TestBrowserLoginReplicaContinuity_RealRedis runs the browser OIDC durability
// proof against Redis 7 rather than the unit-test simulator. It is the Redis
// leg of the production reference resilience harness.
func TestBrowserLoginReplicaContinuity_RealRedis(t *testing.T) {
	redistest.FlushDB(t)
	options, err := goredis.ParseURL(redistest.Start(t))
	require.NoError(t, err)
	client := goredis.NewClient(options)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	store := redisoauth.New(client, redisoauth.WithPrefix("integration:oidc:"))
	runBrowserReplicaContinuity(t, store)
}
