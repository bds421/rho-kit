package tenant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/cache"
)

// Concurrent misses of the same key by two tenants must not share one
// compute: each tenant gets and stores its own value.
func TestComputeCacheKeepsTenantsApartInFlight(t *testing.T) {
	mem, err := cache.NewMemoryCache()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	scoped := Wrap(mem)
	cc, err := cache.NewComputeCache[string](scoped, "p:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })

	ctxA, err := coretenant.WithID(context.Background(), coretenant.MustNewID("tenant-a"))
	require.NoError(t, err)
	ctxB, err := coretenant.WithID(context.Background(), coretenant.MustNewID("tenant-b"))
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	gotA := make(chan string, 1)
	go func() {
		v, _ := cc.GetOrCompute(ctxA, "settings", func(context.Context) (string, time.Duration, error) {
			close(started)
			<-release
			return "A", time.Minute, nil
		})
		gotA <- v
	}()
	<-started
	gotB := make(chan string, 1)
	go func() {
		v, _ := cc.GetOrCompute(ctxB, "settings", func(context.Context) (string, time.Duration, error) {
			return "B", time.Minute, nil
		})
		gotB <- v
	}()
	select {
	case v := <-gotB:
		assert.Equal(t, "B", v, "tenant B received another tenant's compute")
	case <-time.After(2 * time.Second):
		t.Fatal("tenant B waited on tenant A's compute")
	}
	finish()
	assert.Equal(t, "A", <-gotA)

	assert.Eventually(t, func() bool {
		_, err := scoped.Get(ctxA, "p:settings")
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "tenant A's computed value was not stored under its tenant")
}
