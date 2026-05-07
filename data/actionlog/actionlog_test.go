package actionlog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is a minimal in-test Store implementation. The real
// data/actionlog/memory package depends on this module, so we don't
// import it here; this private double keeps the unit tests
// self-contained.
type memStore struct {
	entries map[string]Entry
}

func newMemStore() *memStore { return &memStore{entries: make(map[string]Entry)} }

func (s *memStore) Append(_ context.Context, e Entry) error {
	s.entries[e.ID] = e
	return nil
}

func (s *memStore) Get(_ context.Context, id string) (Entry, error) {
	e, ok := s.entries[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (s *memStore) List(_ context.Context, _ Query) ([]Entry, error) {
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	return out, nil
}

func newTestSecrets(t *testing.T) *StaticSecrets {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	require.Len(t, key, 32)
	return NewStaticSecrets("k1", map[string][]byte{"k1": key})
}

func TestAppend_SetsIDAndTimestamp(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	e, err := logger.Append(context.Background(), Entry{
		TenantID: "t1",
		Actor:    "agent-1",
		Action:   "user.delete",
		Resource: "users/42",
		Outcome:  OutcomeSuccess,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, e.ID)
	assert.NotEmpty(t, e.Signature)
	assert.Equal(t, "k1", e.SignatureKeyID)
	assert.False(t, e.OccurredAt.IsZero())
}

func TestAppend_RejectsMissingFields(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	cases := []struct {
		name string
		e    Entry
	}{
		{"no tenant", Entry{Actor: "a", Action: "x", Outcome: OutcomeSuccess}},
		{"no actor", Entry{TenantID: "t", Action: "x", Outcome: OutcomeSuccess}},
		{"no action", Entry{TenantID: "t", Actor: "a", Outcome: OutcomeSuccess}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := logger.Append(context.Background(), c.e)
			assert.ErrorIs(t, err, ErrInvalidEntry)
		})
	}
}

func TestAppend_RejectsBadOutcome(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	_, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: Outcome("bogus"),
	})
	assert.ErrorIs(t, err, ErrInvalidOutcome)
}

func TestGet_VerifiesSignature(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	written, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, written, got)
}

func TestGet_DetectsTamper(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	e, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	// Mutate the stored entry directly. This is the attacker model: a
	// DBA who edits a row, or a forged entry inserted by some path
	// that bypassed the Logger.
	tampered := store.entries[e.ID]
	tampered.Actor = "rogue"
	store.entries[e.ID] = tampered

	_, err = logger.Get(context.Background(), e.ID)
	assert.ErrorIs(t, err, ErrSignatureInvalid)
}

func TestVerify_RejectsUnknownKeyID(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	e, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	// Mutate the row to point at a key id the source doesn't know.
	tampered := store.entries[e.ID]
	tampered.SignatureKeyID = "rotated-out-key"
	store.entries[e.ID] = tampered

	_, err = logger.Get(context.Background(), e.ID)
	assert.ErrorIs(t, err, ErrUnknownKeyID)
}

func TestList_FailsClosedOnTamperedEntry(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	good, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	bad, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "b", Action: "y", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)
	tampered := store.entries[bad.ID]
	tampered.Reason = "rewritten after the fact"
	store.entries[bad.ID] = tampered

	_, err = logger.List(context.Background(), Query{})
	assert.ErrorIs(t, err, ErrSignatureInvalid)
	_ = good
}

// TestSign_NewlineInjectionDoesNotCollide is the regression test for
// the L-1 audit fix: the canonical form is length-prefixed so a
// field value containing a literal newline cannot shift the field
// boundary in the canonical bytes. Two distinct entries with
// different field assignments must produce different signatures.
//
// Pre-fix (newline-joined), an entry with Reason="x\nfoo" and
// Action="bar" would canonicalise the same as Reason="x" and
// Action="foo\nbar" — both renderings concatenate to the same byte
// sequence after the join. Post-fix the length prefix on each field
// makes the parse unambiguous.
func TestSign_NewlineInjectionDoesNotCollide(t *testing.T) {
	logger := New(newMemStore(), newTestSecrets(t))
	now := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)

	a := Entry{
		ID: "e", TenantID: "t", Actor: "a",
		Action:     "do",
		Resource:   "r",
		Outcome:    OutcomeSuccess,
		Reason:     "x\nfoo",
		OccurredAt: now,
	}
	b := Entry{
		ID: "e", TenantID: "t", Actor: "a",
		Action:     "do\nfoo",
		Resource:   "r",
		Outcome:    OutcomeSuccess,
		Reason:     "x",
		OccurredAt: now,
	}

	sigA, _, err := logger.Sign(a)
	require.NoError(t, err)
	sigB, _, err := logger.Sign(b)
	require.NoError(t, err)
	assert.NotEqual(t, sigA, sigB,
		"length-prefix canonical form must distinguish entries that differ only in newline placement")
}

func TestSign_DeterministicAcrossInvocations(t *testing.T) {
	logger := New(newMemStore(), newTestSecrets(t))
	e := Entry{
		ID:         "e1",
		TenantID:   "t",
		Actor:      "a",
		Action:     "x",
		Outcome:    OutcomeSuccess,
		OccurredAt: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC),
		Metadata:   map[string]any{"b": 1, "a": 2, "z": map[string]any{"y": 3, "x": 4}},
	}
	s1, _, err := logger.Sign(e)
	require.NoError(t, err)
	s2, _, err := logger.Sign(e)
	require.NoError(t, err)
	assert.Equal(t, s1, s2)
}

func TestSign_OrderInsensitiveMetadata(t *testing.T) {
	// The signature must be the same regardless of the source map's
	// internal iteration order, because canonicalisation sorts keys.
	logger := New(newMemStore(), newTestSecrets(t))
	base := Entry{
		ID:         "e1",
		TenantID:   "t",
		Actor:      "a",
		Action:     "x",
		Outcome:    OutcomeSuccess,
		OccurredAt: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC),
	}
	a := base
	a.Metadata = map[string]any{"alpha": 1, "beta": 2}
	b := base
	b.Metadata = map[string]any{"beta": 2, "alpha": 1}

	sa, _, err := logger.Sign(a)
	require.NoError(t, err)
	sb, _, err := logger.Sign(b)
	require.NoError(t, err)
	assert.Equal(t, sa, sb)
}

func TestRotation_VerifiesEntriesSignedWithOlderKey(t *testing.T) {
	// Append with key v1.
	old := []byte("old-key-old-key-old-key-old-key-")
	require.Len(t, old, 32)
	v1 := NewStaticSecrets("v1", map[string][]byte{"v1": old})

	store := newMemStore()
	logger := New(store, v1)
	written, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	// Rotate: current is v2, but v1 still resolvable.
	newKey := []byte("new-key-new-key-new-key-new-key-")
	require.Len(t, newKey, 32)
	v2 := NewStaticSecrets("v2", map[string][]byte{
		"v1": old,
		"v2": newKey,
	})

	rotatedLogger := New(store, v2)

	// Old entry still verifies via its embedded key id.
	got, err := rotatedLogger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, "v1", got.SignatureKeyID)

	// New entries are signed with v2.
	fresh, err := rotatedLogger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", fresh.SignatureKeyID)
}

func TestNew_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { New(nil, newTestSecrets(t)) })
	assert.Panics(t, func() { New(newMemStore(), nil) })
}

func TestNewStaticSecrets_PanicsOnUnknownCurrent(t *testing.T) {
	assert.Panics(t, func() {
		NewStaticSecrets("missing", map[string][]byte{"v1": make([]byte, 32)})
	})
}

func TestNewStaticSecrets_PanicsOnShortKey(t *testing.T) {
	assert.Panics(t, func() {
		NewStaticSecrets("v1", map[string][]byte{"v1": []byte("too-short")})
	})
}
