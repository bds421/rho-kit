package auditlog

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	firstKey         = ChainKey{ID: "1", Key: testChainKey}
	secondKey        = ChainKey{ID: "2", Key: bytes.Repeat([]byte("d"), MinChainKeyLen)}
	rotatedCursorKey = bytes.Repeat([]byte("v"), MinCursorKeyLen)
	bothKeys         = []ChainKey{secondKey, firstKey}
)

func logOne(t *testing.T, l *Logger, action string) {
	t.Helper()
	require.NoError(t, l.LogE(context.Background(), Event{Actor: "a", Action: action, Resource: "r", Status: "success"}))
}

func keyed(store Store, newest ChainKey, older ...ChainKey) *Logger {
	return New(store, WithChainKeys(newest, older...), WithCursorKey(testCursorKey))
}

func unkeyed(store Store, key []byte) *Logger {
	return New(store, WithChainKey(key), WithCursorKey(testCursorKey))
}

// forge appends an event sealed by key onto the head without the Logger's
// head check, as an older kit version or whoever holds the key could.
func forge(t *testing.T, store *MemoryStore, key ChainKey, action string) {
	t.Helper()
	ctx := context.Background()
	event, err := unkeyed(store, key.Key).prepareEvent(ctx, Event{Actor: "a", Action: action, Resource: "r", Status: "success"})
	require.NoError(t, err)
	if head, found, err := store.Head(ctx); err == nil && found {
		event.PrevHMAC = head.HMAC
	}
	event.HMAC = computeKeyedHMAC(key.Key, key.ID, event)
	require.NoError(t, store.Append(ctx, event))
}

// truncate keeps the first n events, as whoever can write the store could.
func truncate(store *MemoryStore, n int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.events = store.events[:n]
}

func TestWithChainKeys_VerifiesAcrossRotation(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, unkeyed(store, firstKey.Key), "before")
	logOne(t, keyed(store, secondKey, firstKey), "after")

	events := store.Events()
	require.Len(t, events, 3)
	assert.Equal(t, RotationAction, events[1].Action)
	assert.Equal(t, "chain-keys/2", events[1].Resource)

	head, err := keyed(store, secondKey, firstKey).Verify(context.Background(), Verification{})
	require.NoError(t, err)
	assert.Equal(t, events[2].HMAC, head.HMAC)
	assert.Equal(t, 3, head.Events)
	_, err = VerifyChainKeys(events, bothKeys, Verification{})
	require.NoError(t, err)

	assert.ErrorIs(t, unkeyed(store, firstKey.Key).VerifyChain(context.Background()), ErrChainBroken,
		"the old key alone does not verify the rotated events")
}

func TestWithChainKeys_MarkerOncePerRotation(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, keyed(store, secondKey, firstKey), "a")
	logOne(t, keyed(store, secondKey, firstKey), "b")
	require.NoError(t, keyed(store, secondKey, firstKey).RecordRotation(context.Background()))

	var markers int
	for _, e := range store.Events() {
		if e.Action == RotationAction {
			markers++
		}
	}
	assert.Equal(t, 1, markers, "a restart under the same key adds no marker")
}

func TestVerify_RejectsOldKeyAfterRotation(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, keyed(store, firstKey), "first")
	require.NoError(t, keyed(store, secondKey, firstKey).RecordRotation(context.Background()))
	forge(t, store, firstKey, "retired-key")

	_, err := keyed(store, secondKey, firstKey).Verify(context.Background(), Verification{})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), `event[3] is sealed by chain key "1", older than the rotation`)
}

func TestVerify_RejectsNewKeyWithoutMarker(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, unkeyed(store, firstKey.Key), "first")
	forge(t, store, ChainKey{Key: secondKey.Key}, "no-marker")

	_, err := keyed(store, secondKey, firstKey).Verify(context.Background(), Verification{})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), "without its rotation marker")
}

func TestVerify_RejectsUnkeyedEventAfterRotation(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, keyed(store, secondKey, firstKey), "rotated")
	forge(t, store, ChainKey{Key: secondKey.Key}, "no-key-id")

	_, err := keyed(store, secondKey, firstKey).Verify(context.Background(), Verification{})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), "does not bind its chain key ID")
}

func TestVerify_AnchorCatchesTruncatedAndReextendedTail(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	logOne(t, keyed(store, firstKey), "first")
	beforeRotation := len(store.Events())
	logOne(t, keyed(store, secondKey, firstKey), "after-rotation")
	verifier := keyed(store, secondKey, firstKey)
	anchor, err := verifier.Verify(ctx, Verification{})
	require.NoError(t, err)

	truncate(store, beforeRotation)
	logOne(t, keyed(store, firstKey), "forged")

	_, err = verifier.Verify(ctx, Verification{})
	require.NoError(t, err, "without an anchor a leaked key can rewrite the tail back to the rotation")
	_, err = verifier.Verify(ctx, Verification{Anchor: anchor})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), "anchored head is no longer in the chain")
}

func TestVerify_AnchorCatchesShorterChain(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	l := keyed(store, secondKey, firstKey)
	for _, action := range []string{"a", "b", "c"} {
		logOne(t, l, action)
	}
	anchor, err := l.Verify(ctx, Verification{})
	require.NoError(t, err)

	logOne(t, l, "d")
	head, err := l.Verify(ctx, Verification{Anchor: anchor})
	require.NoError(t, err, "a chain that grew past the anchor verifies")
	assert.Equal(t, anchor.Events+1, head.Events)

	truncate(store, anchor.Events-1)
	_, err = l.Verify(ctx, Verification{Anchor: anchor})
	assert.ErrorIs(t, err, ErrChainBroken)
}

func TestVerify_AnchorAtWatermarkOfEmptyChain(t *testing.T) {
	watermark := bytes.Repeat([]byte{7}, hmacSize)
	head, err := VerifyChainKeys(nil, bothKeys, Verification{Watermark: watermark, Anchor: Head{HMAC: watermark, KeyID: "2"}})
	require.NoError(t, err)
	assert.Equal(t, Head{HMAC: watermark, KeyID: "2"}, head, "an empty chain's head names the key that seals the next event")
}

func TestLogE_RefusesRotationAction(t *testing.T) {
	err := keyed(NewMemoryStore(), secondKey).LogE(context.Background(), Event{Actor: "a", Action: RotationAction, Resource: "chain-keys/2", Status: "success"})
	assert.ErrorIs(t, err, ErrInvalidEvent)
}

func TestWithChainKeys_PanicsOnBadKeys(t *testing.T) {
	for name, keys := range map[string][]ChainKey{
		"short older key": {secondKey, {ID: "1", Key: []byte("short")}},
		"duplicate ID":    {secondKey, {ID: "2", Key: firstKey.Key}},
		"empty ID":        {{Key: firstKey.Key}},
		"ID with a dot":   {{ID: "a.b", Key: firstKey.Key}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() { keyed(NewMemoryStore(), keys[0], keys[1:]...) })
		})
	}
}

func TestVerifyChainKeys_RequiresAKey(t *testing.T) {
	_, err := VerifyChainKeys(nil, nil, Verification{})
	assert.ErrorIs(t, err, ErrNoChainKey)
	assert.NotErrorIs(t, err, ErrChainBroken, "a missing key is no tampering")
	_, err = VerifyChainKeys(nil, []ChainKey{{ID: "1", Key: []byte("short")}}, Verification{})
	assert.ErrorIs(t, err, ErrNoChainKey)
}

func TestWithCursorKeys_AcceptsCursorOfOlderKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	before := New(store, WithChainKey(testChainKey), WithCursorKey(testCursorKey))
	for _, action := range []string{"a", "b", "c"} {
		logOne(t, before, action)
	}
	_, cursor, err := before.List(ctx, Filter{}, "", 1)
	require.NoError(t, err)
	require.NotEmpty(t, cursor)

	after := New(store, WithChainKey(testChainKey), WithCursorKeys(rotatedCursorKey, testCursorKey))
	page, next, err := after.List(ctx, Filter{}, cursor, 1)
	require.NoError(t, err)
	assert.Len(t, page, 1)

	retired := New(store, WithChainKey(testChainKey), WithCursorKey(rotatedCursorKey))
	_, _, err = retired.List(ctx, Filter{}, cursor, 1)
	assert.ErrorIs(t, err, ErrInvalidCursor, "a dropped key no longer verifies its cursors")
	_, _, err = retired.List(ctx, Filter{}, next, 1)
	assert.NoError(t, err, "new cursors are signed with the newest key")
}

func TestClose_ZeroesOlderKeys(t *testing.T) {
	l := New(NewMemoryStore(), WithChainKeys(secondKey, firstKey), WithCursorKeys(rotatedCursorKey, testCursorKey))
	require.NoError(t, l.Close())
	assert.True(t, l.olderChainKeys[0].key.IsEmpty())
	assert.True(t, l.cursors.older[0].IsEmpty())
}

func TestVerify_AnchorRetentionRemovedHoldsTheEventsAfterIt(t *testing.T) {
	store := NewMemoryStore()
	l := keyed(store, secondKey, firstKey)
	for _, action := range []string{"a", "b", "c"} {
		logOne(t, l, action)
	}
	events := store.Events()
	watermark := events[0].HMAC
	head, err := VerifyChainKeys(events[1:], bothKeys, Verification{Watermark: watermark, Anchor: Head{HMAC: watermark, Events: 1, KeyID: "2"}})
	require.NoError(t, err, "retention removing the anchored event is no tamper")
	assert.Equal(t, events[len(events)-1].HMAC, head.HMAC)
}

func TestVerify_AnchorAtWatermarkRefusesAnOlderKeyAfterIt(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, unkeyed(store, secondKey.Key), "a")
	watermark := store.Events()[0].HMAC
	forge(t, store, ChainKey{Key: firstKey.Key}, "forged")

	_, err := VerifyChainKeys(store.Events()[1:], bothKeys, Verification{Watermark: watermark, Anchor: Head{HMAC: watermark, Events: 1, KeyID: "2"}})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), `older than the anchored key "2"`)
}

func TestVerify_AnchoredEventSealedByOlderKeyFails(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, unkeyed(store, firstKey.Key), "first")
	events := store.Events()

	forged := Head{HMAC: events[0].HMAC, Events: 1, KeyID: "2"}
	_, err := VerifyChainKeys(events, bothKeys, Verification{Anchor: forged})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), `anchored event[0] is sealed by chain key "1", not the anchored key "2"`)
}

func TestVerify_NoEventFromTheAnchorOnUsesAnOlderKey(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, unkeyed(store, firstKey.Key), "first")
	forge(t, store, ChainKey{Key: secondKey.Key}, "second")
	events := store.Events()
	forge(t, store, ChainKey{Key: firstKey.Key}, "older")

	// From the watermark on, event[1] is the first event, which may carry
	// any key; only the anchor on it refuses the older key after it.
	anchor := Head{HMAC: events[1].HMAC, Events: 2, KeyID: "2"}
	_, err := VerifyChainKeys(store.Events()[1:], bothKeys, Verification{Watermark: events[0].HMAC, Anchor: anchor})
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), `event[1] is sealed by chain key "1", older than the anchored key "2"`)
}

func TestVerify_AnchorAtAnotherPositionFails(t *testing.T) {
	store := NewMemoryStore()
	l := keyed(store, secondKey, firstKey)
	logOne(t, l, "a")
	head, err := l.Verify(context.Background(), Verification{})
	require.NoError(t, err)
	head.Events++
	_, err = l.Verify(context.Background(), Verification{Anchor: head})
	assert.ErrorIs(t, err, ErrChainBroken)
}

func TestVerify_AnchoredRotatedChainPasses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	logOne(t, unkeyed(store, firstKey.Key), "before")
	l := keyed(store, secondKey, firstKey)
	logOne(t, l, "after")
	anchor, err := l.Verify(ctx, Verification{})
	require.NoError(t, err)
	assert.Equal(t, "2", anchor.KeyID)
	assert.Equal(t, 3, anchor.Events)
	logOne(t, l, "later")
	head, err := l.Verify(ctx, Verification{Anchor: anchor})
	require.NoError(t, err)
	assert.Equal(t, Head{HMAC: store.Events()[3].HMAC, Events: 4, KeyID: "2"}, head)
}

func TestVerify_AnchoredKeyMustBeListed(t *testing.T) {
	_, err := VerifyChainKeys(nil, bothKeys, Verification{Anchor: Head{HMAC: bytes.Repeat([]byte{1}, hmacSize), KeyID: "9"}})
	assert.ErrorIs(t, err, ErrChainBroken)
}

func TestVerify_RetentionPastTheAnchorIsAnchorPruned(t *testing.T) {
	store := NewMemoryStore()
	l := keyed(store, secondKey, firstKey)
	for _, action := range []string{"a", "b", "c", "d"} {
		logOne(t, l, action)
	}
	events := store.Events()
	anchor := Head{HMAC: events[1].HMAC, Events: 2, KeyID: "2"}

	_, err := VerifyChainKeys(events[3:], bothKeys, Verification{Watermark: events[2].HMAC, Anchor: anchor})
	require.ErrorIs(t, err, ErrAnchorPruned)
	assert.NotErrorIs(t, err, ErrChainBroken)
	assert.NotContains(t, err.Error(), "truncated")

	_, err = VerifyChainKeys(nil, bothKeys, Verification{Watermark: events[4].HMAC, Anchor: anchor})
	assert.ErrorIs(t, err, ErrAnchorPruned, "retention that removed every event")
}

func TestVerify_OlderKeyPastAPrunedAnchorIsChainBroken(t *testing.T) {
	store := NewMemoryStore()
	logOne(t, keyed(store, secondKey, firstKey), "a")
	watermark := store.Events()[1].HMAC
	forge(t, store, firstKey, "forged")

	anchor := Head{HMAC: store.Events()[0].HMAC, Events: 1, KeyID: "2"}
	_, err := VerifyChainKeys(store.Events()[2:], bothKeys, Verification{Watermark: watermark, Anchor: anchor})
	require.ErrorIs(t, err, ErrChainBroken, "a key older than the anchored one cannot follow it, pruned or not")
	assert.NotErrorIs(t, err, ErrAnchorPruned)
}
