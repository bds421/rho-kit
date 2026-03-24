package gormdb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextWithTx_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()

	ctx = ContextWithTx(ctx, tx)

	got, ok := TxFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, tx, got)
}

func TestTxFromContext_Missing(t *testing.T) {
	ctx := context.Background()

	_, ok := TxFromContext(ctx)
	assert.False(t, ok)
}

func TestDBFromContext_ReturnsTxWhenPresent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()

	ctx = ContextWithTx(ctx, tx)

	got := DBFromContext(ctx, db)
	assert.Equal(t, tx, got)
}

func TestDBFromContext_ReturnsFallbackWhenMissing(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	got := DBFromContext(ctx, db)
	assert.Equal(t, db, got)
}

func TestContextRoundTrip_WrapperType(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()

	// Store tx via ContextWithTx and retrieve via TxFromContext.
	ctx = ContextWithTx(ctx, tx)
	got, ok := TxFromContext(ctx)
	require.True(t, ok)
	assert.Same(t, tx.Statement.DB, got.Statement.DB, "round-tripped *gorm.DB should wrap the same sql.DB")
}

type otherContextKey struct{}

func TestTxFromContext_DoesNotCollideWithRawGormDB(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// A plain *gorm.DB stored directly in context should not be returned
	// by TxFromContext, proving the wrapper prevents collisions.
	ctx = context.WithValue(ctx, otherContextKey{}, db)

	_, ok := TxFromContext(ctx)
	assert.False(t, ok, "TxFromContext must not return values stored under a different key")
}
