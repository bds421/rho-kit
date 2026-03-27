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

	"github.com/bds421/rho-kit/infra/outbox"
)

// fakePublisher records published entries and can simulate errors.
type fakePublisher struct {
	mu        sync.Mutex
	published []outbox.Entry
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, entry outbox.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, entry)
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

func newTestEntry(t *testing.T, topic, routingKey string) outbox.Entry {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	return outbox.Entry{
		ID:          id,
		Topic:       topic,
		RoutingKey:  routingKey,
		MessageID:   uuid.New().String(),
		MessageType: topic + ".event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
}

func TestRelay_PublishesPendingEntries(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	params1 := outbox.WriteParams{
		Topic:       "topic1",
		RoutingKey:  "key1",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{"key":"value"}`),
	}
	params2 := outbox.WriteParams{
		Topic:       "topic2",
		RoutingKey:  "key2",
		MessageID:   "msg-2",
		MessageType: "test.event",
		Payload:     []byte(`{"key":"value2"}`),
	}
	require.NoError(t, writer.Write(ctx, params1))
	require.NoError(t, writer.Write(ctx, params2))

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

	// Verify entries are marked published in the store.
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, e := range store.entries {
		assert.Equal(t, outbox.StatusPublished, e.Status)
		assert.NotNil(t, e.PublishedAt)
	}
}

func TestRelay_RetriesOnPublishError(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{err: errors.New("broker down")}
	logger := slog.Default()
	ctx := context.Background()

	params := outbox.WriteParams{
		Topic:       "topic",
		RoutingKey:  "key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
	}
	require.NoError(t, writer.Write(ctx, params))

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
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.entries) == 0 {
			return false
		}
		return store.entries[0].Status == outbox.StatusFailed
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()

	assert.Equal(t, outbox.StatusFailed, entry.Status)
	require.NotNil(t, entry.LastError)
	assert.Contains(t, *entry.LastError, "broker down")
}

func TestRelay_Stop(t *testing.T) {
	store := &fakeStore{}
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
	store := &fakeStore{}
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	oldTime := time.Now().UTC().Add(-48 * time.Hour)
	id, _ := uuid.NewV7()
	entry := outbox.Entry{
		ID:          id,
		Topic:       "test",
		RoutingKey:  "test.key",
		MessageID:   "msg-old",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPublished,
		PublishedAt: &oldTime,
		CreatedAt:   oldTime,
	}
	require.NoError(t, store.Insert(ctx, entry))

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

func TestRelay_PublishesWithCorrectEntryContent(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	payload, err := json.Marshal(map[string]string{"key": "value"})
	require.NoError(t, err)

	params := outbox.WriteParams{
		Topic:       "events",
		RoutingKey:  "order.paid",
		MessageID:   "msg-1",
		MessageType: "order.paid",
		Payload:     payload,
		Headers:     map[string]string{"X-Correlation-Id": "corr-1"},
	}
	require.NoError(t, writer.Write(ctx, params))

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
	assert.Equal(t, "events", published.Topic)
	assert.Equal(t, "order.paid", published.RoutingKey)
	assert.Equal(t, "msg-1", published.MessageID)
	assert.Equal(t, "order.paid", published.MessageType)

	headers, err := published.HeadersMap()
	require.NoError(t, err)
	assert.Equal(t, "corr-1", headers["X-Correlation-Id"])

	var p map[string]string
	require.NoError(t, json.Unmarshal(published.Payload, &p))
	assert.Equal(t, "value", p["key"])
}

func TestRelay_Options(t *testing.T) {
	store := &fakeStore{}
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
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	reg := prometheus.NewRegistry()
	metrics := outbox.NewMetrics(outbox.WithRegisterer(reg))

	params := outbox.WriteParams{
		Topic:       "topic",
		RoutingKey:  "key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
	}
	require.NoError(t, writer.Write(ctx, params))

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

func TestRelay_RecoverStaleProcessingEntries(t *testing.T) {
	store := &fakeStore{}
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	// Insert an entry that is stuck in "processing" with a very old created_at.
	staleTime := time.Now().UTC().Add(-10 * time.Minute)
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
	require.NoError(t, store.Insert(ctx, staleEntry))

	// Run relay with very short poll interval so stale recovery triggers
	// (every 10 polls).
	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(5*time.Millisecond),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	// Wait for the message to be published (stale recovery resets to pending,
	// then FetchPending picks it up and publishes).
	require.Eventually(t, func() bool {
		return pub.count() >= 1
	}, 2*time.Second, 10*time.Millisecond)

	// Wait for the final status to be published.
	require.Eventually(t, func() bool {
		found, ok := store.findByID(staleID)
		return ok && found.Status == outbox.StatusPublished
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

func TestRelay_RecoverAfterPublisherError(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	params := outbox.WriteParams{
		Topic:       "topic",
		RoutingKey:  "key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
	}
	require.NoError(t, writer.Write(ctx, params))

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

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()
	assert.Equal(t, outbox.StatusPublished, entry.Status)
}
