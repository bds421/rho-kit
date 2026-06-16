package memory

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/actionlog"
)

func newTestSecrets(t *testing.T) *actionlog.StaticSecrets {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	require.Len(t, key, 32)
	return actionlog.NewStaticSecrets("k1", map[string][]byte{"k1": key})
}

// testCursorSigner returns a deterministic signer for unit tests so
// cursor encode/decode round-trips without depending on env state.
func testCursorSigner(t *testing.T) *actionlog.CursorSigner {
	t.Helper()
	signer, err := actionlog.NewCursorSigner([]byte("test-actionlog-cursor-key-32bytes"))
	require.NoError(t, err)
	return signer
}

func validStoreEntry(id, tenantID string, metadata map[string]any) actionlog.Entry {
	return actionlog.Entry{
		ID:             id,
		TenantID:       tenantID,
		Actor:          "agent",
		Action:         "user.delete",
		Outcome:        actionlog.OutcomeSuccess,
		SignatureKeyID: "k1",
		Metadata:       metadata,
	}
}

func TestRoundTrip(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	written, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t1",
		Actor:    "agent-1",
		Action:   "user.delete",
		Resource: "users/42",
		Outcome:  actionlog.OutcomeSuccess,
		Metadata: map[string]any{"reason": "GDPR"},
	})
	require.NoError(t, err)

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)
	assert.Equal(t, written, got)
}

func TestAppend_RejectsDuplicateID(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t),
		actionlog.WithIDFunc(func() string { return "fixed-id" }))

	_, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	_, err = logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	assert.ErrorIs(t, err, ErrDuplicateID)
	assert.NotContains(t, strings.ToLower(err.Error()), "fixed-id")
}

func TestTamper_DetectedByLogger(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	written, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	// Reach into the slice and mutate the row by-pass-the-API. This is
	// the test that documents the fail-closed invariant: any entry
	// rewritten in place produces ErrSignatureInvalid on Get.
	store.mu.Lock()
	idx := store.byID[written.ID]
	tampered := store.entries[idx]
	tampered.Resource = "users/forged"
	store.entries[idx] = tampered
	store.mu.Unlock()

	_, err = logger.Get(context.Background(), written.ID)
	assert.ErrorIs(t, err, actionlog.ErrSignatureInvalid)
}

func TestStore_CopiesMetadataOnPublicBoundaries(t *testing.T) {
	store := New(testCursorSigner(t))
	metadata := map[string]any{
		"reason": "original",
		"nested": map[string]any{"key": "value"},
	}

	written, err := store.AppendChained(context.Background(), "t", func(actionlog.Entry, int64) (actionlog.Entry, error) {
		return validStoreEntry("entry-1", "t", metadata), nil
	})
	require.NoError(t, err)

	metadata["reason"] = "input-mutated"
	metadata["nested"].(map[string]any)["key"] = "input-mutated"
	written.Metadata["reason"] = "return-mutated"
	written.Metadata["nested"].(map[string]any)["key"] = "return-mutated"

	got, err := store.Get(context.Background(), "entry-1")
	require.NoError(t, err)
	assert.Equal(t, "original", got.Metadata["reason"])
	assert.Equal(t, "value", got.Metadata["nested"].(map[string]any)["key"])

	got.Metadata["reason"] = "get-mutated"
	got.Metadata["nested"].(map[string]any)["key"] = "get-mutated"
	gotAgain, err := store.Get(context.Background(), "entry-1")
	require.NoError(t, err)
	assert.Equal(t, "original", gotAgain.Metadata["reason"])
	assert.Equal(t, "value", gotAgain.Metadata["nested"].(map[string]any)["key"])

	listed, _, err := store.List(context.Background(), actionlog.Query{TenantID: "t"})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	listed[0].Metadata["reason"] = "list-mutated"
	listed[0].Metadata["nested"].(map[string]any)["key"] = "list-mutated"
	gotAgain, err = store.Get(context.Background(), "entry-1")
	require.NoError(t, err)
	assert.Equal(t, "original", gotAgain.Metadata["reason"])
	assert.Equal(t, "value", gotAgain.Metadata["nested"].(map[string]any)["key"])
}

func TestStore_CopiesPreviousEntryPassedToBuild(t *testing.T) {
	store := New(testCursorSigner(t))
	_, err := store.AppendChained(context.Background(), "t", func(actionlog.Entry, int64) (actionlog.Entry, error) {
		entry := validStoreEntry("entry-1", "t", map[string]any{
			"reason": "original",
			"nested": map[string]any{"key": "value"},
		})
		entry.Seq = 1
		return entry, nil
	})
	require.NoError(t, err)

	_, err = store.AppendChained(context.Background(), "t", func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error) {
		require.Equal(t, int64(1), prevSeq)
		prev.Metadata["reason"] = "mutated-by-build"
		prev.Metadata["nested"].(map[string]any)["key"] = "mutated-by-build"
		entry := validStoreEntry("entry-2", "t", nil)
		entry.Seq = 2
		return entry, nil
	})
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "entry-1")
	require.NoError(t, err)
	assert.Equal(t, "original", got.Metadata["reason"])
	assert.Equal(t, "value", got.Metadata["nested"].(map[string]any)["key"])
}

// TestStore_LatestForTenantPicksHighestSeq pins the contract that the
// per-tenant latest lookup (backed by the latestByTenant index) returns
// the highest-Seq entry regardless of insertion order, and isolates
// tenants from one another. It would fail if the index recorded merely
// the last-appended entry instead of the max-Seq one, or leaked another
// tenant's entry.
func TestStore_LatestForTenantPicksHighestSeq(t *testing.T) {
	store := New(testCursorSigner(t))
	ctx := context.Background()

	// Append t1 entries out of Seq order: 2 then 1. The latest must be
	// the Seq=2 entry, not the most recently appended Seq=1 entry.
	for _, seq := range []int64{2, 1} {
		seq := seq
		_, err := store.AppendChained(ctx, "t1", func(actionlog.Entry, int64) (actionlog.Entry, error) {
			e := validStoreEntry("t1-seq-"+itoa(seq), "t1", nil)
			e.Seq = seq
			return e, nil
		})
		require.NoError(t, err)
	}

	// A separate tenant with its own chain must not influence t1.
	_, err := store.AppendChained(ctx, "t2", func(actionlog.Entry, int64) (actionlog.Entry, error) {
		e := validStoreEntry("t2-seq-9", "t2", nil)
		e.Seq = 9
		return e, nil
	})
	require.NoError(t, err)

	var gotPrev actionlog.Entry
	var gotPrevSeq int64
	_, err = store.AppendChained(ctx, "t1", func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error) {
		gotPrev, gotPrevSeq = prev, prevSeq
		e := validStoreEntry("t1-seq-3", "t1", nil)
		e.Seq = 3
		return e, nil
	})
	require.NoError(t, err)

	assert.Equal(t, int64(2), gotPrevSeq, "latest prevSeq must be the highest Seq, not the last appended")
	assert.Equal(t, "t1-seq-2", gotPrev.ID)
	assert.Equal(t, "t1", gotPrev.TenantID, "must not leak another tenant's entry")
}

// TestStore_LatestForTenantNoPositiveSeq pins the edge case the original
// full-scan honoured: when a tenant's only entries have Seq <= 0, the
// latest lookup yields the zero Entry and prevSeq 0 (bestSeq started at
// 0 with a strict >). The index-backed version must match so chaining
// semantics are unchanged.
func TestStore_LatestForTenantNoPositiveSeq(t *testing.T) {
	store := New(testCursorSigner(t))
	ctx := context.Background()

	_, err := store.AppendChained(ctx, "t", func(actionlog.Entry, int64) (actionlog.Entry, error) {
		return validStoreEntry("entry-seq0", "t", nil), nil // Seq defaults to 0
	})
	require.NoError(t, err)

	var gotPrev actionlog.Entry
	var gotPrevSeq int64
	_, err = store.AppendChained(ctx, "t", func(prev actionlog.Entry, prevSeq int64) (actionlog.Entry, error) {
		gotPrev, gotPrevSeq = prev, prevSeq
		e := validStoreEntry("entry-2", "t", nil)
		e.Seq = 1
		return e, nil
	})
	require.NoError(t, err)

	assert.Equal(t, int64(0), gotPrevSeq)
	assert.Equal(t, actionlog.Entry{}, gotPrev, "Seq<=0-only tenant must yield the zero entry")
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestStore_RejectsInvalidBuiltEntryBeforeClone(t *testing.T) {
	store := New(testCursorSigner(t))
	cyclic := map[string]any{}
	cyclic["self"] = cyclic

	assert.NotPanics(t, func() {
		_, err := store.AppendChained(context.Background(), "t", func(actionlog.Entry, int64) (actionlog.Entry, error) {
			return validStoreEntry("entry-1", "t", cyclic), nil
		})
		assert.ErrorIs(t, err, actionlog.ErrInvalidEntry)
	})

	_, err := store.Get(context.Background(), "entry-1")
	assert.ErrorIs(t, err, actionlog.ErrNotFound)
}

func TestList_FiltersAndOrders(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	now := time.Now().UTC()
	entries := []actionlog.Entry{
		{TenantID: "t1", Actor: "a", Action: "user.create", Outcome: actionlog.OutcomeSuccess, OccurredAt: now.Add(-3 * time.Hour)},
		{TenantID: "t1", Actor: "b", Action: "user.delete", Outcome: actionlog.OutcomeFailure, OccurredAt: now.Add(-2 * time.Hour)},
		{TenantID: "t2", Actor: "a", Action: "user.create", Outcome: actionlog.OutcomeSuccess, OccurredAt: now.Add(-1 * time.Hour)},
		{TenantID: "t1", Actor: "a", Action: "user.create", Outcome: actionlog.OutcomeDenied, OccurredAt: now},
	}
	for _, e := range entries {
		_, err := logger.Append(context.Background(), e)
		require.NoError(t, err)
	}

	// Tenant filter.
	t1, _, err := logger.List(context.Background(), actionlog.Query{TenantID: "t1"})
	require.NoError(t, err)
	assert.Len(t, t1, 3)
	// Newest-first ordering.
	for i := 1; i < len(t1); i++ {
		assert.True(t, t1[i-1].OccurredAt.After(t1[i].OccurredAt) || t1[i-1].OccurredAt.Equal(t1[i].OccurredAt))
	}

	// Actor filter (cross-tenant, so opt in).
	actorA, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Actor: "a"})
	require.NoError(t, err)
	assert.Len(t, actorA, 3)

	// Action filter (cross-tenant, so opt in).
	creates, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Action: "user.create"})
	require.NoError(t, err)
	assert.Len(t, creates, 3)

	// Time filter (cross-tenant, so opt in).
	recent, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Since: now.Add(-90 * time.Minute)})
	require.NoError(t, err)
	assert.Len(t, recent, 2)

	// Limit.
	limited, _, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestStore_ListRejectsZeroQuery(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))
	_, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t1", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)
	_, err = logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t2", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	_, _, err = store.List(context.Background(), actionlog.Query{})
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, _, err = store.List(context.Background(), actionlog.Query{Actor: "a"})
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, _, err = store.List(context.Background(), actionlog.Query{TenantID: "t1", AllTenants: true})
	assert.ErrorIs(t, err, actionlog.ErrQueryScopeConflict)

	all, _, err := store.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestStore_RangeByTenantSeqRejectsEmptyTenant(t *testing.T) {
	store := New(testCursorSigner(t))
	err := store.RangeByTenantSeq(context.Background(), "", func(actionlog.Entry) error { return nil })
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)
}

func TestStore_RangeByTenantSeqStreamsInOrder(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	for i := 0; i < 3; i++ {
		_, err := logger.Append(context.Background(), actionlog.Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	var got []int64
	err := store.RangeByTenantSeq(context.Background(), "t", func(e actionlog.Entry) error {
		got = append(got, e.Seq)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, got)
}

func TestList_DefaultLimitApplied(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	for i := 0; i < defaultLimit+10; i++ {
		_, err := logger.Append(context.Background(), actionlog.Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	all, next, err := logger.List(context.Background(), actionlog.Query{TenantID: "t"})
	require.NoError(t, err)
	assert.Len(t, all, defaultLimit)
	assert.NotEmpty(t, next, "more rows past the page must produce a cursor")
}

// TestList_CursorPaginatesAllRows asserts the invariant that drove this
// cursor work: every appended entry is reachable by following the next
// cursor. Previously Logger.List capped output at Limit silently, so a
// tenant with >Limit entries silently lost the tail.
func TestList_CursorPaginatesAllRows(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	const total = 25
	for i := 0; i < total; i++ {
		_, err := logger.Append(context.Background(), actionlog.Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	seen := make(map[string]struct{}, total)
	cursor := ""
	pages := 0
	for {
		entries, next, err := logger.List(context.Background(), actionlog.Query{
			TenantID: "t", Limit: 7, Cursor: cursor,
		})
		require.NoError(t, err)
		for _, e := range entries {
			if _, dup := seen[e.ID]; dup {
				t.Fatalf("cursor produced a duplicate id %q", e.ID)
			}
			seen[e.ID] = struct{}{}
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
		require.LessOrEqual(t, pages, 10, "pagination did not converge")
	}
	assert.Len(t, seen, total)
}

func TestList_RejectsMalformedCursor(t *testing.T) {
	store := New(testCursorSigner(t))
	_, _, err := store.List(context.Background(), actionlog.Query{
		TenantID: "t", Cursor: "not-a-valid-cursor!!!",
	})
	assert.ErrorIs(t, err, actionlog.ErrInvalidCursor)
}

func TestStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	build := func(_ actionlog.Entry, _ int64) (actionlog.Entry, error) {
		return actionlog.Entry{ID: "id", TenantID: "t"}, nil
	}
	cases := []struct {
		name  string
		store *Store
	}{
		{"nil", nil},
		{"zero", &Store{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.store.AppendChained(ctx, "t", build)
			assert.ErrorIs(t, err, actionlog.ErrInvalidStore)

			_, err = tc.store.Get(ctx, "id")
			assert.ErrorIs(t, err, actionlog.ErrInvalidStore)

			_, _, err = tc.store.List(ctx, actionlog.Query{TenantID: "t"})
			assert.ErrorIs(t, err, actionlog.ErrInvalidStore)

			err = tc.store.RangeByTenantSeq(ctx, "t", func(actionlog.Entry) error { return nil })
			assert.ErrorIs(t, err, actionlog.ErrInvalidStore)

			assert.NotPanics(t, func() { tc.store.PruneTenants() })
		})
	}
}

// TestNew_PanicsOnNilCursorSigner pins the cursor-signer-required
// contract: a nil signer at construction would let clients forge List
// cursors, so [New] must fail-fast with a panic at startup rather than
// returning a Store that misbehaves at the first paginated read.
func TestNew_PanicsOnNilCursorSigner(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

// TestList_RejectsForgedCursor pins the M-006 finding: a client cannot
// construct a cursor that the store will accept. Either the signature
// won't verify (different key) or the payload bytes don't decode
// (random base64). In every case, errors.Is(err, ErrInvalidCursor)
// must hold so HTTP handlers can map cleanly to 400 Bad Request.
func TestList_RejectsForgedCursor(t *testing.T) {
	store := New(testCursorSigner(t))
	logger := actionlog.New(store, newTestSecrets(t))

	_, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t1",
		Actor:    "agent",
		Action:   "user.delete",
		Outcome:  actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	// Forged with a different signing key.
	otherSigner, err := actionlog.NewCursorSigner([]byte("attacker-cursor-key-32bytes-padd"))
	require.NoError(t, err)
	forged := otherSigner.Encode(time.Now().UTC(), "fake-id")

	_, _, err = store.List(context.Background(), actionlog.Query{
		TenantID: "t1",
		Cursor:   forged,
	})
	require.ErrorIs(t, err, actionlog.ErrInvalidCursor)

	// Stripped (unsigned base64 of the legacy transparent encoding):
	// still rejected because no "." separator and no signature.
	_, _, err = store.List(context.Background(), actionlog.Query{
		TenantID: "t1",
		Cursor:   "aGVsbG8td29ybGQ", // arbitrary base64 without "."
	})
	require.ErrorIs(t, err, actionlog.ErrInvalidCursor)
}
