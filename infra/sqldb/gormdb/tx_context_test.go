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
