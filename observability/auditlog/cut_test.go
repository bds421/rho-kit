package auditlog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testRetentionKey = bytes.Repeat([]byte("r"), MinRetentionKeyLen)
	day              = 24 * time.Hour
	cutNow           = time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
)

// trail appends one event per age, oldest first, ages in days before cutNow.
func trail(t *testing.T, ages ...float64) (*MemoryStore, *Logger) {
	t.Helper()
	store := NewMemoryStore()
	l := newTestLogger(store)
	for _, age := range ages {
		at := cutNow.Add(-time.Duration(age * float64(day)))
		require.NoError(t, l.LogE(context.Background(), Event{Timestamp: at, Actor: "a", Action: "x", Resource: "r", Status: StatusSuccess}))
	}
	return store, l
}

func TestComputeCut_TheOldestRunBeforeTheCut(t *testing.T) {
	store, l := trail(t, 500, 450, 400, 10, 450, 5)
	events := store.Events()
	c, err := l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.NoError(t, err)
	assert.EqualValues(t, 3, c.Events, "the backdated event after a kept one stays")
	assert.Equal(t, events[2].HMAC, c.Watermark)
	assert.Equal(t, cutNow.Add(-365*day), c.Before)
	assert.Equal(t, cutNow, c.IssuedAt)
	hmacs := [][]byte{events[0].HMAC, events[1].HMAC, events[2].HMAC, events[3].HMAC}
	require.NoError(t, c.Holds(hmacs))
}

func TestComputeCut_TheNewestEventStays(t *testing.T) {
	store, l := trail(t, 500, 450, 400)
	c, err := l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.NoError(t, err)
	assert.EqualValues(t, 2, c.Events)
	assert.Equal(t, store.Events()[2].Timestamp, c.Before, "the cut moves back to the kept event")

	_, l = trail(t, 500, 450, 460)
	c, err = l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.NoError(t, err)
	assert.Zero(t, c.Events, "a backdated newest event keeps the run")

	_, l = trail(t, 500)
	c, err = l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.NoError(t, err)
	assert.Zero(t, c.Events)
}

func TestComputeCut_RefusesABrokenChain(t *testing.T) {
	store, l := trail(t, 500, 400, 10)
	store.events[1].Actor = "b"
	_, err := l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.ErrorIs(t, err, ErrChainBroken)
	_, err = ComputeCut(context.Background(), store, []ChainKey{{Key: testChainKey}}, Verification{}, cutNow.Add(-365*day), cutNow)
	require.ErrorIs(t, err, ErrChainBroken)
}

func TestCut_HoldsOnlyTheNamedEvents(t *testing.T) {
	store, l := trail(t, 500, 450, 10)
	events := store.Events()
	c, err := l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.NoError(t, err)
	require.EqualValues(t, 2, c.Events)
	for name, hmacs := range map[string][][]byte{
		"none stays":   {events[0].HMAC, events[1].HMAC},
		"one replaced": {events[0].HMAC, events[2].HMAC, events[2].HMAC},
		"one more":     {events[0].HMAC, events[1].HMAC, events[2].HMAC, events[2].HMAC},
	} {
		assert.ErrorIs(t, c.Holds(hmacs), ErrCutRefused, name)
	}
	assert.ErrorIs(t, Cut{}.Holds(nil), ErrCutRefused)
}

func TestCut_EncodeDecode(t *testing.T) {
	_, l := trail(t, 500, 450, 10)
	c, err := l.ComputeCut(context.Background(), Verification{}, cutNow.Add(-365*day), cutNow)
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := NewEd25519CutSigner(private)
	require.NoError(t, err)
	verifier, err := NewEd25519CutVerifier(public)
	require.NoError(t, err)

	text, err := c.Encode(signer)
	require.NoError(t, err)
	got, err := DecodeCut(text, verifier, cutNow.Add(30*time.Minute), time.Hour)
	require.NoError(t, err)
	assert.Equal(t, c, got)

	otherPublic, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	other, err := NewEd25519CutVerifier(otherPublic)
	require.NoError(t, err)
	for name, check := range map[string]func() (Cut, error){
		"another key": func() (Cut, error) { return DecodeCut(text, other, cutNow, time.Hour) },
		"expired":     func() (Cut, error) { return DecodeCut(text, verifier, cutNow.Add(2*time.Hour), time.Hour) },
		"from later":  func() (Cut, error) { return DecodeCut(text, verifier, cutNow.Add(-2*time.Minute), time.Hour) },
		"not base64":  func() (Cut, error) { return DecodeCut("!", verifier, cutNow, time.Hour) },
		"changed":     func() (Cut, error) { return DecodeCut(changedCut(t, text), verifier, cutNow, time.Hour) },
	} {
		_, err := check()
		assert.ErrorIs(t, err, ErrCutRefused, name)
	}
	_, err = DecodeCut(text, verifier, cutNow, 0)
	assert.Error(t, err)
	_, err = NewEd25519CutSigner(private[:10])
	assert.Error(t, err)
	_, err = NewEd25519CutVerifier(public[:10])
	assert.Error(t, err)
}

func changedCut(t *testing.T, text string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(text)
	require.NoError(t, err)
	var e map[string]any
	require.NoError(t, json.Unmarshal(raw, &e))
	e["events"] = 3
	raw, err = json.Marshal(e)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestCut_Removable(t *testing.T) {
	c := Cut{Before: cutNow.Add(-400 * day), IssuedAt: cutNow}
	require.NoError(t, c.Removable(365*day, cutNow))
	assert.ErrorIs(t, c.Removable(500*day, cutNow), ErrRetentionTooShort)
	assert.ErrorIs(t, c.Removable(-day, cutNow), ErrRetentionTooShort)
	c.Events = 1
	assert.ErrorIs(t, c.Removable(365*day, cutNow), ErrCutRefused, "a count without watermark and digest")
}
