package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoryCache_WithMaxSize_ImpliesEntryCost verifies the documented
// "Implies WithEntryCost" contract: WithMaxSize must disable byte-based
// cost accounting so the cache is bounded by entry count, regardless of a
// prior WithByteCost / WithCostFunc. Before the fix, WithMaxSize left
// costFunc set, so combining it with WithByteCost bounded the cache at
// maxSize BYTES instead of maxSize entries, rejecting nearly every Set.
func TestMemoryCache_WithMaxSize_ImpliesEntryCost(t *testing.T) {
	tests := []struct {
		name string
		opts []MemoryCacheOption
	}{
		{
			name: "WithByteCost then WithMaxSize",
			opts: []MemoryCacheOption{WithByteCost(), WithMaxSize(100)},
		},
		{
			name: "WithCostFunc then WithMaxSize",
			opts: []MemoryCacheOption{
				WithCostFunc(func(value []byte) int64 { return int64(len(value)) }),
				WithMaxSize(100),
			},
		},
		{
			name: "WithMaxSize alone",
			opts: []MemoryCacheOption{WithMaxSize(100)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, err := NewMemoryCache(tc.opts...)
			require.NoError(t, err)
			defer func() { _ = mc.Close() }()

			// costFunc must be cleared so Ristretto uses the cost=1 path.
			assert.Nil(t, mc.costFunc, "WithMaxSize must clear costFunc to honor 'Implies WithEntryCost'")
			assert.True(t, mc.entryCost, "WithMaxSize must set entryCost")

			// With entry-count accounting (cost=1 per entry) and a cap of 100
			// entries, a single multi-byte value must be admitted. Under the
			// bug (byte cost + maxCost=100) a value larger than 100 bytes is
			// rejected outright.
			value := make([]byte, 256)
			ctx := context.Background()
			err = mc.Set(ctx, "k", value, time.Minute)
			require.NoError(t, err, "entry-cost cache must admit a 256-byte value under a 100-entry cap")
		})
	}
}

// TestMemoryCache_SetNX_SeesPriorPlainSet verifies the documented in-process
// test-and-set semantics for the Set->SetNX sequence: a plain Set(k) followed
// by SetNX(k) without an intervening Sync must observe the existing value and
// return false (Redis SetNX semantics). Before the fix, Ristretto buffered the
// new-key Set in setBuf, so the SetNX existence check missed it and overwrote
// the value while reporting ok=true.
func TestMemoryCache_SetNX_SeesPriorPlainSet(t *testing.T) {
	mc := MustNewMemoryCache()
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	require.NoError(t, mc.Set(ctx, "k", []byte("original"), time.Minute))

	// No Sync between Set and SetNX: this is the buffered-write race.
	ok, err := mc.SetNX(ctx, "k", []byte("overwritten"), time.Minute)
	require.NoError(t, err)
	assert.False(t, ok, "SetNX must return false when a prior plain Set already wrote the key")

	// And the original value must be preserved.
	got, err := mc.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), got, "SetNX must not overwrite the existing value")
}
