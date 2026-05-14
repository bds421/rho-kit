package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/actionlog"
)

func testCursorSigner(t *testing.T) *actionlog.CursorSigner {
	t.Helper()
	signer, err := actionlog.NewCursorSigner([]byte("test-actionlog-cursor-key-32bytes"))
	require.NoError(t, err)
	return signer
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
		})
	}
}

func TestStore_ValidatesBeforeQueryingPool(t *testing.T) {
	ctx := context.Background()
	store := &Store{pool: &pgxpool.Pool{}, cursorSigner: testCursorSigner(t)}

	_, _, err := store.List(ctx, actionlog.Query{})
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, _, err = store.List(ctx, actionlog.Query{Actor: "a"})
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, _, err = store.List(ctx, actionlog.Query{TenantID: "tenant", AllTenants: true})
	assert.ErrorIs(t, err, actionlog.ErrQueryScopeConflict)

	err = store.RangeByTenantSeq(ctx, "", func(actionlog.Entry) error { return nil })
	assert.ErrorIs(t, err, actionlog.ErrQueryTenantRequired)

	_, err = store.AppendChained(ctx, "", func(_ actionlog.Entry, _ int64) (actionlog.Entry, error) {
		return actionlog.Entry{}, nil
	})
	assert.ErrorIs(t, err, actionlog.ErrInvalidEntry)

	_, err = store.AppendChained(ctx, "tenant", nil)
	assert.ErrorIs(t, err, actionlog.ErrInvalidEntry)
}
