package actionlog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
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
	mu      sync.Mutex
	entries map[string]Entry
	order   []string
	latest  map[string]Entry
	seq     map[string]int64
}

func newMemStore() *memStore {
	return &memStore{
		entries: make(map[string]Entry),
		latest:  make(map[string]Entry),
		seq:     make(map[string]int64),
	}
}

func (s *memStore) AppendChained(_ context.Context, tenantID string, build func(prev Entry, prevSeq int64) (Entry, error)) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.latest[tenantID]
	prevSeq := s.seq[tenantID]
	entry, err := build(prev, prevSeq)
	if err != nil {
		return Entry{}, err
	}
	s.entries[entry.ID] = entry
	s.order = append(s.order, entry.ID)
	s.latest[tenantID] = entry
	s.seq[tenantID] = entry.Seq
	return entry, nil
}

func (s *memStore) Get(_ context.Context, id string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (s *memStore) List(_ context.Context, q Query) ([]Entry, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, id := range s.order {
		e := s.entries[id]
		if q.TenantID != "" && e.TenantID != q.TenantID {
			continue
		}
		out = append(out, e)
	}
	return out, "", nil
}

func (s *memStore) RangeByTenantSeq(ctx context.Context, tenantID string, fn func(Entry) error) error {
	if fn == nil {
		return ErrInvalidEntry
	}
	s.mu.Lock()
	entries := make([]Entry, 0)
	for _, id := range s.order {
		e := s.entries[id]
		if e.TenantID == tenantID {
			entries = append(entries, e)
		}
	}
	s.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func newTestSecrets(t *testing.T) *StaticSecrets {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	require.Len(t, key, 32)
	return NewStaticSecrets("k1", map[string][]byte{"k1": key})
}

type stubSecrets struct {
	current string
	keys    map[string][]byte
}

func (s stubSecrets) CurrentKeyID(context.Context) (string, error) { return s.current, nil }

func (s stubSecrets) Resolve(_ context.Context, keyID string) ([]byte, error) {
	k, ok := s.keys[keyID]
	if !ok {
		return nil, ErrUnknownKeyID
	}
	return k, nil
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

func TestAppend_RejectsInvalidBoundedFields(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	base := Entry{
		TenantID: "tenant",
		Actor:    "agent",
		Action:   "user.delete",
		Resource: "users/42",
		Reason:   "ok",
		Outcome:  OutcomeSuccess,
	}
	cases := []struct {
		name string
		mut  func(*Entry)
	}{
		{"id too long", func(e *Entry) { e.ID = strings.Repeat("a", MaxIDLen+1) }},
		{"tenant too long", func(e *Entry) { e.TenantID = strings.Repeat("t", MaxTenantIDLen+1) }},
		{"tenant invalid token", func(e *Entry) { e.TenantID = "tenant/1" }},
		{"actor too long", func(e *Entry) { e.Actor = strings.Repeat("a", MaxActorLen+1) }},
		{"actor invalid utf8", func(e *Entry) { e.Actor = string([]byte{0xff}) }},
		{"actor contains nul", func(e *Entry) { e.Actor = "agent\x001" }},
		{"actor contains newline", func(e *Entry) { e.Actor = "agent\n1" }},
		{"actor contains space", func(e *Entry) { e.Actor = "agent 1" }},
		{"actor contains tab", func(e *Entry) { e.Actor = "agent\t1" }},
		{"action too long", func(e *Entry) { e.Action = strings.Repeat("x", MaxActionLen+1) }},
		{"action contains carriage return", func(e *Entry) { e.Action = "user\rdelete" }},
		{"action contains space", func(e *Entry) { e.Action = "user delete" }},
		{"resource too long", func(e *Entry) { e.Resource = strings.Repeat("r", MaxResourceLen+1) }},
		{"resource contains newline", func(e *Entry) { e.Resource = "users\n42" }},
		{"resource contains space", func(e *Entry) { e.Resource = "users 42" }},
		{"reason too long", func(e *Entry) { e.Reason = strings.Repeat("r", MaxReasonLen+1) }},
		{"reason invalid utf8", func(e *Entry) { e.Reason = string([]byte{0xff}) }},
		{"reason contains nul", func(e *Entry) { e.Reason = "reason\x00bad" }},
		{"metadata too large", func(e *Entry) { e.Metadata = map[string]any{"blob": strings.Repeat("x", MaxMetadataBytes+1)} }},
		{"metadata too many entries", func(e *Entry) {
			e.Metadata = make(map[string]any, MaxMetadataEntries+1)
			for i := 0; i < MaxMetadataEntries+1; i++ {
				e.Metadata[fmt.Sprintf("k%d", i)] = i
			}
		}},
		{"metadata invalid key", func(e *Entry) { e.Metadata = map[string]any{"bad key": "value"} }},
		{"metadata invalid string", func(e *Entry) { e.Metadata = map[string]any{"k": "bad\x00value"} }},
		{"metadata too deep", func(e *Entry) {
			var cur any = "leaf"
			for i := 0; i < MaxMetadataDepth+1; i++ {
				cur = []any{cur}
			}
			e.Metadata = map[string]any{"deep": cur}
		}},
		{"metadata cycle", func(e *Entry) {
			m := map[string]any{}
			m["self"] = m
			e.Metadata = m
		}},
		{"metadata unsupported value", func(e *Entry) { e.Metadata = map[string]any{"fn": func() {}} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entry := base
			c.mut(&entry)
			_, err := logger.Append(context.Background(), entry)
			assert.ErrorIs(t, err, ErrInvalidEntry)
			if c.name == "id too long" {
				assert.NotContains(t, err.Error(), "128")
				assert.NotContains(t, err.Error(), "129")
			}
		})
	}
}

func TestAppend_RejectsLongSignatureKeyID(t *testing.T) {
	logger := New(newMemStore(), stubSecrets{
		current: strings.Repeat("k", MaxSignatureKeyIDLen+1),
		keys: map[string][]byte{
			strings.Repeat("k", MaxSignatureKeyIDLen+1): []byte("0123456789abcdef0123456789abcdef"),
		},
	})

	_, err := logger.Append(context.Background(), Entry{
		TenantID: "t",
		Actor:    "a",
		Action:   "x",
		Outcome:  OutcomeSuccess,
	})
	assert.ErrorIs(t, err, ErrInvalidEntry)
}

func TestAppend_RejectsControlBytesInSignatureKeyID(t *testing.T) {
	logger := New(newMemStore(), stubSecrets{
		current: "k\n1",
		keys: map[string][]byte{
			"k\n1": []byte("0123456789abcdef0123456789abcdef"),
		},
	})

	_, err := logger.Append(context.Background(), Entry{
		TenantID: "t",
		Actor:    "a",
		Action:   "x",
		Outcome:  OutcomeSuccess,
	})
	assert.ErrorIs(t, err, ErrInvalidEntry)
}

func TestValidateStoredEntry(t *testing.T) {
	valid := Entry{
		ID:             "entry-1",
		TenantID:       "tenant",
		Actor:          "agent",
		Action:         "user.delete",
		Outcome:        OutcomeSuccess,
		SignatureKeyID: "k1",
	}
	assert.NoError(t, ValidateStoredEntry("tenant", valid))
	assert.ErrorIs(t, ValidateStoredEntry("", valid), ErrInvalidEntry)
	assert.ErrorIs(t, ValidateStoredEntry("other", valid), ErrInvalidEntry)

	invalid := valid
	invalid.Metadata = map[string]any{"bad key": "value"}
	assert.ErrorIs(t, ValidateStoredEntry("tenant", invalid), ErrInvalidEntry)
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

func TestAppend_ClonesMetadataBeforeStore(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))
	metadata := map[string]any{
		"reason": "original",
		"nested": map[string]any{"key": "value"},
	}

	written, err := logger.Append(context.Background(), Entry{
		TenantID: "t",
		Actor:    "a",
		Action:   "x",
		Outcome:  OutcomeSuccess,
		Metadata: metadata,
	})
	require.NoError(t, err)

	metadata["reason"] = "mutated"
	metadata["nested"].(map[string]any)["key"] = "mutated"
	written.Metadata["reason"] = "returned"
	written.Metadata["nested"].(map[string]any)["key"] = "returned"

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, "original", got.Metadata["reason"])
	assert.Equal(t, "value", got.Metadata["nested"].(map[string]any)["key"])

	got.Metadata["reason"] = "get-mutated"
	got.Metadata["nested"].(map[string]any)["key"] = "get-mutated"
	gotAgain, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, "original", gotAgain.Metadata["reason"])
	assert.Equal(t, "value", gotAgain.Metadata["nested"].(map[string]any)["key"])
}

func TestEntryClone_DetachesMetadata(t *testing.T) {
	metadata := map[string]any{
		"nested": map[string]any{"key": "value"},
		"list":   []any{map[string]any{"item": "value"}},
	}
	cloned := Entry{Metadata: metadata}.Clone()

	metadata["nested"].(map[string]any)["key"] = "mutated"
	metadata["list"].([]any)[0].(map[string]any)["item"] = "mutated"

	assert.Equal(t, "value", cloned.Metadata["nested"].(map[string]any)["key"])
	assert.Equal(t, "value", cloned.Metadata["list"].([]any)[0].(map[string]any)["item"])
}

func TestEntryClone_DoesNotPanicOnCyclicMetadata(t *testing.T) {
	metadata := map[string]any{}
	metadata["self"] = metadata
	originalPtr := reflect.ValueOf(metadata).Pointer()

	var cloned Entry
	assert.NotPanics(t, func() {
		cloned = Entry{Metadata: metadata}.Clone()
	})
	require.NotNil(t, cloned.Metadata)
	clonedPtr := reflect.ValueOf(cloned.Metadata).Pointer()
	self, ok := cloned.Metadata["self"].(map[string]any)
	require.True(t, ok)
	assert.NotEqual(t, originalPtr, clonedPtr)
	assert.Equal(t, clonedPtr, reflect.ValueOf(self).Pointer())
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
	store.mu.Lock()
	tampered := store.entries[e.ID]
	tampered.Actor = "rogue"
	store.entries[e.ID] = tampered
	store.mu.Unlock()

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
	store.mu.Lock()
	tampered := store.entries[e.ID]
	tampered.SignatureKeyID = "rotated-out-key"
	store.entries[e.ID] = tampered
	store.mu.Unlock()

	_, err = logger.Get(context.Background(), e.ID)
	assert.ErrorIs(t, err, ErrUnknownKeyID)
}

func TestAppend_RejectsShortCustomSecret(t *testing.T) {
	logger := New(newMemStore(), stubSecrets{
		current: "secret-token",
		keys:    map[string][]byte{"secret-token": []byte("too-short")},
	})

	_, err := logger.Append(context.Background(), Entry{
		TenantID: "t",
		Actor:    "a",
		Action:   "x",
		Outcome:  OutcomeSuccess,
	})
	assert.ErrorIs(t, err, ErrSecretTooShort)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSign_RejectsEmptyCurrentKeyIDFromCustomSecrets(t *testing.T) {
	secrets := stubSecrets{
		current: "",
		keys:    map[string][]byte{"": make([]byte, 32)},
	}
	_ = New(newMemStore(), secrets)

	sig, keyID, err := SignEntry(context.Background(), Entry{
		ID:         "e1",
		TenantID:   "t",
		Actor:      "a",
		Action:     "x",
		Outcome:    OutcomeSuccess,
		OccurredAt: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
	}, secrets)
	assert.ErrorIs(t, err, ErrUnknownKeyID)
	assert.Empty(t, sig)
	assert.Empty(t, keyID)
}

func TestVerify_RejectsShortCustomSecret(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))
	e, err := logger.Append(context.Background(), Entry{
		TenantID: "t",
		Actor:    "a",
		Action:   "x",
		Outcome:  OutcomeSuccess,
	})
	require.NoError(t, err)

	weakSecrets := stubSecrets{
		current: "k1",
		keys:    map[string][]byte{"k1": []byte("too-short")},
	}
	_ = New(store, weakSecrets)

	err = VerifyEntry(context.Background(), e, weakSecrets)
	assert.ErrorIs(t, err, ErrSecretTooShort)
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
	store.mu.Lock()
	tampered := store.entries[bad.ID]
	tampered.Reason = "rewritten after the fact"
	store.entries[bad.ID] = tampered
	store.mu.Unlock()

	_, _, err = logger.List(context.Background(), Query{TenantID: "t"})
	assert.ErrorIs(t, err, ErrSignatureInvalid)
	_ = good
}

func TestList_RejectsZeroQuery(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))
	_, _, err := logger.List(context.Background(), Query{})
	assert.ErrorIs(t, err, ErrQueryTenantRequired)
}

func TestList_RejectsQueryScopeConflict(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))
	_, _, err := logger.List(context.Background(), Query{TenantID: "t", AllTenants: true})
	assert.ErrorIs(t, err, ErrQueryScopeConflict)
}

func TestQueryValidate(t *testing.T) {
	assert.ErrorIs(t, (Query{}).Validate(), ErrQueryTenantRequired)
	assert.ErrorIs(t, (Query{Actor: "a"}).Validate(), ErrQueryTenantRequired)
	assert.NoError(t, (Query{TenantID: "t"}).Validate())
	assert.NoError(t, (Query{AllTenants: true}).Validate())
	assert.ErrorIs(t, (Query{TenantID: "t", AllTenants: true}).Validate(), ErrQueryScopeConflict)
}

func TestQueryValidate_LimitBounds(t *testing.T) {
	// Reject at the Query boundary so every Store impl stays safe
	// from caller-controlled unbounded scans, regardless of how each
	// Store interprets limit <= 0 internally.
	assert.ErrorIs(t, (Query{TenantID: "t", Limit: -1}).Validate(), ErrLimitNegative)
	assert.ErrorIs(t, (Query{TenantID: "t", Limit: MaxPageLimit + 1}).Validate(), ErrLimitTooLarge)
	assert.NoError(t, (Query{TenantID: "t", Limit: 0}).Validate(),
		"limit=0 is reserved for Store-side defaulting; only negative is rejected")
	assert.NoError(t, (Query{TenantID: "t", Limit: MaxPageLimit}).Validate())
}

func TestList_AllTenantsOptIn(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))
	_, err := logger.Append(context.Background(), Entry{
		TenantID: "t1", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)
	_, err = logger.Append(context.Background(), Entry{
		TenantID: "t2", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)
	got, _, err := logger.List(context.Background(), Query{AllTenants: true})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestList_VerificationErrorDoesNotReflectEntryID(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t), WithIDFunc(func() string { return "secret-token" }))
	written, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	store.mu.Lock()
	tampered := store.entries[written.ID]
	tampered.Signature = "tampered"
	store.entries[written.ID] = tampered
	store.mu.Unlock()

	_, _, err = logger.List(context.Background(), Query{TenantID: "t"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSignatureInvalid)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestVerifyChain_DetectsDeletion(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	for i := 0; i < 3; i++ {
		_, err := logger.Append(context.Background(), Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
		})
		require.NoError(t, err)
	}
	require.NoError(t, logger.VerifyChain(context.Background(), "t"))

	// Delete the middle entry.
	store.mu.Lock()
	middleID := store.order[1]
	delete(store.entries, middleID)
	store.order = append(store.order[:1], store.order[2:]...)
	store.mu.Unlock()

	err := logger.VerifyChain(context.Background(), "t")
	assert.ErrorIs(t, err, ErrChainBroken)
}

func TestVerifyChain_ErrorDoesNotReflectTenantOrEntryID(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	for i := 0; i < 3; i++ {
		_, err := logger.Append(context.Background(), Entry{
			TenantID: "secret-token", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	store.mu.Lock()
	middleID := store.order[1]
	delete(store.entries, middleID)
	store.order = append(store.order[:1], store.order[2:]...)
	store.mu.Unlock()

	err := logger.VerifyChain(context.Background(), "secret-token")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChainBroken)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
}

func TestVerifyChain_DetectsReorder(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	for i := 0; i < 3; i++ {
		_, err := logger.Append(context.Background(), Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	// Swap Seq values on two entries — this is what a malicious DBA
	// would do to make a later action appear to have come first.
	// Without the chain we'd only catch this if signatures broke; with
	// the chain, the prev_hash of subsequent entries becomes wrong.
	store.mu.Lock()
	a := store.entries[store.order[0]]
	b := store.entries[store.order[1]]
	a.Seq, b.Seq = b.Seq, a.Seq
	store.entries[a.ID] = a
	store.entries[b.ID] = b
	store.mu.Unlock()

	err := logger.VerifyChain(context.Background(), "t")
	// Either ErrSignatureInvalid (Seq is part of the canonical form)
	// or ErrChainBroken (prev_hash mismatch) is acceptable — both
	// signal tamper to the caller.
	assert.True(t, err != nil)
}

func TestAppend_ConcurrentMonotonicSeq(t *testing.T) {
	store := newMemStore()
	logger := New(store, newTestSecrets(t))

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := logger.Append(context.Background(), Entry{
				TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
			})
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	require.NoError(t, logger.VerifyChain(context.Background(), "t"))
}

func TestStaticSecrets_ResolveReturnsCopy(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ss := NewStaticSecrets("k1", map[string][]byte{"k1": key})
	ctx := context.Background()
	got, err := ss.Resolve(ctx, "k1")
	require.NoError(t, err)
	got[0] ^= 0xff

	again, err := ss.Resolve(ctx, "k1")
	require.NoError(t, err)
	assert.Equal(t, byte('0'), again[0], "Resolve must return a defensive copy")
}

func TestStaticSecrets_NilReceiverFailsClosed(t *testing.T) {
	var ss *StaticSecrets
	id, err := ss.CurrentKeyID(context.Background())
	assert.NoError(t, err)
	assert.Empty(t, id)
	got, err := ss.Resolve(context.Background(), "k1")
	assert.ErrorIs(t, err, ErrUnknownKeyID)
	assert.Nil(t, got)
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
	secrets := newTestSecrets(t)
	_ = New(newMemStore(), secrets)
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

	sigA, _, err := SignEntry(context.Background(), a, secrets)
	require.NoError(t, err)
	sigB, _, err := SignEntry(context.Background(), b, secrets)
	require.NoError(t, err)
	assert.NotEqual(t, sigA, sigB,
		"length-prefix canonical form must distinguish entries that differ only in newline placement")
}

func TestSign_DeterministicAcrossInvocations(t *testing.T) {
	secrets := newTestSecrets(t)
	_ = New(newMemStore(), secrets)
	e := Entry{
		ID:         "e1",
		TenantID:   "t",
		Actor:      "a",
		Action:     "x",
		Outcome:    OutcomeSuccess,
		OccurredAt: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC),
		Metadata:   map[string]any{"b": 1, "a": 2, "z": map[string]any{"y": 3, "x": 4}},
	}
	s1, _, err := SignEntry(context.Background(), e, secrets)
	require.NoError(t, err)
	s2, _, err := SignEntry(context.Background(), e, secrets)
	require.NoError(t, err)
	assert.Equal(t, s1, s2)
}

func TestSign_OrderInsensitiveMetadata(t *testing.T) {
	// The signature must be the same regardless of the source map's
	// internal iteration order, because canonicalisation sorts keys.
	secrets := newTestSecrets(t)
	_ = New(newMemStore(), secrets)
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

	sa, _, err := SignEntry(context.Background(), a, secrets)
	require.NoError(t, err)
	sb, _, err := SignEntry(context.Background(), b, secrets)
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
	assert.Panics(t, func() { New(newMemStore(), newTestSecrets(t), nil) })
}

func TestSignedLogger_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		log  *signedLogger
	}{
		{"nil", nil},
		{"zero", &signedLogger{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.log.Append(ctx, Entry{TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess})
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, err = tc.log.Get(ctx, "id")
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, _, err = tc.log.List(ctx, Query{TenantID: "t"})
			assert.ErrorIs(t, err, ErrInvalidStore)

			_, _, err = SignEntry(ctx, Entry{}, nil)
			assert.ErrorIs(t, err, ErrInvalidStore)

			assert.ErrorIs(t, VerifyEntry(ctx, Entry{}, nil), ErrInvalidStore)
			assert.ErrorIs(t, tc.log.VerifyChain(ctx, "t"), ErrInvalidStore)
		})
	}
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}

func TestWithIDFunc_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithIDFunc(nil) })
}

func TestWithIDFuncE_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithIDFuncE(nil) })
}

func TestAppend_ReturnsIDGenerationError(t *testing.T) {
	want := errors.New("id source unavailable")
	logger := New(newMemStore(), newTestSecrets(t), WithIDFuncE(func() (string, error) {
		return "", want
	}))

	_, err := logger.Append(context.Background(), Entry{
		TenantID: "tenant",
		Actor:    "alice",
		Action:   "file.delete",
		Outcome:  OutcomeDenied,
	})

	assert.ErrorIs(t, err, want)
}

// TestVerifyChain_SpansKeyRotation is the regression test for the
// R2 hash-chain key-rotation finding: appending under one key, then
// rotating and appending under a different key, must still pass
// VerifyChain. Pre-fix, prev_hash was an HMAC under the current key;
// VerifyChain recomputed it under the previous entry's key, so any
// rotation between two entries produced a false ErrChainBroken.
func TestVerifyChain_SpansKeyRotation(t *testing.T) {
	old := []byte("old-key-old-key-old-key-old-key-")
	require.Len(t, old, 32)
	newKey := []byte("new-key-new-key-new-key-new-key-")
	require.Len(t, newKey, 32)

	store := newMemStore()

	v1 := NewStaticSecrets("v1", map[string][]byte{"v1": old})
	logger1 := New(store, v1)
	for i := 0; i < 2; i++ {
		_, err := logger1.Append(context.Background(), Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	v2 := NewStaticSecrets("v2", map[string][]byte{
		"v1": old,
		"v2": newKey,
	})
	logger2 := New(store, v2)
	for i := 0; i < 2; i++ {
		_, err := logger2.Append(context.Background(), Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	require.NoError(t, logger2.VerifyChain(context.Background(), "t"))
}

func TestNewStaticSecrets_PanicsOnUnknownCurrent(t *testing.T) {
	assert.Panics(t, func() {
		NewStaticSecrets("missing", map[string][]byte{"v1": make([]byte, 32)})
	})
}

func TestNewStaticSecrets_PanicsOnShortKey(t *testing.T) {
	assert.PanicsWithValue(t, "actionlog: NewStaticSecrets secret must be at least 32 bytes", func() {
		NewStaticSecrets("secret-token", map[string][]byte{"secret-token": []byte("too-short")})
	})
}

// TestNewStaticSecrets_PanicsOnEmptyCurrent regression-tests FR-050:
// pre-fix, NewStaticSecrets accepted currentKeyID="" if the keys map
// also had an "" entry. The signed entry's SignatureKeyID would then
// be "" — and Verify treats "" as ErrSignatureInvalid, so the entry
// could never verify. Empty-string IDs slip through with no warning
// from the type system.
func TestNewStaticSecrets_PanicsOnEmptyCurrent(t *testing.T) {
	assert.Panics(t, func() {
		NewStaticSecrets("", map[string][]byte{"": make([]byte, 32)})
	})
}

// TestNewStaticSecrets_PanicsOnEmptyKeyIDInMap covers the case where
// currentKeyID is non-empty but some other map entry has an empty key.
// That entry could never be selected as current, but a key-rotation
// flow that swaps in the "next" id might accidentally promote "" if
// the loader never validated.
func TestNewStaticSecrets_PanicsOnEmptyKeyIDInMap(t *testing.T) {
	assert.Panics(t, func() {
		NewStaticSecrets("v1", map[string][]byte{
			"v1": make([]byte, 32),
			"":   make([]byte, 32),
		})
	})
}

// usStore wraps a memStore but truncates OccurredAt to microsecond
// precision on the way in, mimicking a TIMESTAMPTZ-backed store (e.g.
// Postgres via pgx, which encodes sub-microsecond nanoseconds away on a
// TIMESTAMPTZ column). It is the minimal model that exercises the
// signing/persistence precision mismatch without a database.
type usStore struct {
	inner *memStore
}

func newUSStore() *usStore { return &usStore{inner: newMemStore()} }

func (s *usStore) AppendChained(ctx context.Context, tenantID string, build func(prev Entry, prevSeq int64) (Entry, error)) (Entry, error) {
	return s.inner.AppendChained(ctx, tenantID, func(prev Entry, prevSeq int64) (Entry, error) {
		e, err := build(prev, prevSeq)
		if err != nil {
			return Entry{}, err
		}
		e.OccurredAt = e.OccurredAt.Truncate(time.Microsecond)
		return e, nil
	})
}

func (s *usStore) Get(ctx context.Context, id string) (Entry, error) {
	return s.inner.Get(ctx, id)
}

func (s *usStore) List(ctx context.Context, q Query) ([]Entry, string, error) {
	return s.inner.List(ctx, q)
}

func (s *usStore) RangeByTenantSeq(ctx context.Context, tenantID string, fn func(Entry) error) error {
	return s.inner.RangeByTenantSeq(ctx, tenantID, fn)
}

// TestAppend_SurvivesMicrosecondTruncatingStore is the regression test
// for the precision mismatch: canonicalForm signs OccurredAt at
// RFC3339Nano (full ns), but TIMESTAMPTZ-backed stores persist at µs
// granularity. With a ns-precision clock, the entry read back has a
// different canonical form and Get/List/VerifyChain reject it unless
// Append truncates OccurredAt to µs before signing.
func TestAppend_SurvivesMicrosecondTruncatingStore(t *testing.T) {
	store := newUSStore()
	// A clock whose nanosecond component is non-zero modulo a
	// microsecond — exactly what time.Now() yields on Linux.
	nsClock := func() time.Time {
		return time.Date(2026, 6, 15, 12, 0, 0, 123456789, time.UTC)
	}
	logger := New(store, newTestSecrets(t), WithClock(nsClock))

	written, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
	})
	require.NoError(t, err)

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, written.OccurredAt, got.OccurredAt)

	out, _, err := logger.List(context.Background(), Query{TenantID: "t"})
	require.NoError(t, err)
	require.Len(t, out, 1)

	require.NoError(t, logger.VerifyChain(context.Background(), "t"))
}

// TestAppend_TruncatesCallerSuppliedOccurredAt verifies the truncation
// also applies when the caller supplies a ns-precision OccurredAt rather
// than relying on the clock.
func TestAppend_TruncatesCallerSuppliedOccurredAt(t *testing.T) {
	store := newUSStore()
	logger := New(store, newTestSecrets(t))

	occurred := time.Date(2026, 6, 15, 12, 0, 0, 987654321, time.UTC)
	written, err := logger.Append(context.Background(), Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: OutcomeSuccess,
		OccurredAt: occurred,
	})
	require.NoError(t, err)
	assert.Equal(t, occurred.Truncate(time.Microsecond), written.OccurredAt)

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, written.OccurredAt, got.OccurredAt)
}
