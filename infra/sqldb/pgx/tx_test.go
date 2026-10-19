package pgx

import (
	"context"
	"errors"
	"testing"

	jpgx "github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTx records commit/rollback; unused pgx.Tx methods panic via the nil
// embedded interface.
type fakeTx struct {
	jpgx.Tx
	commits, rollbacks int
	commitErr          error
	closed             bool
}

func (f *fakeTx) Commit(context.Context) error {
	f.commits++
	f.closed = true
	return f.commitErr
}

func (f *fakeTx) Rollback(context.Context) error {
	if f.closed {
		return jpgx.ErrTxClosed
	}
	f.rollbacks++
	f.closed = true
	return nil
}

func beginWith(tx *fakeTx) func(context.Context) (jpgx.Tx, error) {
	return func(context.Context) (jpgx.Tx, error) { return tx, nil }
}

func TestTxContext(t *testing.T) {
	_, ok := TxFromContext(context.Background())
	assert.False(t, ok)
	_, ok = TxFromContext(nil) //nolint:staticcheck // nil-context guard
	assert.False(t, ok)
	ctx := context.Background()
	assert.Equal(t, ctx, WithTx(ctx, nil), "nil tx leaves ctx unchanged")
	tx := &fakeTx{}
	got, ok := TxFromContext(WithTx(ctx, tx))
	require.True(t, ok)
	assert.Same(t, tx, got)
}

func TestQuerier_PrefersContextTx(t *testing.T) {
	tx := &fakeTx{}
	p := &Pool{}
	assert.Same(t, tx, p.Querier(WithTx(context.Background(), tx)).(*fakeTx))
	_, isTx := p.Querier(context.Background()).(*fakeTx)
	assert.False(t, isTx, "without a tx the pool is used")
}

func TestRunInTx_CommitsOnSuccess(t *testing.T) {
	tx := &fakeTx{}
	err := runInTx(context.Background(), beginWith(tx), func(ctx context.Context) error {
		got, ok := TxFromContext(ctx)
		require.True(t, ok)
		assert.Same(t, tx, got)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.commits)
	assert.Zero(t, tx.rollbacks)
}

func TestRunInTx_RollsBackOnError(t *testing.T) {
	tx := &fakeTx{}
	boom := errors.New("boom")
	err := runInTx(context.Background(), beginWith(tx), func(context.Context) error { return boom })
	require.ErrorIs(t, err, boom)
	assert.Zero(t, tx.commits)
	assert.Equal(t, 1, tx.rollbacks)
}

func TestRunInTx_RollsBackAndRepanics(t *testing.T) {
	tx := &fakeTx{}
	assert.PanicsWithValue(t, "kaboom", func() {
		_ = runInTx(context.Background(), beginWith(tx), func(context.Context) error { panic("kaboom") })
	})
	assert.Zero(t, tx.commits)
	assert.Equal(t, 1, tx.rollbacks)
}

func TestRunInTx_CommitAndBeginErrors(t *testing.T) {
	tx := &fakeTx{commitErr: errors.New("secret driver detail")}
	err := runInTx(context.Background(), beginWith(tx), func(context.Context) error { return nil })
	require.Error(t, err)
	assert.ErrorIs(t, err, tx.commitErr)
	assert.NotContains(t, err.Error(), "secret")

	called := false
	err = runInTx(context.Background(), func(context.Context) (jpgx.Tx, error) {
		return nil, errors.New("dial")
	}, func(context.Context) error { called = true; return nil })
	require.Error(t, err)
	assert.False(t, called)
}

func TestInTx_JoinsExistingTx(t *testing.T) {
	outer := &fakeTx{}
	ctx := WithTx(context.Background(), outer)
	p := &Pool{} // no pool: joining must not begin a new transaction
	err := p.InTx(ctx, func(inner context.Context) error {
		got, ok := TxFromContext(inner)
		require.True(t, ok)
		assert.Same(t, outer, got)
		return nil
	})
	require.NoError(t, err)
	assert.Zero(t, outer.commits, "the outer owner commits")
	assert.Zero(t, outer.rollbacks)

	boom := errors.New("boom")
	require.ErrorIs(t, p.InTx(ctx, func(context.Context) error { return boom }), boom)
	assert.Zero(t, outer.rollbacks, "a joined failure leaves rollback to the owner")
}

func TestInTx_RejectsInvalidUse(t *testing.T) {
	require.Error(t, (&Pool{}).InTx(context.Background(), nil))
	require.Error(t, (&Pool{}).InTx(context.Background(), func(context.Context) error { return nil }))
}
