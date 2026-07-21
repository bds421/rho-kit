package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoryStore_RecheckExpiredEntryKeepsConcurrentReplacement pins the
// check-then-act repair: the map contains the fresh replacement observed after
// Get's stale snapshot, so cleanup must return it rather than report a miss.
func TestMemoryStore_RecheckExpiredEntryKeepsConcurrentReplacement(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore(WithMemoryStoreClock(func() time.Time { return now }))
	store.items["race-key"] = memEntry{
		resp:      CachedResponse{StatusCode: 202, Body: []byte("live")},
		expiresAt: now.Add(time.Minute),
	}

	entry, ok := store.recheckExpiredEntry("race-key")
	require.True(t, ok)
	assert.Equal(t, 202, entry.resp.StatusCode)

	resp, mismatch, err := store.Get(context.Background(), "race-key", nil)
	require.NoError(t, err)
	assert.False(t, mismatch)
	require.NotNil(t, resp)
	assert.Equal(t, []byte("live"), resp.Body)
}

func TestMemoryStore_RecheckExpiredEntryDeletesStaleEntry(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore(WithMemoryStoreClock(func() time.Time { return now }))
	store.items["expired"] = memEntry{expiresAt: now.Add(-time.Second)}

	_, ok := store.recheckExpiredEntry("expired")
	assert.False(t, ok)
	_, stillStored := store.items["expired"]
	assert.False(t, stillStored)
}
