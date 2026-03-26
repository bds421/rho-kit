package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeCache_CustomCodec(t *testing.T) {
	backend := newTestBackend(t)

	cc, err := NewComputeCacheWithCodec[string](backend, "codec:", JSONCodec[string]{})
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "hello", 10 * time.Minute, nil
	}

	val, err := cc.GetOrCompute(context.Background(), "key1", fn)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)

	backend.Sync()

	// Should hit cache.
	val, err = cc.GetOrCompute(context.Background(), "key1", fn)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestComputeCache_NilCodecUsesJSON(t *testing.T) {
	backend := newTestBackend(t)

	cc, err := NewComputeCacheWithCodec[string](backend, "nilcodec:", nil)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "world", 10 * time.Minute, nil
	}

	val, err := cc.GetOrCompute(context.Background(), "key1", fn)
	require.NoError(t, err)
	assert.Equal(t, "world", val)
}
