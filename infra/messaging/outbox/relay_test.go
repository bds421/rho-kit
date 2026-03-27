package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/outbox"
)

// fakePublisher records published messages and can simulate errors.
type fakePublisher struct {
	mu        sync.Mutex
	published []publishedMsg
	err       error
}

type publishedMsg struct {
	exchange   string
	routingKey string
	msg        messaging.Message
}

func (f *fakePublisher) Publish(_ context.Context, exchange, routingKey string, msg messaging.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, publishedMsg{
		exchange:   exchange,
		routingKey: routingKey,
		msg:        msg,
	})
	return nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func (f *fakePublisher) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func TestRelay_PublishesPendingEntries(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	msg1 := testMessage(t)
	msg2 := testMessage(t)
	require.NoError(t, writer.Write(ctx, db, "exchange1", "key1", msg1))
	require.NoError(t, writer.Write(ctx, db, "exchange2", "key2", msg2))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithBatchSize(10),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool {
		return pub.count() >= 2
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	var entries []outbox.Entry
	require.NoError(t, db.Find(&entries).Error)
	for _, e := range entries {
		assert.Equal(t, outbox.StatusPublished, e.Status)
		assert.NotNil(t, e.PublishedAt)
	}
}

func TestRelay_RetriesOnPublishError(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{err: errors.New("broker down")}
	logger := slog.Default()
	ctx := context.Background()

	msg := testMessage(t)
	require.NoError(t, writer.Write(ctx, db, "exchange", "key", msg))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithMaxAttempts(3),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool {
		var entry outbox.Entry
		if err := db.First(&entry).Error; err != nil {
			return false
		}
		return entry.Status == outbox.StatusFailed
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	var entry outbox.Entry
	require.NoError(t, db.First(&entry).Error)
	assert.Equal(t, outbox.StatusFailed, entry.Status)
	require.NotNil(t, entry.LastError)
	assert.Contains(t, *entry.LastError, "broker down")
}

func TestRelay_Stop(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	pub := &fakePublisher{}
	logger := slog.Default()

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(50*time.Millisecond),
	)

	done := make(chan error, 1)
	go func() {
		done <- relay.Start(context.Background())
	}()

	time.Sleep(20 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	require.NoError(t, relay.Stop(stopCtx))
	require.NoError(t, <-done)
}

func TestRelay_Cleanup(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	oldTime := time.Now().UTC().Add(-48 * time.Hour)
	id, _ := uuid.NewV7()
	entry := outbox.Entry{
		ID:          id,
		Exchange:    "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-old",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPublished,
		PublishedAt: &oldTime,
		CreatedAt:   oldTime,
	}
	require.NoError(t, store.Insert(ctx, db, entry))

	// Verify cleanup works via store directly since cleanup interval
	// is too long for unit tests.
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	deleted, err := store.DeletePublishedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Verify relay starts and stops cleanly.
	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithRetention(24*time.Hour),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relay.Start(relayCtx)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	require.NoError(t, <-relayDone)
}

func TestRelay_PublishesWithCorrectMessageContent(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	msg := testMessage(t).
		WithHeader("X-Correlation-Id", "corr-1").
		WithSchemaVersion(3)
	require.NoError(t, writer.Write(ctx, db, "events", "order.paid", msg))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool {
		return pub.count() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.published, 1)

	published := pub.published[0]
	assert.Equal(t, "events", published.exchange)
	assert.Equal(t, "order.paid", published.routingKey)
	assert.Equal(t, msg.ID, published.msg.ID)
	assert.Equal(t, msg.Type, published.msg.Type)
	assert.Equal(t, uint(3), published.msg.SchemaVersion)
	assert.Equal(t, "corr-1", published.msg.Headers["X-Correlation-Id"])

	var payload map[string]string
	require.NoError(t, json.Unmarshal(published.msg.Payload, &payload))
	assert.Equal(t, "value", payload["key"])
}

func TestRelay_Options(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	pub := &fakePublisher{}
	logger := slog.Default()

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(5*time.Second),
		outbox.WithBatchSize(50),
		outbox.WithMaxAttempts(5),
		outbox.WithRetention(48*time.Hour),
	)
	require.NotNil(t, relay)

	// Zero/negative values are ignored (defaults preserved).
	relay2 := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(0),
		outbox.WithBatchSize(0),
		outbox.WithMaxAttempts(0),
		outbox.WithRetention(0),
	)
	require.NotNil(t, relay2)
}

func TestRelay_WithMetrics(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	reg := prometheus.NewRegistry()
	metrics := outbox.NewMetrics(outbox.WithRegisterer(reg))

	msg := testMessage(t)
	require.NoError(t, writer.Write(ctx, db, "exchange", "key", msg))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithMetrics(metrics),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool {
		return pub.count() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	// Verify metrics were recorded.
	families, err := reg.Gather()
	require.NoError(t, err)

	metricNames := make(map[string]bool)
	for _, f := range families {
		metricNames[f.GetName()] = true
	}
	assert.True(t, metricNames["outbox_published_total"])
	assert.True(t, metricNames["outbox_relay_latency_seconds"])
}

func TestRelay_RecoverAfterPublisherError(t *testing.T) {
	db := testDB(t)
	store := outbox.NewGormStore(db)
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	msg := testMessage(t)
	require.NoError(t, writer.Write(ctx, db, "exchange", "key", msg))

	pub.setErr(errors.New("temporary error"))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithMaxAttempts(10),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	// Wait for at least one retry.
	time.Sleep(30 * time.Millisecond)

	// Clear error to let publish succeed.
	pub.setErr(nil)

	require.Eventually(t, func() bool {
		return pub.count() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	var entry outbox.Entry
	require.NoError(t, db.First(&entry).Error)
	assert.Equal(t, outbox.StatusPublished, entry.Status)
}
