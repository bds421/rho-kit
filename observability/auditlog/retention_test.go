package auditlog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRetention = 400 * 24 * time.Hour
	testMinimum   = 365 * 24 * time.Hour
)

func verifyRetained(t *testing.T, l *Logger, store *MemoryStore, anchor Head, now time.Time) (Head, error) {
	t.Helper()
	record, err := store.RetentionRecord(context.Background(), testRetentionKey)
	require.NoError(t, err)
	v := record.Verification(testRetention, testMinimum, now)
	v.Anchor = anchor
	return l.Verify(context.Background(), v)
}

func TestPrune_RemovesTheCutAndKeepsTheChainVerifiable(t *testing.T) {
	ctx := context.Background()
	store, l := trail(t, 500, 450, 10, 5)
	kept, err := l.Verify(ctx, Verification{})
	require.NoError(t, err)
	early, err := VerifyChainKeys(store.Events()[:1], []ChainKey{{Key: testChainKey}}, Verification{})
	require.NoError(t, err)
	events := store.Events()

	removed, err := l.Prune(ctx, testRetentionKey, testRetention, testMinimum, cutNow)
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
	assert.Len(t, store.Events(), 2)

	record, err := store.RetentionRecord(ctx, testRetentionKey)
	require.NoError(t, err)
	assert.Equal(t, RetentionRecord{Watermark: events[1].HMAC, Removed: 2, PrunedBefore: cutNow.Add(-testRetention), PrunedAt: cutNow}, record)

	head, err := verifyRetained(t, l, store, kept, cutNow)
	require.NoError(t, err)
	assert.Equal(t, kept, head, "positions count the removed events")
	_, err = verifyRetained(t, l, store, early, cutNow)
	require.ErrorIs(t, err, ErrAnchorPruned)

	removed, err = l.Prune(ctx, testRetentionKey, testRetention, testMinimum, cutNow)
	require.NoError(t, err)
	assert.Zero(t, removed, "nothing more is due")
	_, err = l.Prune(ctx, testRetentionKey, 300*24*time.Hour, testMinimum, cutNow)
	require.ErrorIs(t, err, ErrRetentionTooShort)
}

func TestPrune_ARecordWithoutItsSealFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, l := trail(t, 500, 450, 10)
	_, err := l.Prune(ctx, testRetentionKey, testRetention, testMinimum, cutNow)
	require.NoError(t, err)

	store.record.Removed = 1
	_, err = store.RetentionRecord(ctx, testRetentionKey)
	require.ErrorIs(t, err, ErrRecordUnsealed)
	_, err = l.Prune(ctx, testRetentionKey, testRetention, testMinimum, cutNow)
	require.ErrorIs(t, err, ErrRecordUnsealed)

	store.record.Removed = 2
	other := make([]byte, MinRetentionKeyLen)
	_, err = store.RetentionRecord(ctx, other)
	require.ErrorIs(t, err, ErrRecordUnsealed)
	_, err = store.RetentionRecord(ctx, other[:8])
	require.Error(t, err)
}

func TestApplyCut_RefusesACutTheChainNoLongerHolds(t *testing.T) {
	ctx := context.Background()
	store, l := trail(t, 500, 450, 10)
	c, err := l.ComputeCut(ctx, Verification{}, cutNow.Add(-testRetention), cutNow)
	require.NoError(t, err)
	_, err = store.ApplyCut(ctx, c, testRetentionKey, testRetention+day, cutNow)
	require.ErrorIs(t, err, ErrRetentionTooShort)

	removed, err := store.ApplyCut(ctx, c, testRetentionKey, testMinimum, cutNow)
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
	_, err = store.ApplyCut(ctx, c, testRetentionKey, testMinimum, cutNow)
	require.ErrorIs(t, err, ErrCutRefused, "the same cut again")
}

func TestVerification_TheFirstEventLeftIsNotOlderThanTheCut(t *testing.T) {
	ctx := context.Background()
	store, l := trail(t, 500, 450, 10)
	_, err := l.Prune(ctx, testRetentionKey, testRetention, testMinimum, cutNow)
	require.NoError(t, err)

	record, err := store.RetentionRecord(ctx, testRetentionKey)
	require.NoError(t, err)
	record.PrunedBefore = cutNow
	_, err = l.Verify(ctx, record.Verification(testRetention, 0, cutNow.Add(testRetention+day)))
	require.ErrorIs(t, err, ErrChainBroken)
}

func TestRetentionRecord_Verification(t *testing.T) {
	assert.Equal(t, Verification{}, RetentionRecord{}.Verification(testRetention, testMinimum, cutNow))
	r := RetentionRecord{Watermark: []byte{1}, Removed: 3, PrunedBefore: cutNow.Add(-10 * 24 * time.Hour)}
	v := r.Verification(30*24*time.Hour, testMinimum, cutNow)
	assert.Equal(t, cutNow.Add(-testMinimum), v.PrunedBefore, "never within the minimum")
	assert.Equal(t, 3, v.Removed)
	r.PrunedBefore = cutNow.Add(-500 * 24 * time.Hour)
	assert.Equal(t, r.PrunedBefore, r.Verification(testRetention, testMinimum, cutNow).PrunedBefore)
}

func TestPrune_NeedsARetentionStore(t *testing.T) {
	l := newTestLogger(&headlessStore{})
	_, err := l.Prune(context.Background(), testRetentionKey, testRetention, testMinimum, cutNow)
	require.ErrorIs(t, err, ErrNotRetentionStore)
}

type headlessStore struct{ Store }
