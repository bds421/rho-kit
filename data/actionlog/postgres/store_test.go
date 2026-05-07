package postgres

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/actionlog"
	"github.com/bds421/rho-kit/infra/sqldb/memdb"
)

func setupStore(t *testing.T) *Store {
	t.Helper()
	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	db := memdb.New(t, sub)
	return New(db)
}

func newTestSecrets(t *testing.T) *actionlog.StaticSecrets {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	require.Len(t, key, 32)
	return actionlog.NewStaticSecrets("k1", map[string][]byte{"k1": key})
}

func TestAppendAndGet_RoundTrip(t *testing.T) {
	store := setupStore(t)
	logger := actionlog.New(store, newTestSecrets(t))

	written, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t1",
		Actor:    "agent-1",
		Action:   "user.delete",
		Resource: "users/42",
		Outcome:  actionlog.OutcomeSuccess,
		Reason:   "",
		Metadata: map[string]any{"requested_by": "ops@example.com", "count": float64(3)},
	})
	require.NoError(t, err)

	got, err := logger.Get(context.Background(), written.ID)
	require.NoError(t, err)

	// Round-trip equality is the property the canonical signing
	// depends on. JSON unmarshal turns numbers into float64; keep the
	// expected the same shape on the way in.
	assert.Equal(t, written, got)
}

func TestList_Filters(t *testing.T) {
	store := setupStore(t)
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

	t1, err := logger.List(context.Background(), actionlog.Query{TenantID: "t1"})
	require.NoError(t, err)
	assert.Len(t, t1, 3)

	createsByA, err := logger.List(context.Background(), actionlog.Query{
		Actor:  "a",
		Action: "user.create",
	})
	require.NoError(t, err)
	assert.Len(t, createsByA, 3)

	recent, err := logger.List(context.Background(), actionlog.Query{Since: now.Add(-90 * time.Minute)})
	require.NoError(t, err)
	assert.Len(t, recent, 2)

	limited, err := logger.List(context.Background(), actionlog.Query{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestGet_DetectsTamper(t *testing.T) {
	// Bypass the store and rewrite a row directly with raw SQL — the
	// scenario is a DBA editing the audit table. The Logger must
	// surface ErrSignatureInvalid on the next read.
	store := setupStore(t)
	logger := actionlog.New(store, newTestSecrets(t))

	written, err := logger.Append(context.Background(), actionlog.Entry{
		TenantID: "t1", Actor: "a", Action: "x", Outcome: actionlog.OutcomeSuccess,
	})
	require.NoError(t, err)

	require.NoError(t, store.db.Exec("UPDATE action_log_entries SET actor = ? WHERE id = ?", "rogue", written.ID).Error)

	_, err = logger.Get(context.Background(), written.ID)
	assert.ErrorIs(t, err, actionlog.ErrSignatureInvalid)
}

func TestGet_NotFound(t *testing.T) {
	store := setupStore(t)
	_, err := store.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, actionlog.ErrNotFound)
}

func TestNew_PanicsOnNilDB(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}
