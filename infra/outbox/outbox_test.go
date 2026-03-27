package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/outbox"
)

// testDBCounter provides unique names for SQLite databases.
var testDBCounter atomic.Int64

func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Use a temp file per test for isolation. File-based SQLite avoids
	// shared-cache lifetime issues with in-memory databases.
	dbPath := fmt.Sprintf("%s/outbox_test_%d.db", t.TempDir(), testDBCounter.Add(1))

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	// Create table manually since SQLite doesn't support UUID type.
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
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		published_at   DATETIME,
		last_error     TEXT
	)`).Error
	require.NoError(t, err)

	return db
}

func TestWriter_Write(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()

	payload, err := json.Marshal(map[string]string{"key": "value"})
	require.NoError(t, err)

	params := outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     payload,
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, params)
	})
	require.NoError(t, err)

	var entries []outbox.Entry
	require.NoError(t, db.Find(&entries).Error)
	assert.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, "orders", entry.Topic)
	assert.Equal(t, "order.created", entry.RoutingKey)
	assert.Equal(t, "msg-1", entry.MessageID)
	assert.Equal(t, "order.created", entry.MessageType)
	assert.Equal(t, outbox.StatusPending, entry.Status)
	assert.Equal(t, 0, entry.Attempts)
	assert.Nil(t, entry.PublishedAt)
}

func TestWriter_Write_EmptyTopic(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, outbox.WriteParams{
			Topic:       "",
			RoutingKey:  "order.created",
			MessageID:   "msg-1",
			MessageType: "order.created",
			Payload:     []byte(`{}`),
		})
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic must not be empty")
}

func TestWriter_Write_EmptyRoutingKey(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, outbox.WriteParams{
			Topic:       "orders",
			RoutingKey:  "",
			MessageID:   "msg-1",
			MessageType: "order.created",
			Payload:     []byte(`{}`),
		})
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "routing key must not be empty")
}

func TestWriter_Write_PreservesHeaders(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, outbox.WriteParams{
			Topic:       "orders",
			RoutingKey:  "order.created",
			MessageID:   "msg-1",
			MessageType: "order.created",
			Payload:     []byte(`{}`),
			Headers:     map[string]string{"X-Correlation-Id": "abc-123"},
		})
	})
	require.NoError(t, err)

	var entry outbox.Entry
	require.NoError(t, db.First(&entry).Error)

	headers, err := entry.HeadersMap()
	require.NoError(t, err)
	assert.Equal(t, "abc-123", headers["X-Correlation-Id"])
}

func TestEntry_HeadersMap(t *testing.T) {
	headers, _ := json.Marshal(map[string]string{"X-Request-Id": "req-1"})

	entry := outbox.Entry{
		Headers: headers,
	}

	got, err := entry.HeadersMap()
	require.NoError(t, err)
	assert.Equal(t, "req-1", got["X-Request-Id"])
}

func TestEntry_HeadersMap_NilHeaders(t *testing.T) {
	entry := outbox.Entry{}

	got, err := entry.HeadersMap()
	require.NoError(t, err)
	assert.Nil(t, got)
}
