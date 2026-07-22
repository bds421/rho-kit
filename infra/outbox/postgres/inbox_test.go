package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTx struct {
	insertRows int64
	commit     bool
	rollback   bool
}

func (f *fakeTx) Begin(context.Context) (pgx.Tx, error) { return f, nil }
func (f *fakeTx) Commit(context.Context) error          { f.commit = true; return nil }
func (f *fakeTx) Rollback(context.Context) error        { f.rollback = true; return nil }
func (f *fakeTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (f *fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (f *fakeTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (f *fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (f *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "INSERT INTO inbox_entries") {
		return pgconn.NewCommandTag(""), fmt.Errorf("unexpected SQL: %s", sql)
	}
	return pgconn.NewCommandTag(fmt.Sprintf("INSERT 0 %d", f.insertRows)), nil
}
func (f *fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (f *fakeTx) QueryRow(context.Context, string, ...any) pgx.Row        { return fakeRow{} }
func (f *fakeTx) Conn() *pgx.Conn                                         { return nil }

type fakeRow struct{ err error }

func (r fakeRow) Scan(...any) error { return r.err }

type fakePool struct {
	tx         *fakeTx
	beginCalls int
	pruneRows  int64
	count      int64
}

func (f *fakePool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	f.beginCalls++
	return f.tx, nil
}
func (f *fakePool) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "DELETE FROM inbox_entries") {
		return pgconn.NewCommandTag(""), fmt.Errorf("unexpected SQL: %s", sql)
	}
	return pgconn.NewCommandTag(fmt.Sprintf("DELETE %d", f.pruneRows)), nil
}
func (f *fakePool) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.Contains(sql, "SELECT 1") {
		return oneRow{}
	}
	if !strings.Contains(sql, "SELECT count(*)") {
		return fakeRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
	}
	return countRow{value: f.count}
}

type oneRow struct{}

func (oneRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("expected one destination")
	}
	p, ok := dest[0].(*int)
	if !ok {
		return errors.New("unexpected destination")
	}
	*p = 1
	return nil
}

type countRow struct{ value int64 }

func (r countRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("expected one destination")
	}
	p, ok := dest[0].(*int64)
	if !ok {
		return errors.New("unexpected destination")
	}
	*p = r.value
	return nil
}

func TestProcess_CommitsClaimAndHandlerInOneOwnedTransaction(t *testing.T) {
	tx := &fakeTx{insertRows: 1}
	store := newInboxWithPool(&fakePool{tx: tx})
	called := false
	result, err := store.Process(context.Background(), "orders.billing", "msg-1", func(ctx context.Context) error {
		called = true
		got, ok := TxFromContext(ctx)
		if !ok || got != tx {
			t.Fatal("handler must receive the same transaction used for the inbox claim")
		}
		return nil
	})
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	assert.True(t, called)
	assert.True(t, tx.commit)
	assert.False(t, tx.rollback)
}

func TestProcess_DuplicateSkipsHandler(t *testing.T) {
	tx := &fakeTx{insertRows: 0}
	store := newInboxWithPool(&fakePool{tx: tx})
	called := false
	result, err := store.Process(context.Background(), "orders.billing", "msg-1", func(context.Context) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, result.Duplicate)
	assert.False(t, called)
	assert.False(t, tx.commit, "duplicate makes no local state change")
	assert.True(t, tx.rollback, "owned duplicate transaction is safely closed")
}

func TestProcess_HandlerFailureRollsBackClaim(t *testing.T) {
	tx := &fakeTx{insertRows: 1}
	store := newInboxWithPool(&fakePool{tx: tx})
	want := errors.New("domain rejected")
	_, err := store.Process(context.Background(), "orders.billing", "msg-1", func(context.Context) error { return want })
	require.ErrorIs(t, err, want)
	assert.False(t, tx.commit)
	assert.True(t, tx.rollback)
}

func TestProcessInTx_UsesCallerTransactionAndLeavesCommitToCaller(t *testing.T) {
	tx := &fakeTx{insertRows: 1}
	store := newInboxWithPool(&fakePool{tx: tx})
	ctx := WithTx(context.Background(), tx)
	result, err := store.ProcessInTx(ctx, "orders.billing", "msg-1", func(context.Context) error { return nil })
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	assert.False(t, tx.commit)
	assert.False(t, tx.rollback)
}

func TestProcessInTx_RejectsMissingTransaction(t *testing.T) {
	store := newInboxWithPool(&fakePool{tx: &fakeTx{insertRows: 1}})
	_, err := store.ProcessInTx(context.Background(), "orders.billing", "msg-1", func(context.Context) error { return nil })
	require.ErrorIs(t, err, ErrNoTx)
}

func TestRetentionHelpers(t *testing.T) {
	pool := &fakePool{tx: &fakeTx{}, pruneRows: 3, count: 7}
	store := newInboxWithPool(pool)
	deleted, err := store.PruneBefore(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	assert.EqualValues(t, 3, deleted)
	count, err := store.Count(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 7, count)
}

func TestProcess_RejectsUnsafeIdentifiersBeforeStartingTransaction(t *testing.T) {
	pool := &fakePool{tx: &fakeTx{insertRows: 1}}
	store := newInboxWithPool(pool)
	_, err := store.Process(context.Background(), "bad consumer", "msg-1", func(context.Context) error { return nil })
	require.ErrorIs(t, err, ErrInvalidConsumer)
	assert.Zero(t, pool.beginCalls)
}

func TestInboxHealthCheck(t *testing.T) {
	store := newInboxWithPool(&fakePool{tx: &fakeTx{}})
	assert.Equal(t, "healthy", store.HealthCheck().Check(context.Background()))
	assert.True(t, store.HealthCheck().Critical)
}
