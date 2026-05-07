package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/actionlog"
)

func newTestSecrets(t *testing.T) *actionlog.StaticSecrets {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	require.Len(t, key, 32)
	return actionlog.NewStaticSecrets("k1", map[string][]byte{"k1": key})
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
	store.entries[idx].Resource = "users/forged"
	store.mu.Unlock()

	_, err = logger.Get(context.Background(), written.ID)
	assert.ErrorIs(t, err, actionlog.ErrSignatureInvalid)
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

	// Actor filter.
	actorA, err := logger.List(context.Background(), actionlog.Query{Actor: "a"})
	require.NoError(t, err)
	assert.Len(t, actorA, 3)

	// Action filter.
	creates, err := logger.List(context.Background(), actionlog.Query{Action: "user.create"})
	require.NoError(t, err)
	assert.Len(t, creates, 3)

	// Time filter.
	recent, err := logger.List(context.Background(), actionlog.Query{Since: now.Add(-90 * time.Minute)})
	require.NoError(t, err)
	assert.Len(t, recent, 2)

	// Limit.
	limited, err := logger.List(context.Background(), actionlog.Query{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
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

	all, err := logger.List(context.Background(), actionlog.Query{})
	require.NoError(t, err)
	assert.Len(t, all, defaultLimit)
}
