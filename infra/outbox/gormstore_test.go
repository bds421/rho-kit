package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/outbox"
)

func TestGormStore_Insert(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	id, err := uuid.NewV7()
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:          id,
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     []byte(`{"order_id":"123"}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}

	err = store.Insert(ctx, db, entry)
	require.NoError(t, err)

	var found outbox.Entry
	require.NoError(t, db.First(&found).Error)
	assert.Equal(t, entry.ID, found.ID)
	assert.Equal(t, "orders", found.Topic)
}

func TestGormStore_FetchPending(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	// Insert multiple pending entries.
	for i := 0; i < 3; i++ {
		id, _ := uuid.NewV7()
		entry := outbox.Entry{
			ID:          id,
			Topic:       "test",
			RoutingKey:  "test.key",
			MessageID:   uuid.New().String(),
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusPending,
			CreatedAt:   time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
		}
		require.NoError(t, store.Insert(ctx, db, entry))
	}

	// Insert a published entry -- should not be fetched.
	pubID, _ := uuid.NewV7()
	now := time.Now().UTC()
	pubEntry := outbox.Entry{
		ID:          pubID,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   uuid.New().String(),
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPublished,
		PublishedAt: &now,
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.Insert(ctx, db, pubEntry))

	entries, err := store.FetchPending(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, entries, 2)

	// Verify entries are now in "processing" status in the database.
	for _, e := range entries {
		var found outbox.Entry
		require.NoError(t, db.First(&found, "id = ?", e.ID).Error)
		assert.Equal(t, outbox.StatusProcessing, found.Status)
	}

	// A second fetch should not return the same entries (they are claimed).
	entries2, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, entries2, 1, "only the unclaimed pending entry should be returned")
}

func TestGormStore_MarkPublished(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	id, _ := uuid.NewV7()
	entry := outbox.Entry{
		ID:          id,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.Insert(ctx, db, entry))

	publishedAt := time.Now().UTC()
	err := store.MarkPublished(ctx, id.String(), publishedAt)
	require.NoError(t, err)

	var found outbox.Entry
	require.NoError(t, db.First(&found, "id = ?", id).Error)
	assert.Equal(t, outbox.StatusPublished, found.Status)
	assert.NotNil(t, found.PublishedAt)
}

func TestGormStore_MarkFailed(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	id, _ := uuid.NewV7()
	entry := outbox.Entry{
		ID:          id,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.Insert(ctx, db, entry))

	err := store.MarkFailed(ctx, id.String(), "connection refused")
	require.NoError(t, err)

	var found outbox.Entry
	require.NoError(t, db.First(&found, "id = ?", id).Error)
	assert.Equal(t, outbox.StatusFailed, found.Status)
	require.NotNil(t, found.LastError)
	assert.Equal(t, "connection refused", *found.LastError)
}

func TestGormStore_IncrementAttempts(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	id, _ := uuid.NewV7()
	entry := outbox.Entry{
		ID:          id,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusProcessing,
		Attempts:    0,
		CreatedAt:   time.Now().UTC(),
	}
	require.NoError(t, store.Insert(ctx, db, entry))

	err := store.IncrementAttempts(ctx, id.String(), "timeout")
	require.NoError(t, err)

	var found outbox.Entry
	require.NoError(t, db.First(&found, "id = ?", id).Error)
	assert.Equal(t, 1, found.Attempts)
	assert.Equal(t, outbox.StatusPending, found.Status, "status should be reset to pending for retry")
	require.NotNil(t, found.LastError)
	assert.Equal(t, "timeout", *found.LastError)
}

func TestGormStore_DeletePublishedBefore(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	oldTime := time.Now().UTC().Add(-48 * time.Hour)

	id1, _ := uuid.NewV7()
	entry1 := outbox.Entry{
		ID:          id1,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPublished,
		PublishedAt: &oldTime,
		CreatedAt:   oldTime,
	}
	require.NoError(t, store.Insert(ctx, db, entry1))

	recentTime := time.Now().UTC()
	id2, _ := uuid.NewV7()
	entry2 := outbox.Entry{
		ID:          id2,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-2",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPublished,
		PublishedAt: &recentTime,
		CreatedAt:   recentTime,
	}
	require.NoError(t, store.Insert(ctx, db, entry2))

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := store.DeletePublishedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	var remaining []outbox.Entry
	require.NoError(t, db.Find(&remaining).Error)
	assert.Len(t, remaining, 1)
	assert.Equal(t, id2, remaining[0].ID)
}

func TestGormStore_CountPending(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	count, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	for i := 0; i < 3; i++ {
		id, _ := uuid.NewV7()
		entry := outbox.Entry{
			ID:          id,
			Topic:       "test",
			RoutingKey:  "test.key",
			MessageID:   uuid.New().String(),
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusPending,
			CreatedAt:   time.Now().UTC(),
		}
		require.NoError(t, store.Insert(ctx, db, entry))
	}

	count, err = store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestGormStore_ResetStaleProcessing(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	ctx := context.Background()

	staleTime := time.Now().UTC().Add(-10 * time.Minute)
	recentTime := time.Now().UTC()

	// Insert a stale processing entry.
	staleID, _ := uuid.NewV7()
	staleEntry := outbox.Entry{
		ID:          staleID,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-stale",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusProcessing,
		CreatedAt:   staleTime,
	}
	require.NoError(t, store.Insert(ctx, db, staleEntry))

	// Insert a recent processing entry (should not be reset).
	recentID, _ := uuid.NewV7()
	recentEntry := outbox.Entry{
		ID:          recentID,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-recent",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusProcessing,
		CreatedAt:   recentTime,
	}
	require.NoError(t, store.Insert(ctx, db, recentEntry))

	reset, err := store.ResetStaleProcessing(ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reset)

	var staleFound outbox.Entry
	require.NoError(t, db.First(&staleFound, "id = ?", staleID).Error)
	assert.Equal(t, outbox.StatusPending, staleFound.Status)

	var recentFound outbox.Entry
	require.NoError(t, db.First(&recentFound, "id = ?", recentID).Error)
	assert.Equal(t, outbox.StatusProcessing, recentFound.Status)
}

func TestNewGormStore_NilDB_Panics(t *testing.T) {
	assert.Panics(t, func() {
		outbox.NewGormStore(nil)
	})
}
