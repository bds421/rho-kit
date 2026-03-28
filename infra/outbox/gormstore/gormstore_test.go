package gormstore_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/outbox"
	"github.com/bds421/rho-kit/infra/outbox/gormstore"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

// testDBCounter provides unique names for SQLite databases.
var testDBCounter atomic.Int64

func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := fmt.Sprintf("%s/outbox_test_%d.db", t.TempDir(), testDBCounter.Add(1))

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	err = db.Exec(`CREATE TABLE IF NOT EXISTS outbox_entries (
		id             TEXT PRIMARY KEY,
		topic          TEXT NOT NULL,
		routing_key    TEXT NOT NULL,
		message_id     TEXT NOT NULL,
		message_type   TEXT NOT NULL,
		payload        TEXT NOT NULL,
		headers        TEXT,
		status         TEXT NOT NULL DEFAULT 'pending',
		attempts       INTEGER NOT NULL DEFAULT 0,
		last_error     TEXT,
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		published_at   DATETIME
	)`).Error
	require.NoError(t, err)

	return db
}

func newEntry(t *testing.T) outbox.Entry {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return outbox.Entry{
		ID:          id,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   uuid.New().String(),
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
}

func TestStore_Insert(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	entry := newEntry(t)
	entry.Topic = "orders"

	err := store.Insert(ctx, entry)
	require.NoError(t, err)

	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestStore_Insert_WithTx(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	entry := newEntry(t)

	err := db.Transaction(func(tx *gorm.DB) error {
		txCtx := gormdb.ContextWithTx(ctx, tx)
		return store.Insert(txCtx, entry)
	})
	require.NoError(t, err)

	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestStore_Insert_RollbackUndoesInsert(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	entry := newEntry(t)

	// Begin a transaction, insert via context, then rollback.
	tx := db.Begin()
	require.NoError(t, tx.Error)

	txCtx := gormdb.ContextWithTx(ctx, tx)
	err := store.Insert(txCtx, entry)
	require.NoError(t, err)

	require.NoError(t, tx.Rollback().Error)

	// The entry must NOT be persisted — proving Insert used the transaction.
	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "rolled-back insert must not persist")
}

func TestStore_FetchPending(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		e := newEntry(t)
		e.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, store.Insert(ctx, e))
	}

	// Insert a published entry -- should not be fetched.
	pubEntry := newEntry(t)
	pubEntry.Status = outbox.StatusPublished
	now := time.Now().UTC()
	pubEntry.PublishedAt = &now
	require.NoError(t, store.Insert(ctx, pubEntry))

	entries, err := store.FetchPending(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, entries, 2)

	// Returned entries must reflect the claimed "processing" status.
	for _, e := range entries {
		assert.Equal(t, outbox.StatusProcessing, e.Status, "claimed entries must have processing status")
	}

	// A second fetch should not return the same entries (they are claimed).
	entries2, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, entries2, 1, "only the unclaimed pending entry should be returned")
}

func TestStore_MarkPublished(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	entry := newEntry(t)
	require.NoError(t, store.Insert(ctx, entry))

	publishedAt := time.Now().UTC()
	err := store.MarkPublished(ctx, entry.ID.String(), publishedAt)
	require.NoError(t, err)

	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestStore_MarkFailed(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	entry := newEntry(t)
	require.NoError(t, store.Insert(ctx, entry))

	err := store.MarkFailed(ctx, entry.ID.String(), "connection refused")
	require.NoError(t, err)

	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestStore_IncrementAttempts(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	entry := newEntry(t)
	entry.Status = outbox.StatusProcessing
	require.NoError(t, store.Insert(ctx, entry))

	err := store.IncrementAttempts(ctx, entry.ID.String(), "timeout")
	require.NoError(t, err)

	// Should be back to pending after increment.
	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestStore_DeletePublishedBefore(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	oldTime := time.Now().UTC().Add(-48 * time.Hour)
	e1 := newEntry(t)
	e1.Status = outbox.StatusPublished
	e1.PublishedAt = &oldTime
	e1.CreatedAt = oldTime
	require.NoError(t, store.Insert(ctx, e1))

	recentTime := time.Now().UTC()
	e2 := newEntry(t)
	e2.Status = outbox.StatusPublished
	e2.PublishedAt = &recentTime
	e2.CreatedAt = recentTime
	require.NoError(t, store.Insert(ctx, e2))

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := store.DeletePublishedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestStore_CountPending(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	for i := 0; i < 3; i++ {
		require.NoError(t, store.Insert(ctx, newEntry(t)))
	}

	count, err = store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestStore_ResetStaleProcessing(t *testing.T) {
	db := testDB(t)
	store := gormstore.New(db)
	ctx := context.Background()

	staleEntry := newEntry(t)
	staleEntry.Status = outbox.StatusProcessing
	require.NoError(t, store.Insert(ctx, staleEntry))

	// Backdate updated_at to simulate a stale processing entry.
	staleTime := time.Now().UTC().Add(-10 * time.Minute)
	db.Exec("UPDATE outbox_entries SET updated_at = ? WHERE id = ?", staleTime, staleEntry.ID.String())

	recentEntry := newEntry(t)
	recentEntry.Status = outbox.StatusProcessing
	require.NoError(t, store.Insert(ctx, recentEntry))

	reset, err := store.ResetStaleProcessing(ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reset)
}

func TestNew_NilDB_Panics(t *testing.T) {
	assert.Panics(t, func() {
		gormstore.New(nil)
	})
}
