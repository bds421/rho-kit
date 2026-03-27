package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/outbox"
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
		exchange       TEXT NOT NULL,
		routing_key    TEXT NOT NULL,
		message_id     TEXT NOT NULL,
		message_type   TEXT NOT NULL,
		payload        TEXT NOT NULL,
		headers        TEXT,
		schema_version INTEGER NOT NULL DEFAULT 0,
		status         TEXT NOT NULL DEFAULT 'pending',
		attempts       INTEGER NOT NULL DEFAULT 0,
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		published_at   DATETIME,
		last_error     TEXT
	)`).Error
	require.NoError(t, err)

	return db
}

func testMessage(t *testing.T) messaging.Message {
	t.Helper()
	msg, err := messaging.NewMessage("test.event", map[string]string{"key": "value"})
	require.NoError(t, err)
	return msg
}

func TestWriter_Write(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()
	msg := testMessage(t)

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, "orders", "order.created", msg)
	})
	require.NoError(t, err)

	var entries []outbox.Entry
	require.NoError(t, db.Find(&entries).Error)
	assert.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, "orders", entry.Exchange)
	assert.Equal(t, "order.created", entry.RoutingKey)
	assert.Equal(t, msg.ID, entry.MessageID)
	assert.Equal(t, msg.Type, entry.MessageType)
	assert.Equal(t, outbox.StatusPending, entry.Status)
	assert.Equal(t, 0, entry.Attempts)
	assert.Nil(t, entry.PublishedAt)
}

func TestWriter_Write_EmptyExchange(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()
	msg := testMessage(t)

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, "", "order.created", msg)
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exchange must not be empty")
}

func TestWriter_Write_EmptyRoutingKey(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()
	msg := testMessage(t)

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, "orders", "", msg)
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "routing key must not be empty")
}

func TestWriter_Write_PreservesHeaders(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	ctx := context.Background()
	msg := testMessage(t).WithHeader("X-Correlation-Id", "abc-123")

	err := db.Transaction(func(tx *gorm.DB) error {
		return writer.Write(ctx, tx, "orders", "order.created", msg)
	})
	require.NoError(t, err)

	var entry outbox.Entry
	require.NoError(t, db.First(&entry).Error)

	var headers map[string]string
	require.NoError(t, json.Unmarshal(entry.Headers, &headers))
	assert.Equal(t, "abc-123", headers["X-Correlation-Id"])
}

func TestEntry_ToMessage(t *testing.T) {
	headers, _ := json.Marshal(map[string]string{"X-Request-Id": "req-1"})
	payload, _ := json.Marshal(map[string]string{"order": "123"})

	now := time.Now().UTC()
	entry := outbox.Entry{
		MessageID:     "msg-1",
		MessageType:   "order.created",
		Payload:       payload,
		Headers:       headers,
		SchemaVersion: 2,
		CreatedAt:     now,
	}

	msg, err := entry.ToMessage()
	require.NoError(t, err)
	assert.Equal(t, "msg-1", msg.ID)
	assert.Equal(t, "order.created", msg.Type)
	assert.Equal(t, uint(2), msg.SchemaVersion)
	assert.Equal(t, "req-1", msg.Headers["X-Request-Id"])
}

func TestEntry_ToMessage_NilHeaders(t *testing.T) {
	payload, _ := json.Marshal("test")
	entry := outbox.Entry{
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     payload,
	}

	msg, err := entry.ToMessage()
	require.NoError(t, err)
	assert.Equal(t, "msg-1", msg.ID)
	assert.Nil(t, msg.Headers)
}
