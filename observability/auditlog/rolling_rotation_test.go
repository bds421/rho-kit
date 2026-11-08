package auditlog

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// following is a replica still sealing with firstKey that already knows
// secondKey, as the first step of a rolling rotation deploys it.
func following(store Store) *Logger {
	return New(store, WithChainKeys(firstKey), WithNextChainKeys(secondKey), WithCursorKey(testCursorKey))
}

func sealers(t *testing.T, events []Event) []string {
	t.Helper()
	ids := make([]string, 0, len(events))
	for _, e := range events {
		i, err := sealerOf(mustRing(t), 0, e)
		require.NoError(t, err)
		require.GreaterOrEqual(t, i, 0)
		ids = append(ids, bothKeys[i].ID)
	}
	return ids
}

func mustRing(t *testing.T) []namedKey {
	t.Helper()
	ring, _ := keyed(NewMemoryStore(), secondKey, firstKey).keyRing()
	return ring
}

func TestRollingRotation_OldReplicaFollowsTheNewKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	old, rotated := following(store), keyed(store, secondKey, firstKey)

	logOne(t, old, "old-1")
	logOne(t, old, "old-2")
	logOne(t, rotated, "new-1")
	logOne(t, old, "old-3")
	logOne(t, rotated, "new-2")
	logOne(t, old, "old-4")

	events := store.Events()
	require.Len(t, events, 8, "a marker opens each key's events")
	assert.Equal(t, "chain-keys/2", events[3].Resource)
	assert.Equal(t, []string{"1", "1", "1", "2", "2", "2", "2", "2"}, sealers(t, events),
		"after the marker the old replica seals with the key the head carries")

	for name, verifier := range map[string]*Logger{"old replica": old, "new replica": rotated} {
		head, err := verifier.Verify(ctx, Verification{})
		require.NoError(t, err, name)
		assert.Equal(t, Head{HMAC: events[7].HMAC, Events: 8, KeyID: "2", At: events[7].Timestamp}, head, name)
	}
	_, err := VerifyChainKeys(events, bothKeys, Verification{})
	require.NoError(t, err)
}

func TestRollingRotation_ReplicaWithoutTheNewKeyRefuses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	old, rotated := keyed(store, firstKey), keyed(store, secondKey, firstKey)

	logOne(t, old, "old-1")
	logOne(t, rotated, "new-1")
	before := len(store.Events())

	err := old.LogE(ctx, Event{Actor: "a", Action: "old-2", Resource: "r", Status: "success"})
	require.ErrorIs(t, err, ErrChainHeadUnknown)
	assert.NotErrorIs(t, err, ErrChainBroken)
	assert.Len(t, store.Events(), before, "nothing is chained onto a head of an unknown key")
	assert.Equal(t, uint64(1), old.DroppedCount())
	assert.ErrorIs(t, old.RecordRotation(ctx), ErrChainHeadUnknown)

	logOne(t, rotated, "new-2")
	_, err = rotated.Verify(ctx, Verification{})
	require.NoError(t, err, "the refused writer left the chain intact")
}

func TestRollingRotation_UnkeyedReplicaFollowsTheFirstKeyedRotation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	old := New(store, WithChainKey(firstKey.Key), WithNextChainKeys(secondKey), WithCursorKey(testCursorKey))
	rotated := keyed(store, secondKey, firstKey)

	logOne(t, old, "old-1")
	logOne(t, rotated, "new-1")
	logOne(t, old, "old-2")

	_, err := rotated.Verify(ctx, Verification{})
	require.NoError(t, err)
	_, err = old.Verify(ctx, Verification{})
	require.NoError(t, err)
}

func TestRollingRotation_FollowerRecordsNoMarker(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	logOne(t, keyed(store, secondKey, firstKey), "new-1")
	before := len(store.Events())

	require.NoError(t, following(store).RecordRotation(ctx))
	assert.Len(t, store.Events(), before, "a head of a newer known key needs no marker")
}

func TestRollingRotation_ConcurrentReplicasKeepOneChain(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	old, rotated := following(store), keyed(store, secondKey, firstKey)
	logOne(t, old, "before")

	var wg sync.WaitGroup
	for i := range 16 {
		l := old
		if i%2 == 0 {
			l = rotated
		}
		wg.Go(func() {
			for range 8 {
				assert.NoError(t, l.LogE(ctx, Event{Actor: "a", Action: "x", Resource: "r", Status: "success"}))
			}
		})
	}
	wg.Wait()

	head, err := rotated.Verify(ctx, Verification{})
	require.NoError(t, err)
	assert.Equal(t, "2", head.KeyID)
	var markers int
	for _, e := range store.Events() {
		if e.Resource == "chain-keys/2" {
			markers++
		}
	}
	assert.Equal(t, 1, markers)
}

func TestRollingRotation_EmptyChainHeadNamesTheSealingKey(t *testing.T) {
	head, err := following(NewMemoryStore()).Verify(context.Background(), Verification{})
	require.NoError(t, err)
	assert.Equal(t, "1", head.KeyID, "the next event is sealed by the sealing key, not the next one")
}

func TestWithNextChainKeys_PanicsOnBadKeys(t *testing.T) {
	for name, opts := range map[string][]Option{
		"ID listed twice": {WithChainKeys(firstKey), WithNextChainKeys(ChainKey{ID: "1", Key: secondKey.Key})},
		"empty ID":        {WithChainKey(firstKey.Key), WithNextChainKeys(ChainKey{Key: secondKey.Key})},
		"short key":       {WithChainKeys(firstKey), WithNextChainKeys(ChainKey{ID: "2", Key: []byte("short")})},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() { New(NewMemoryStore(), append(opts, WithCursorKey(testCursorKey))...) })
		})
	}
}

func TestClose_ZeroesNextKeys(t *testing.T) {
	l := following(NewMemoryStore())
	require.NoError(t, l.Close())
	assert.True(t, l.nextChainKeys[0].key.IsEmpty())
}
