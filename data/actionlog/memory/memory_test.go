package memory

import (
	"context"
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
	store := New()
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
	store := New()
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
	store := New()
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
	store := New()
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

	listed, err := store.List(context.Background(), actionlog.Query{TenantID: "t"})
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
	store := New()
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

func TestStore_RejectsInvalidBuiltEntryBeforeClone(t *testing.T) {
	store := New()
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
	store := New()
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
	t1, err := logger.List(context.Background(), actionlog.Query{TenantID: "t1"})
	require.NoError(t, err)
	assert.Len(t, t1, 3)
	// Newest-first ordering.
	for i := 1; i < len(t1); i++ {
		assert.True(t, t1[i-1].OccurredAt.After(t1[i].OccurredAt) || t1[i-1].OccurredAt.Equal(t1[i].OccurredAt))
	}

	// Actor filter (cross-tenant, so opt in).
	actorA, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Actor: "a"})
	require.NoError(t, err)
	assert.Len(t, actorA, 3)

	// Action filter (cross-tenant, so opt in).
	creates, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Action: "user.create"})
	require.NoError(t, err)
	assert.Len(t, creates, 3)

	// Time filter (cross-tenant, so opt in).
	recent, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Since: now.Add(-90 * time.Minute)})
	require.NoError(t, err)
	assert.Len(t, recent, 2)

	// Limit.
	limited, err := logger.List(context.Background(), actionlog.Query{AllTenants: true, Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestStore_ListRejectsZeroQuery(t *testing.T) {
	store := New()
	logger := actionlog.New(store, newTestSecrets(t))
	_, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t1", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)
	_, err = logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t2", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	_, err = store.List(context.Background(), actionlog.Query{})
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, err = store.List(context.Background(), actionlog.Query{Actor: "a"})
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, err = store.List(context.Background(), actionlog.Query{TenantID: "t1", AllTenants: true})
	assert.ErrorIs(t, err, actionlog.ErrQueryScopeConflict)

	all, err := store.List(context.Background(), actionlog.Query{AllTenants: true})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestStore_ListByTenantSeqRejectsEmptyTenant(t *testing.T) {
	store := New()
	_, err := store.ListByTenantSeq(context.Background(), "")
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)
}

func TestList_DefaultLimitApplied(t *testing.T) {
	store := New()
	logger := actionlog.New(store, newTestSecrets(t))

	for i := 0; i < defaultLimit+10; i++ {
		_, err := logger.Append(context.Background(), actionlog.Entry{
			TenantID: "t", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
		})
		require.NoError(t, err)
	}

	all, err := logger.List(context.Background(), actionlog.Query{TenantID: "t"})
	require.NoError(t, err)
	assert.Len(t, all, defaultLimit)
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

			_, err = tc.store.List(ctx, actionlog.Query{TenantID: "t"})
			assert.ErrorIs(t, err, actionlog.ErrInvalidStore)

			_, err = tc.store.ListByTenantSeq(ctx, "t")
			assert.ErrorIs(t, err, actionlog.ErrInvalidStore)

			assert.NotPanics(t, func() { tc.store.PruneTenants() })
		})
	}
}
