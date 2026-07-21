package outbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/infra/v2/outbox"
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

type panicPublisher struct{}

func (panicPublisher) Publish(context.Context, outbox.Entry) error {
	panic("publisher exploded")
}

func TestNewRelay_PanicsOnNilDeps(t *testing.T) {
	cases := []struct {
		name      string
		store     outbox.Store
		publisher outbox.Publisher
	}{
		{"nil store", nil, &fakePublisher{}},
		{"nil publisher", &fakeStore{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic, got none")
				}
			}()
			outbox.NewRelay(tc.store, tc.publisher, nil)
		})
	}
}

func TestNewRelay_NilLoggerDefaults(t *testing.T) {
	// Logger nil should not panic — slog.Default() takes over.
	r := outbox.NewRelay(&fakeStore{}, &fakePublisher{}, nil)
	if r == nil {
		t.Fatal("expected relay, got nil")
	}
}

func TestNewRelay_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		outbox.NewRelay(&fakeStore{}, &fakePublisher{}, nil, nil)
	})
}

func TestRelay_PublishesPendingEntries(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
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
	writer := outbox.NewWriterWithoutTransactionCheck(store)
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

	// maxAttempts=2: the first failure schedules a future retry and the second
	// exhausts attempts and marks the entry failed. The test verifies that gate,
	// then advances the fake store past it instead of sleeping for two seconds.
	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithMaxAttempts(2),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	store.mu.Lock()
	entryID := store.entries[0].ID
	store.mu.Unlock()
	var scheduledRetry time.Time
	require.Eventually(t, func() bool {
		var ok bool
		scheduledRetry, ok = store.forceRetryEligible(entryID)
		return ok
	}, time.Second, 5*time.Millisecond)
	assert.True(t, scheduledRetry.After(time.Now()),
		"first failure must schedule a future retry gate")

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.entries) == 0 {
			return false
		}
		return store.entries[0].Status == outbox.StatusFailed
	}, time.Second, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()

	assert.Equal(t, outbox.StatusFailed, entry.Status)
	require.NotNil(t, entry.LastError)
	// last_error keeps the redacted concrete error type for triage but must
	// never contain the raw broker message.
	assert.Contains(t, *entry.LastError, "<redacted error:")
	assert.NotContains(t, *entry.LastError, "broker down")
}

// errAuth and errTimeout are distinct concrete error types used to prove that
// last_error carries a distinguishing diagnostic signal (the redacted concrete
// type) instead of a single opaque constant.
type errAuth struct{}

func (errAuth) Error() string { return "auth rejected: token=tenant-secret" }

type errTimeout struct{}

func (errTimeout) Error() string { return "deadline exceeded contacting broker.internal" }

// TestRelay_PublishErrorRecordsDistinguishableType pins the medium-severity
// diagnostics finding: every publish failure used to store the constant
// "publish failed" as last_error, so operators could not tell a timeout from
// an auth failure from the DB or logs. The fix records the redacted concrete
// error type — distinguishable across failure modes — while never leaking the
// raw error message.
func TestRelay_PublishErrorRecordsDistinguishableType(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		secret  string // a raw substring that must never appear in last_error
		typeTag string // distinguishing fragment that must appear
	}{
		{"auth", errAuth{}, "tenant-secret", "outbox_test.errAuth"},
		{"timeout", errTimeout{}, "broker.internal", "outbox_test.errTimeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			writer := outbox.NewWriterWithoutTransactionCheck(store)
			pub := &fakePublisher{err: tc.err}
			logs := &syncBuffer{}
			logger := slog.New(slog.NewTextHandler(logs, nil))
			ctx := context.Background()

			require.NoError(t, writer.Write(ctx, outbox.WriteParams{
				Topic:       "topic",
				RoutingKey:  "key",
				MessageID:   "msg-1",
				MessageType: "test.event",
				Payload:     []byte(`{}`),
			}))

			relay := outbox.NewRelay(store, pub, logger,
				outbox.WithPollInterval(10*time.Millisecond),
				outbox.WithMaxAttempts(1),
			)

			relayCtx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- relay.Start(relayCtx) }()

			require.Eventually(t, func() bool {
				store.mu.Lock()
				defer store.mu.Unlock()
				return len(store.entries) == 1 && store.entries[0].Status == outbox.StatusFailed
			}, 2*time.Second, 10*time.Millisecond)

			cancel()
			require.NoError(t, <-done)

			store.mu.Lock()
			entry := store.entries[0]
			store.mu.Unlock()

			require.NotNil(t, entry.LastError)
			// Diagnostic signal: the concrete type distinguishes failure modes.
			assert.Contains(t, *entry.LastError, tc.typeTag,
				"last_error must carry the concrete error type for triage")
			// Security invariant: raw error text must never be persisted.
			assert.NotContains(t, *entry.LastError, tc.secret,
				"last_error must not leak the raw error message")

			// The real (redacted) error type must also reach the logs, not just
			// the opaque constant.
			assert.Contains(t, logs.String(), tc.typeTag,
				"redacted publish error type must be logged")
			assert.NotContains(t, logs.String(), tc.secret,
				"raw error message must not reach the logs")
		})
	}
}

func TestRelay_HandlesPublisherPanicAsPublishError(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
	ctx := context.Background()

	params := outbox.WriteParams{
		Topic:       "topic",
		RoutingKey:  "key",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
	}
	require.NoError(t, writer.Write(ctx, params))

	relay := outbox.NewRelay(store, panicPublisher{}, slog.Default(),
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithMaxAttempts(1),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.entries) == 1 && store.entries[0].Status == outbox.StatusFailed
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()
	require.NotNil(t, entry.LastError)
	// last_error keeps the redacted concrete error type for triage but must
	// never contain the raw panic value text.
	assert.Contains(t, *entry.LastError, "<redacted error:")
	assert.NotContains(t, *entry.LastError, "publisher exploded")
}

func TestRelay_StoreErrorLogRedactsBackendError(t *testing.T) {
	store := &fakeStore{fetchPendingErr: errors.New("database token=tenant-secret")}
	pub := &fakePublisher{}
	// syncBuffer is goroutine-safe; the relay's poll-loop goroutine
	// writes while the Eventually-poll below reads. A plain bytes.Buffer
	// would race here.
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	// Poll for the log assertion rather than sleeping a guess-interval.
	// The relay's poll loop ticks every 10ms (WithPollInterval above);
	// under heavy CI load a fixed 25ms wait was tight enough to flake.
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "outbox relay: fetch pending failed")
	}, 2*time.Second, 10*time.Millisecond, "expected fetch-failure log within 2s")
	cancel()
	require.NoError(t, <-done)

	got := logs.String()
	assert.Contains(t, got, "outbox relay: fetch pending failed")
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
}

func TestRelay_Stop(t *testing.T) {
	// Use the start-signal store so we deterministically know the Start
	// goroutine has begun polling before calling Stop. A bare time.Sleep
	// could let Stop set stopped=true first on a loaded CI runner, making
	// Start return "already stopped" and the test fail spuriously.
	store := newStartSignalStore()
	pub := &fakePublisher{}
	logger := slog.Default()

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(50*time.Millisecond),
	)

	done := make(chan error, 1)
	go func() {
		done <- relay.Start(context.Background())
	}()

	store.waitForFetch(t)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	require.NoError(t, relay.Stop(stopCtx))
	require.NoError(t, <-done)
}

func TestRelay_StartRejectsNilContext(t *testing.T) {
	relay := outbox.NewRelay(&fakeStore{}, &fakePublisher{}, slog.Default())
	var ctx context.Context
	err := relay.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestRelay_StartRejectsSecondStart(t *testing.T) {
	store := newStartSignalStore()
	relay := outbox.NewRelay(store, &fakePublisher{}, slog.Default(),
		outbox.WithPollInterval(time.Hour),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Start(relayCtx) }()

	store.waitForFetch(t)

	err := relay.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancel()
	require.NoError(t, <-done)
}

func TestRelay_StartRejectsAfterStopBeforeStart(t *testing.T) {
	relay := outbox.NewRelay(&fakeStore{}, &fakePublisher{}, slog.Default())

	require.NoError(t, relay.Stop(context.Background()))

	err := relay.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestRelay_StartRejectsRestartAfterStop(t *testing.T) {
	store := newStartSignalStore()
	relay := outbox.NewRelay(store, &fakePublisher{}, slog.Default(),
		outbox.WithPollInterval(time.Hour),
	)

	done := make(chan error, 1)
	go func() { done <- relay.Start(context.Background()) }()
	store.waitForFetch(t)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	require.NoError(t, relay.Stop(stopCtx))
	require.NoError(t, <-done)

	err := relay.Start(context.Background())
	require.Error(t, err)
	// A relay that ran and was stopped reports its actual terminal state on
	// restart: "already stopped", not "already started". The latter would
	// point operators at the wrong condition (a concurrent run that is not
	// happening).
	assert.Contains(t, err.Error(), "already stopped")
}

func TestRelay_StopRejectsNilContext(t *testing.T) {
	relay := outbox.NewRelay(&fakeStore{}, &fakePublisher{}, slog.Default())
	var ctx context.Context
	err := relay.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

type startSignalStore struct {
	fakeStore
	fetchStarted chan struct{}
	once         sync.Once
}

func newStartSignalStore() *startSignalStore {
	return &startSignalStore{fetchStarted: make(chan struct{})}
}

func (s *startSignalStore) FetchPending(ctx context.Context, _ int) ([]outbox.Entry, error) {
	s.once.Do(func() { close(s.fetchStarted) })
	<-ctx.Done()
	return nil, nil
}

func (s *startSignalStore) waitForFetch(t *testing.T) {
	t.Helper()
	select {
	case <-s.fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("relay did not start polling")
	}
}

func TestRelay_Cleanup(t *testing.T) {
	store := &fakeStore{}
	pub := &fakePublisher{}
	logger := slog.Default()
	ctx := context.Background()

	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour)
	recentTime := now.Add(-1 * time.Hour)

	oldPublishedID := uuid.UUID(id.NewBytes())
	recentPublishedID := uuid.UUID(id.NewBytes())
	oldFailedID := uuid.UUID(id.NewBytes())
	recentFailedID := uuid.UUID(id.NewBytes())

	for _, entry := range []outbox.Entry{
		{
			ID:          oldPublishedID,
			Topic:       "test",
			RoutingKey:  "test.key",
			MessageID:   "msg-published-old",
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusPublished,
			PublishedAt: &oldTime,
			CreatedAt:   oldTime,
		},
		{
			ID:          recentPublishedID,
			Topic:       "test",
			RoutingKey:  "test.key",
			MessageID:   "msg-published-recent",
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusPublished,
			PublishedAt: &recentTime,
			CreatedAt:   recentTime,
		},
		{
			ID:          oldFailedID,
			Topic:       "test",
			RoutingKey:  "test.key",
			MessageID:   "msg-failed-old",
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusFailed,
			CreatedAt:   oldTime,
		},
		{
			ID:          recentFailedID,
			Topic:       "test",
			RoutingKey:  "test.key",
			MessageID:   "msg-failed-recent",
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusFailed,
			CreatedAt:   recentTime,
		},
	} {
		require.NoError(t, store.Insert(ctx, entry))
	}

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithRetention(24*time.Hour),
		outbox.WithFailedRetention(24*time.Hour),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool {
		return store.count() == 2
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-relayDone)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.entries, 2)
	remaining := make(map[string]outbox.Status, len(store.entries))
	for _, entry := range store.entries {
		remaining[entry.MessageID] = entry.Status
	}
	assert.Equal(t, map[string]outbox.Status{
		"msg-published-recent": outbox.StatusPublished,
		"msg-failed-recent":    outbox.StatusFailed,
	}, remaining)
}

func TestRelay_PublishesWithCorrectEntryContent(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
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
		outbox.WithFailedRetention(14*24*time.Hour),
	)
	require.NotNil(t, relay)
}

func TestRelay_OptionsPanicOnInvalidValues(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithPollInterval zero":     func() { outbox.WithPollInterval(0) },
		"WithPollInterval negative": func() { outbox.WithPollInterval(-time.Second) },
		"WithBatchSize zero":        func() { outbox.WithBatchSize(0) },
		"WithBatchSize negative":    func() { outbox.WithBatchSize(-1) },
		"WithMaxAttempts zero":      func() { outbox.WithMaxAttempts(0) },
		"WithMaxAttempts negative":  func() { outbox.WithMaxAttempts(-1) },
		"WithRetention zero":        func() { outbox.WithRetention(0) },
		"WithRetention negative":    func() { outbox.WithRetention(-time.Second) },
		"WithFailedRetention zero":  func() { outbox.WithFailedRetention(0) },
		"WithFailedRetention negative": func() {
			outbox.WithFailedRetention(-time.Second)
		},
		"WithStaleDuration zero":     func() { outbox.WithStaleDuration(0) },
		"WithStaleDuration negative": func() { outbox.WithStaleDuration(-time.Second) },
		"WithPublishTimeout zero":    func() { outbox.WithPublishTimeout(0) },
		"WithPublishTimeout negative": func() {
			outbox.WithPublishTimeout(-time.Second)
		},
		"WithMaxConcurrentPublishes zero": func() {
			outbox.WithMaxConcurrentPublishes(0)
		},
		"WithMaxConcurrentPublishes negative": func() {
			outbox.WithMaxConcurrentPublishes(-1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, fn)
		})
	}
}

func TestRelay_WithMetrics(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
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
	staleID := uuid.UUID(id.NewBytes())
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

// TestRelay_LongPublishDoesNotDuplicate pins the high-severity stale-recovery
// finding: a publish that legitimately exceeds the configured stale duration
// must not be reset to pending and double-published. The fix is heartbeating
// the processing row + a configurable stale duration. This test wires both:
//
//   - staleDuration: 300ms, which produces the package's 100ms minimum
//     heartbeat cadence.
//   - the publisher is gated for at least four heartbeats (> one stale
//     duration), after which the test drives a competing stale reset directly.
//   - We assert exactly one publish reached the publisher AND the row ends
//     up in published state.
func TestRelay_LongPublishDoesNotDuplicate(t *testing.T) {
	store := &fakeStore{}
	logger := slog.Default()
	ctx := context.Background()

	entryID := uuid.UUID(id.NewBytes())
	require.NoError(t, store.Insert(ctx, outbox.Entry{
		ID: entryID, Topic: "t", RoutingKey: "rk", MessageID: "msg-1",
		MessageType: "test.event", Payload: []byte(`{}`),
		Status: outbox.StatusPending, CreatedAt: time.Now().UTC().Add(-time.Hour),
	}))
	pub := &gatedPublisher{gate: make(chan struct{})}
	const staleWindow = 300 * time.Millisecond

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(50*time.Millisecond),
		outbox.WithStaleDuration(staleWindow),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- relay.Start(relayCtx)
	}()

	require.Eventually(t, func() bool { return pub.entered() >= 1 }, time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		return store.heartbeatCalls.Load() >= 4
	}, 2*time.Second, 5*time.Millisecond,
		"gated publish must remain active for more than one stale window")

	reset, err := store.ResetStaleProcessing(ctx, staleWindow)
	require.NoError(t, err)
	assert.Equal(t, int64(0), reset,
		"in-flight entry must be heartbeated so a competing relay cannot reclaim it")

	close(pub.gate)
	require.Eventually(t, func() bool { return pub.count() >= 1 }, time.Second, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, 1, pub.count(),
		"long publish must not be duplicated by stale recovery (heartbeat keeps the row alive)")

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.entries, 1)
	assert.Equal(t, outbox.StatusPublished, store.entries[0].Status)

	// Heartbeat must have been called at least once during the slow publish.
	assert.GreaterOrEqual(t, store.heartbeatCalls.Load(), int64(1),
		"heartbeat must fire while publish is in flight")
}

// TestRelay_QueuedBatchEntriesAreHeartbeated pins the high-severity batch
// heartbeat finding: FetchPending claims a whole batch to "processing" at T0,
// but the per-entry heartbeat only refreshes the row currently being published.
// Entries queued behind a slow publish keep updated_at=T0, so another replica's
// ResetStaleProcessing can reclaim them mid-batch — both relays then publish the
// same entry.
//
// The fix heartbeats every claimed-but-unfinished id for the lifetime of the
// batch. This test gates the first publish so the second entry sits in
// "processing", drives a stale window, simulates a competing replica's
// stale-recovery sweep, then releases the gate. The queued entry must survive
// the sweep (its heartbeat keeps updated_at fresh) and be published exactly
// once.
func TestRelay_QueuedBatchEntriesAreHeartbeated(t *testing.T) {
	store := &fakeStore{}
	logger := slog.Default()
	ctx := context.Background()

	// Two entries created well in the past so ResetStaleProcessing would treat
	// them as stale-eligible the instant they sit in "processing" without a
	// fresh heartbeat.
	staleCreated := time.Now().UTC().Add(-1 * time.Hour)
	for i := 0; i < 2; i++ {
		require.NoError(t, store.Insert(ctx, outbox.Entry{
			ID:          uuid.UUID(id.NewBytes()),
			Topic:       "t",
			RoutingKey:  "rk",
			MessageID:   "msg-" + string(rune('a'+i)),
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusPending,
			CreatedAt:   staleCreated,
		}))
	}

	pub := &gatedPublisher{gate: make(chan struct{})}

	// staleDuration=300ms produces the package's 100ms minimum heartbeat.
	// Four observed beats prove the queued claim stays fresh beyond a full
	// stale window without relying on an imprecise wall-clock sleep.
	const staleWindow = 300 * time.Millisecond
	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(50*time.Millisecond),
		outbox.WithStaleDuration(staleWindow),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Start(relayCtx) }()

	// Wait until the relay is blocked inside the first publish (batch is claimed,
	// entry[1] is queued in "processing").
	require.Eventually(t, func() bool {
		return pub.entered() >= 1
	}, 2*time.Second, 5*time.Millisecond)

	// Observe more than a full stale window's worth of beats, then simulate a
	// competing replica reclaiming stale rows. With the bug the queued entry's
	// updated_at remains T0 and gets reset to pending.
	require.Eventually(t, func() bool {
		return store.heartbeatCalls.Load() >= 4
	}, 2*time.Second, 5*time.Millisecond)
	reset, err := store.ResetStaleProcessing(ctx, staleWindow)
	require.NoError(t, err)
	assert.Equal(t, int64(0), reset,
		"queued claimed entry must be heartbeated so a competing stale-recovery does not reclaim it")

	// Release the gated publish and let the batch complete.
	close(pub.gate)

	require.Eventually(t, func() bool {
		return pub.count() >= 2
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, 2, pub.count(),
		"each claimed entry must be published exactly once (no stale-reset duplicate)")
}

// gatedPublisher blocks the first publish on a gate channel, then records every
// publish. Subsequent publishes pass straight through. Used to hold a batch's
// first entry in flight while the queued remainder sits in "processing".
type gatedPublisher struct {
	mu        sync.Mutex
	published []outbox.Entry
	entries   atomic.Int64
	gate      chan struct{}
}

func (g *gatedPublisher) Publish(_ context.Context, entry outbox.Entry) error {
	first := g.entries.Add(1) == 1
	if first {
		<-g.gate
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.published = append(g.published, entry)
	return nil
}

func (g *gatedPublisher) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.published)
}

func (g *gatedPublisher) entered() int64 {
	return g.entries.Load()
}

func TestRelay_RecoverAfterPublisherError(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
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

	store.mu.Lock()
	entryID := store.entries[0].ID
	store.mu.Unlock()
	require.Eventually(t, func() bool {
		entry, ok := store.findByID(entryID)
		return ok && entry.Attempts == 1 && entry.NextRetryAt != nil
	}, time.Second, 5*time.Millisecond)

	// Clear the fault, then move the already-asserted retry deadline into the
	// past. This exercises recovery through the real NextRetryAt gate without
	// sleeping through the production 2s backoff.
	pub.setErr(nil)
	scheduledRetry, ok := store.forceRetryEligible(entryID)
	require.True(t, ok)
	assert.True(t, scheduledRetry.After(time.Now()),
		"first failure must schedule a future retry gate")
	require.Eventually(t, func() bool {
		return pub.count() >= 1
	}, time.Second, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()
	assert.Equal(t, outbox.StatusPublished, entry.Status)
}

// resetterStore wraps fakeStore and implements outbox.PendingResetter,
// recording the ids passed to ResetPending and whether the context it was
// handed was already cancelled. It also gates the first publish so the relay
// is guaranteed to be mid-batch (rows still claimed) when shutdown begins.
type resetterStore struct {
	fakeStore

	mu             sync.Mutex
	resetIDs       []string
	resetCtxLive   bool // true if the ResetPending ctx was NOT already cancelled
	resetCtxHadErr bool
	resetCalls     int
}

// The outcome methods mirror the real pgx store, which fails any write
// issued on an already-cancelled context (pgx Exec returns the ctx error
// before touching the row). Under shutdown the relay's outcome calls run
// on the cancelled run ctx, so the in-flight row stays "processing" and
// must be reset by the shutdown path rather than silently completed.
func (s *resetterStore) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeStore.MarkPublished(ctx, id, publishedAt)
}

func (s *resetterStore) MarkFailed(ctx context.Context, id string, lastError string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeStore.MarkFailed(ctx, id, lastError)
}

func (s *resetterStore) IncrementAttempts(ctx context.Context, id string, lastError string, nextRetryAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.fakeStore.IncrementAttempts(ctx, id, lastError, nextRetryAt)
}

func (s *resetterStore) ResetPending(ctx context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetCalls++
	s.resetIDs = append(s.resetIDs, ids...)
	s.resetCtxHadErr = ctx.Err() != nil
	s.resetCtxLive = ctx.Err() == nil
	// Mirror the real store: return the rows to pending so state stays sane.
	for _, id := range ids {
		for i := range s.entries {
			if s.entries[i].ID.String() == id &&
				s.entries[i].Status == outbox.StatusProcessing {
				s.entries[i] = withStatus(s.entries[i], outbox.StatusPending)
			}
		}
	}
	return nil
}

func (s *resetterStore) snapshot() (calls int, ids []string, ctxLive, ctxHadErr bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.resetIDs...)
	return s.resetCalls, out, s.resetCtxLive, s.resetCtxHadErr
}

// blockingPublisher blocks every publish on a gate channel and reports when at
// least one publish has been entered, so a test can stop the relay while a
// batch is mid-flight (rows claimed, none yet terminal).
type blockingPublisher struct {
	entered atomic.Int64
	gate    chan struct{}
}

func (b *blockingPublisher) Publish(ctx context.Context, _ outbox.Entry) error {
	b.entered.Add(1)
	select {
	case <-b.gate:
	case <-ctx.Done():
	}
	return ctx.Err()
}

// TestRelay_ResetsClaimedEntriesOnShutdown pins DEFECT A: when the relay is
// stopped mid-batch, rows it has already claimed sit in "processing" until the
// slow stale sweep. The fix tracks claimed-but-unfinished ids and, if the store
// implements outbox.PendingResetter, resets them on shutdown using a fresh
// (non-cancelled) context.
func TestRelay_ResetsClaimedEntriesOnShutdown(t *testing.T) {
	store := &resetterStore{}
	logger := slog.Default()
	ctx := context.Background()

	const n = 3
	wantIDs := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		e := outbox.Entry{
			ID:          uuid.UUID(id.NewBytes()),
			Topic:       "t",
			RoutingKey:  "rk",
			MessageID:   "msg-" + string(rune('a'+i)),
			MessageType: "test.event",
			Payload:     []byte(`{}`),
			Status:      outbox.StatusPending,
			CreatedAt:   time.Now().UTC(),
		}
		require.NoError(t, store.Insert(ctx, e))
		wantIDs[e.ID.String()] = struct{}{}
	}

	pub := &blockingPublisher{gate: make(chan struct{})}

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithBatchSize(n),
		// Disable the publish timeout so the publish blocks until shutdown
		// cancels the context, keeping the batch mid-flight.
		outbox.WithoutPublishTimeout(),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Start(relayCtx) }()

	// Wait until the relay is blocked inside the first publish: the whole batch
	// is now claimed to "processing" and none has reached a terminal outcome.
	require.Eventually(t, func() bool {
		return pub.entered.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond)

	// Stop the relay mid-batch.
	cancel()
	require.NoError(t, <-done)

	calls, gotIDs, ctxLive, ctxHadErr := store.snapshot()
	require.GreaterOrEqual(t, calls, 1, "ResetPending must be called on shutdown")
	assert.True(t, ctxLive, "ResetPending must use a non-cancelled context")
	assert.False(t, ctxHadErr, "ResetPending context must not already be cancelled")

	// Every still-claimed id must have been reset. The first entry was blocked
	// in publish (never terminal) and the remaining serial-path entries were
	// never reached, so all n ids are still claimed at shutdown.
	gotSet := make(map[string]struct{}, len(gotIDs))
	for _, gid := range gotIDs {
		gotSet[gid] = struct{}{}
	}
	for wid := range wantIDs {
		assert.Contains(t, gotSet, wid, "claimed id %s must be reset on shutdown", wid)
	}

	// And the store rows must be back to pending, not stranded in processing.
	store.fakeStore.mu.Lock()
	defer store.fakeStore.mu.Unlock()
	for _, e := range store.entries {
		assert.NotEqual(t, outbox.StatusProcessing, e.Status,
			"no row may remain in processing after shutdown reset")
	}
}

// TestRelay_ShutdownWithoutResetterDoesNotPanic confirms the optional-capability
// contract: a RelayStore that does NOT implement PendingResetter shuts down
// cleanly (the reset path is simply skipped).
func TestRelay_ShutdownWithoutResetterDoesNotPanic(t *testing.T) {
	store := &fakeStore{}
	pub := &blockingPublisher{gate: make(chan struct{})}
	logger := slog.Default()
	ctx := context.Background()

	require.NoError(t, store.Insert(ctx, outbox.Entry{
		ID:          uuid.UUID(id.NewBytes()),
		Topic:       "t",
		RoutingKey:  "rk",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     []byte(`{}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}))

	relay := outbox.NewRelay(store, pub, logger,
		outbox.WithPollInterval(10*time.Millisecond),
		outbox.WithoutPublishTimeout(),
	)

	relayCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Start(relayCtx) }()

	require.Eventually(t, func() bool {
		return pub.entered.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

// syncBuffer is a goroutine-safe bytes.Buffer wrapper for capturing
// logs across the relay's poll-loop goroutine and the test's polling
// assertions. A plain bytes.Buffer is documented as non-safe for
// concurrent Read/Write — without this wrapper, require.Eventually
// polling logs.String() while the relay still writes triggers a real
// data race under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
