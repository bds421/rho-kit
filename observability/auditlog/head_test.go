package auditlog

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fiveEvents is a rotated chain of a marker and four events.
func fiveEvents(t *testing.T) []Event {
	t.Helper()
	store := NewMemoryStore()
	l := keyed(store, secondKey, firstKey)
	for _, action := range []string{"a", "b", "c", "d"} {
		logOne(t, l, action)
	}
	return store.Events()
}

func TestVerify_RemovedCountsPositionsFromTheFirstEventEver(t *testing.T) {
	events := fiveEvents(t)
	v := Verification{Watermark: events[1].HMAC, Removed: 2}
	head, err := VerifyChainKeys(events[2:], bothKeys, v)
	require.NoError(t, err)
	assert.Equal(t, Head{HMAC: events[4].HMAC, Events: 5, KeyID: "2"}, head)

	v.Anchor = Head{HMAC: events[3].HMAC, Events: 4, KeyID: "2"}
	_, err = VerifyChainKeys(events[2:], bothKeys, v)
	require.NoError(t, err, "the anchor at its position past the watermark")

	v.Anchor.Events = 3
	_, err = VerifyChainKeys(events[2:], bothKeys, v)
	require.ErrorIs(t, err, ErrChainBroken)
	assert.Contains(t, err.Error(), "the anchored event is event[3], not event[2]")
}

func TestVerify_RemovedTellsAPrunedAnchorFromACutTail(t *testing.T) {
	events := fiveEvents(t)
	anchor := Head{HMAC: events[2].HMAC, Events: 3, KeyID: "2"}

	_, err := VerifyChainKeys(events[4:], bothKeys, Verification{Watermark: events[3].HMAC, Removed: 4, Anchor: anchor})
	require.ErrorIs(t, err, ErrAnchorPruned, "retention removed the anchor")

	_, err = VerifyChainKeys(events[:2], bothKeys, Verification{Anchor: anchor})
	require.ErrorIs(t, err, ErrChainBroken, "the tail from the anchor cut without a watermark")

	_, err = VerifyChainKeys(events[2:2], bothKeys, Verification{Watermark: events[1].HMAC, Removed: 2, Anchor: anchor})
	require.ErrorIs(t, err, ErrChainBroken, "the tail from the anchor cut past what retention removed")
	assert.NotErrorIs(t, err, ErrAnchorPruned)
}

func TestVerify_AnchorAtTheWatermarkMustBeTheLastRemoved(t *testing.T) {
	events := fiveEvents(t)
	anchor := Head{HMAC: events[1].HMAC, Events: 2, KeyID: "2"}
	head, err := VerifyChainKeys(events[2:], bothKeys, Verification{Watermark: events[1].HMAC, Removed: 2, Anchor: anchor})
	require.NoError(t, err)
	assert.Equal(t, 5, head.Events)

	_, err = VerifyChainKeys(events[2:], bothKeys, Verification{Watermark: events[1].HMAC, Removed: 3, Anchor: anchor})
	assert.ErrorIs(t, err, ErrChainBroken)
}

func TestVerify_RemovedNeedsAWatermark(t *testing.T) {
	events := fiveEvents(t)
	for _, v := range []Verification{{Removed: 1}, {Watermark: events[0].HMAC, Removed: -1}} {
		_, err := VerifyChainKeys(events, bothKeys, v)
		assert.ErrorIs(t, err, errRemovedWithoutWatermark)
	}
}

func TestVerifyStore_NeedsOnlyTheChainKeys(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	l := keyed(store, secondKey, firstKey)
	logOne(t, l, "a")
	want, err := l.Verify(ctx, Verification{})
	require.NoError(t, err)

	head, err := VerifyStore(ctx, store, bothKeys, Verification{Anchor: want})
	require.NoError(t, err)
	assert.Equal(t, want, head)

	_, err = VerifyStore(ctx, store, []ChainKey{firstKey}, Verification{})
	assert.ErrorIs(t, err, ErrChainBroken)
}

func TestHead_TextRoundTrips(t *testing.T) {
	for _, h := range []Head{
		{HMAC: bytes.Repeat([]byte{0xab}, hmacSize), Events: 12, KeyID: "k-2"},
		{HMAC: bytes.Repeat([]byte{1}, hmacSize), Events: 3},
		{Events: 0, KeyID: "2"},
		{},
	} {
		text, err := h.MarshalText()
		require.NoError(t, err)
		var back Head
		require.NoError(t, back.UnmarshalText(text), string(text))
		assert.Equal(t, h.Events, back.Events)
		assert.Equal(t, h.KeyID, back.KeyID)
		assert.True(t, bytes.Equal(h.HMAC, back.HMAC))
	}
	text, _ := Head{HMAC: []byte{0xab, 0xcd}, Events: 7, KeyID: "2"}.MarshalText()
	assert.Equal(t, "7:2:abcd", string(text))
}

func TestHead_UnmarshalTextRefusesWhatItDidNotWrite(t *testing.T) {
	for _, text := range []string{"", "7:2", "7:2:ab:cd", "x:2:ab", "-1:2:ab", "07:2:ab", "7:k.y:ab", "7:2:zz", "7:2:"} {
		var h Head
		assert.ErrorIs(t, h.UnmarshalText([]byte(text)), ErrInvalidHead, text)
	}
}
