package auditlog

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	rotatedChainKey  = bytes.Repeat([]byte("d"), MinChainKeyLen)
	rotatedCursorKey = bytes.Repeat([]byte("v"), MinCursorKeyLen)
)

func logOne(t *testing.T, l *Logger, action string) {
	t.Helper()
	require.NoError(t, l.LogE(context.Background(), Event{Actor: "a", Action: action, Resource: "r", Status: "success"}))
}

func TestWithChainKeys_VerifiesAcrossRotation(t *testing.T) {
	store := NewMemoryStore()
	before := New(store, WithChainKey(testChainKey), WithCursorKey(testCursorKey))
	logOne(t, before, "first")

	after := New(store, WithChainKeys(rotatedChainKey, testChainKey), WithCursorKey(testCursorKey))
	logOne(t, after, "second")

	require.NoError(t, after.VerifyChain(context.Background()))
	require.NoError(t, VerifyChainKeysFrom(store.Events(), [][]byte{rotatedChainKey, testChainKey}, nil))

	err := before.VerifyChain(context.Background())
	assert.ErrorIs(t, err, ErrChainBroken, "the old key alone does not verify the rotated event")
}

func TestWithChainKeys_RejectsOlderKeyAfterNewer(t *testing.T) {
	store := NewMemoryStore()
	rotated := New(store, WithChainKey(rotatedChainKey), WithCursorKey(testCursorKey))
	logOne(t, rotated, "first")
	retired := New(store, WithChainKey(testChainKey), WithCursorKey(testCursorKey))
	logOne(t, retired, "second")

	verifier := New(store, WithChainKeys(rotatedChainKey, testChainKey), WithCursorKey(testCursorKey))
	err := verifier.VerifyChain(context.Background())
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), "event[1]")
	assert.ErrorIs(t, VerifyChainKeysFrom(store.Events(), [][]byte{rotatedChainKey, testChainKey}, nil), ErrChainBroken)
}

func TestWithChainKeys_PanicsOnShortOlderKey(t *testing.T) {
	assert.Panics(t, func() {
		New(NewMemoryStore(), WithChainKeys(rotatedChainKey, []byte("short")), WithCursorKey(testCursorKey))
	})
}

func TestVerifyChainKeysFrom_RequiresAKey(t *testing.T) {
	assert.ErrorIs(t, VerifyChainKeysFrom(nil, nil, nil), ErrChainBroken)
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
	assert.True(t, errors.Is(err, ErrInvalidCursor), "a dropped key no longer verifies its cursors")
	_, _, err = retired.List(ctx, Filter{}, next, 1)
	assert.NoError(t, err, "new cursors are signed with the newest key")
}

func TestClose_ZeroesOlderKeys(t *testing.T) {
	l := New(NewMemoryStore(), WithChainKeys(rotatedChainKey, testChainKey), WithCursorKeys(rotatedCursorKey, testCursorKey))
	l.Close()
	assert.True(t, l.olderChainKeys[0].IsEmpty())
	assert.True(t, l.cursors.older[0].IsEmpty())
}
