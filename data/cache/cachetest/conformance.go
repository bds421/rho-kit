package cachetest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/cache"
)

// Factory constructs a fresh Cache for one subtest. It receives
// *testing.T so it can register cleanup via t.Cleanup.
type Factory func(t *testing.T) cache.Cache

// Run executes the full conformance battery against the supplied
// factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	if factory == nil {
		t.Fatal("cachetest.Run: factory must not be nil")
	}

	t.Run("SetGetRoundTripPreservesBytes", func(t *testing.T) { testSetGetRoundTrip(t, factory) })
	t.Run("GetOnMissingKeyReturnsErrCacheMiss", func(t *testing.T) { testGetMissing(t, factory) })
	t.Run("DeleteIdempotent", func(t *testing.T) { testDeleteIdempotent(t, factory) })
	t.Run("SetOverwrites", func(t *testing.T) { testSetOverwrites(t, factory) })
	t.Run("ExistsReturnsCorrectBool", func(t *testing.T) { testExists(t, factory) })
	t.Run("RejectsEmptyKey", func(t *testing.T) { testRejectsEmptyKey(t, factory) })
	t.Run("ConcurrentReadWriteSafe", func(t *testing.T) { testConcurrentReadWrite(t, factory) })
}

func testSetGetRoundTrip(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()
	const key = "round-trip"
	val := []byte("the bytes")

	require.NoError(t, c.Set(ctx, key, val, time.Minute))

	got, err := pollUntilHit(c, key)
	require.NoError(t, err)
	assert.Equal(t, val, got, "Cache must round-trip bytes exactly")
}

// pollUntilHit hides the kit's mix of synchronous (Redis) and
// eventually-consistent (Ristretto-backed MemoryCache) Set
// visibility behind a bounded poll. The contract Cache exposes
// does not promise read-your-writes after Set returns; backends
// that ARE synchronous succeed on the first poll iteration.
func pollUntilHit(c cache.Cache, key string) ([]byte, error) {
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		got, err := c.Get(context.Background(), key)
		if err == nil {
			return got, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testGetMissing(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()

	got, err := c.Get(ctx, "never-set")
	assert.Nil(t, got, "missing key must return nil value")
	assert.ErrorIs(t, err, cache.ErrCacheMiss, "missing key must return ErrCacheMiss specifically")
}

func testDeleteIdempotent(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()

	// Delete on a missing key must succeed (idempotent).
	require.NoError(t, c.Delete(ctx, "never-set"), "Delete on missing key must NOT error")

	// Set, Delete, Delete: second Delete still succeeds.
	require.NoError(t, c.Set(ctx, "k", []byte("v"), time.Minute))
	require.NoError(t, c.Delete(ctx, "k"))
	require.NoError(t, c.Delete(ctx, "k"), "repeated Delete must NOT error")

	_, err := c.Get(ctx, "k")
	assert.ErrorIs(t, err, cache.ErrCacheMiss, "after Delete the key is a miss")
}

func testSetOverwrites(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()
	const key = "overwrite"

	require.NoError(t, c.Set(ctx, key, []byte("first"), time.Minute))
	// Wait for the first Set to become visible before issuing the
	// second so the eventual-consistency window cannot reorder
	// them.
	if _, err := pollUntilHit(c, key); err != nil {
		t.Fatalf("first Set never became visible: %v", err)
	}
	require.NoError(t, c.Set(ctx, key, []byte("second"), time.Minute))

	got, err := pollUntilValue(c, key, []byte("second"))
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), got, "Set must overwrite the prior value")
}

func testExists(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()

	yes, err := c.Exists(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, yes, "Exists on a missing key must return false")

	require.NoError(t, c.Set(ctx, "present", []byte("v"), time.Minute))

	// Poll Exists for the same eventual-consistency reason as Get.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		yes, err = c.Exists(ctx, "present")
		if err == nil && yes {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Exists never returned true within 500ms (last err=%v)", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.True(t, yes, "Exists on a set key must return true")
}

// pollUntilValue polls Get until the value matches `want` or the
// deadline fires. Returns the last observed value (which may be
// stale on the eventual-consistency tail).
func pollUntilValue(c cache.Cache, key string, want []byte) ([]byte, error) {
	deadline := time.Now().Add(500 * time.Millisecond)
	var last []byte
	for {
		got, err := c.Get(context.Background(), key)
		if err == nil {
			last = got
			if string(got) == string(want) {
				return got, nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return last, err
			}
			return last, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testRejectsEmptyKey(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()

	_, err := c.Get(ctx, "")
	assert.Error(t, err, "Get must reject empty key")

	err = c.Set(ctx, "", []byte("v"), time.Minute)
	assert.Error(t, err, "Set must reject empty key")

	err = c.Delete(ctx, "")
	assert.Error(t, err, "Delete must reject empty key")

	_, err = c.Exists(ctx, "")
	assert.Error(t, err, "Exists must reject empty key")
}

func testConcurrentReadWrite(t *testing.T, factory Factory) {
	c := factory(t)
	ctx := context.Background()
	const key = "concurrent"

	require.NoError(t, c.Set(ctx, key, []byte("initial"), time.Minute))

	var wg sync.WaitGroup
	var errs atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				if _, err := c.Get(ctx, key); err != nil && err != cache.ErrCacheMiss {
					errs.Add(1)
				}
			} else {
				if err := c.Set(ctx, key, []byte("v"), time.Minute); err != nil {
					errs.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	assert.Zero(t, errs.Load(), "concurrent Get/Set must not surface backend errors")
}
