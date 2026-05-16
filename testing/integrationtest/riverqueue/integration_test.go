//go:build integration

// Integration test: boots Postgres via testcontainers, runs River
// migrations, enqueues a kit envelope through Publisher, and asserts
// the registered EnvelopeWorker hands the message to the kit handler.
//
// Run with:
//
//	go test -tags=integration ./...
//
// This is gated because testcontainer startup is expensive and the
// unit suite must stay fast.

package riverqueue_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/queue/riverqueue/v2"
	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "river")
	q := url.Values{}
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// migrateRiver applies River's canonical schema to a fresh database.
// Uses the rivermigrate package directly rather than riverdbtest so the
// test mirrors what a production caller does at boot.
func migrateRiver(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	require.NoError(t, err)
	_, err = migrator.Migrate(context.Background(), rivermigrate.DirectionUp, nil)
	require.NoError(t, err)
}

func TestIntegration_RiverPublisher_RoundtripsThroughEnvelopeWorker(t *testing.T) {
	dsn := startPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	migrateRiver(t, pool)

	// Wire the kit envelope worker against a captured handler so we
	// can assert the round-trip carries the kit Message intact.
	var got atomic.Pointer[kitqueue.Message]
	handler := func(_ context.Context, msg kitqueue.Message) error {
		// Copy because msg is consumed once the worker returns.
		cp := msg
		got.Store(&cp)
		return nil
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, riverqueue.NewEnvelopeWorker(handler))

	const queueName = "kit-test"
	client, err := river.NewClient(riverqueue.DriverFromPool(pool), &river.Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Queues: map[string]river.QueueConfig{
			queueName: {MaxWorkers: 2},
		},
		Workers: workers,
	})
	require.NoError(t, err)

	completed, cancelSub := client.Subscribe(river.EventKindJobCompleted)
	defer cancelSub()

	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = client.Stop(shutdown)
	})

	// Publish via the kit's surface — the path operators wire when
	// they call app.Builder.WithRiver and pull *kitqueue.Publisher
	// out of Infrastructure.
	pub := riverqueue.NewPublisher(client)
	want := kitqueue.Message{
		ID:      "msg-1",
		Type:    "user.created",
		Payload: json.RawMessage(`{"id":42,"email":"a@b.com"}`),
	}
	require.NoError(t, pub.Enqueue(ctx, queueName, want))

	select {
	case <-completed:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for River to complete the enqueued job")
	}

	delivered := got.Load()
	require.NotNil(t, delivered, "handler was never invoked")
	assert.Equal(t, want.ID, delivered.ID)
	assert.Equal(t, want.Type, delivered.Type)
	assert.JSONEq(t, string(want.Payload), string(delivered.Payload))
}

func TestIntegration_RiverPublisher_RejectsEmptyQueue(t *testing.T) {
	dsn := startPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	migrateRiver(t, pool)

	workers := river.NewWorkers()
	// Worker registration is required even for the rejection path —
	// river.NewClient validates Queues against Workers.
	river.AddWorker(workers, riverqueue.NewEnvelopeWorker(func(context.Context, kitqueue.Message) error { return nil }))

	client, err := river.NewClient(riverqueue.DriverFromPool(pool), &river.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Queues:  map[string]river.QueueConfig{"unused": {MaxWorkers: 1}},
		Workers: workers,
	})
	require.NoError(t, err)

	pub := riverqueue.NewPublisher(client)
	err = pub.Enqueue(ctx, "", kitqueue.Message{ID: "x", Type: "y", Payload: json.RawMessage(`{}`)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queue name must not be empty")
}

// TestIntegration_RiverPublisher_DedupesBySharedID guards L050: the
// kit configures river.UniqueOpts{ByArgs, ByQueue} for messages with
// non-empty ID, so two Enqueue calls carrying the same ID against
// the same queue must produce exactly one delivered job (River
// silently rejects the duplicate Insert). Verifies the FR-059
// dedupe-by-args claim against a real River+Postgres rather than
// asserting on the option struct alone.
func TestIntegration_RiverPublisher_DedupesBySharedID(t *testing.T) {
	dsn := startPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	migrateRiver(t, pool)

	var delivered atomic.Int32
	handler := func(_ context.Context, _ kitqueue.Message) error {
		delivered.Add(1)
		return nil
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, riverqueue.NewEnvelopeWorker(handler))

	const queueName = "kit-dedupe"
	client, err := river.NewClient(riverqueue.DriverFromPool(pool), &river.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Queues:  map[string]river.QueueConfig{queueName: {MaxWorkers: 2}},
		Workers: workers,
	})
	require.NoError(t, err)

	completed, cancelSub := client.Subscribe(river.EventKindJobCompleted)
	defer cancelSub()

	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = client.Stop(shutdown)
	})

	pub := riverqueue.NewPublisher(client)
	want := kitqueue.Message{
		ID:      "dedupe-msg-1",
		Type:    "user.created",
		Payload: json.RawMessage(`{"id":42}`),
	}

	// Enqueue the same ID three times — River must persist exactly one.
	require.NoError(t, pub.Enqueue(ctx, queueName, want))
	require.NoError(t, pub.Enqueue(ctx, queueName, want))
	require.NoError(t, pub.Enqueue(ctx, queueName, want))

	// Wait for the first completion, then hold to confirm no follow-ups arrive.
	select {
	case <-completed:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for River to complete the first enqueued job")
	}

	// Brief grace window: if River was about to deliver a duplicate it
	// would have fired by now (in-memory subscribe is instant once the
	// job moves to completed).
	select {
	case <-completed:
		t.Fatalf("second job delivered — dedupe-by-args failed")
	case <-time.After(2 * time.Second):
	}

	assert.Equal(t, int32(1), delivered.Load(),
		"three Enqueue calls with the same ID must produce exactly one delivery; got %d", delivered.Load())
}

// TestIntegration_RiverPublisher_DistinctIDsAllDelivered is the
// inverse check: with three different IDs (or empty IDs that bypass
// the dedupe path) we must see all three deliveries. Guards against
// a regression that over-dedupes.
func TestIntegration_RiverPublisher_DistinctIDsAllDelivered(t *testing.T) {
	dsn := startPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	migrateRiver(t, pool)

	var delivered atomic.Int32
	handler := func(_ context.Context, _ kitqueue.Message) error {
		delivered.Add(1)
		return nil
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, riverqueue.NewEnvelopeWorker(handler))

	const queueName = "kit-distinct"
	client, err := river.NewClient(riverqueue.DriverFromPool(pool), &river.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Queues:  map[string]river.QueueConfig{queueName: {MaxWorkers: 2}},
		Workers: workers,
	})
	require.NoError(t, err)

	completed, cancelSub := client.Subscribe(river.EventKindJobCompleted)
	defer cancelSub()

	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = client.Stop(shutdown)
	})

	pub := riverqueue.NewPublisher(client)
	for i := 0; i < 3; i++ {
		require.NoError(t, pub.Enqueue(ctx, queueName, kitqueue.Message{
			ID:      "distinct-" + strconv.Itoa(i),
			Type:    "user.created",
			Payload: json.RawMessage(`{}`),
		}))
	}

	// Wait for all three completions.
	for i := 0; i < 3; i++ {
		select {
		case <-completed:
		case <-time.After(20 * time.Second):
			t.Fatalf("timed out waiting for delivery %d/3", i+1)
		}
	}
	assert.Equal(t, int32(3), delivered.Load())
}
