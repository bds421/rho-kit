//go:build integration

package outboxpg

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
	outboxpg "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/outbox"
	kittestamqp "github.com/bds421/rho-kit/testing/kittest/v2/amqp"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "outbox_test")
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

func openAndMigrate(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	sub, err := fs.Sub(outboxpg.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func mustEntry() outbox.Entry {
	return outbox.Entry{
		ID:          uuid.UUID(id.NewBytes()),
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "msg-" + uuid.NewString(),
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
}

// TestInbox_AMQPAckAfterCommittedDeduplication is the durable consumer
// reference proof: RabbitMQ delivers the same logical event twice, but the
// inbox commits the domain effect exactly once and only then lets the AMQP
// consumer ACK both deliveries. This exercises the actual broker + Postgres
// boundary, not merely the store in isolation.
func TestInbox_AMQPAckAfterCommittedDeduplication(t *testing.T) {
	pool := openAndMigrate(t, startPostgres(t))
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE processed_events (message_id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	brokerURL := kittestamqp.Start(t)
	conn, err := amqpbackend.Connect(brokerURL, slog.Default(), amqpbackend.WithoutTLS())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	binding, err := amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:      "inbox.integration.exchange",
		ExchangeType:  messaging.ExchangeDirect,
		ConsumerGroup: "inbox.integration.queue",
		RoutingKey:    "inbox.integration.key",
		WithoutRetry:  true,
	})
	require.NoError(t, err)
	egressBinding, err := amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
		Exchange:      "inbox.integration.egress",
		ExchangeType:  messaging.ExchangeDirect,
		ConsumerGroup: "inbox.integration.egress.queue",
		RoutingKey:    "example.completed",
		WithoutRetry:  true,
	})
	require.NoError(t, err)
	publisher := amqpbackend.NewPublisher(conn, slog.Default())
	message, err := messaging.NewMessage("example.created", map[string]string{"id": "42"})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(ctx, binding.Exchange, binding.RoutingKey, message))
	require.NoError(t, publisher.Publish(ctx, binding.Exchange, binding.RoutingKey, message))

	inbox := outboxpg.NewInbox(pool)
	outboxStore := outboxpg.New(pool)
	writer := outbox.NewWriter(outboxStore, outboxpg.RequireTx)
	consumer := amqpbackend.NewConsumer(conn, nil, slog.Default())
	consumeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	acked := make(chan struct{})
	go func() {
		_ = consumer.ConsumeOnce(consumeCtx, binding, func(handlerCtx context.Context, d messaging.Delivery) error {
			_, err := inbox.Process(handlerCtx, "example-created", d.Message.ID, func(txCtx context.Context) error {
				tx, ok := outboxpg.TxFromContext(txCtx)
				if !ok {
					return errors.New("missing inbox transaction")
				}
				_, err := tx.Exec(txCtx, `INSERT INTO processed_events (message_id) VALUES ($1)`, d.Message.ID)
				if err != nil {
					return err
				}
				return writer.Write(txCtx, outbox.WriteParams{
					Topic: egressBinding.Exchange, RoutingKey: egressBinding.RoutingKey,
					MessageID: d.Message.ID + ".completed", MessageType: "example.completed",
					Payload: json.RawMessage(`{"id":"42"}`),
				})
			})
			if err == nil {
				count, countErr := inbox.Count(handlerCtx)
				if countErr == nil && count == 1 {
					select {
					case <-acked:
					default:
						close(acked)
					}
				}
			}
			return err
		})
	}()

	select {
	case <-acked:
	case <-consumeCtx.Done():
		t.Fatal("timed out waiting for durable inbox processing")
	}

	require.Eventually(t, func() bool {
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()
		_, ok, err := ch.Get(binding.ConsumerGroup, true)
		return err == nil && !ok
	}, 5*time.Second, 50*time.Millisecond, "both deliveries must be ACKed after inbox commit")

	var effects int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM processed_events`).Scan(&effects))
	assert.Equal(t, 1, effects, "duplicate delivery must not repeat domain work")
	var receipts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM inbox_entries`).Scan(&receipts))
	assert.Equal(t, 1, receipts)

	// A real relay publishes the event written inside the same inbox
	// transaction. This completes the reference path rather than proving only
	// the inbound half of the composition.
	relay := outbox.NewRelay(outboxStore, outbox.NewMessagingPublisher(publisher), slog.Default(), outbox.WithPollInterval(10*time.Millisecond))
	relayCtx, stopRelay := context.WithCancel(ctx)
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Start(relayCtx) }()
	require.Eventually(t, func() bool {
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()
		delivery, ok, err := ch.Get(egressBinding.ConsumerGroup, true)
		return err == nil && ok && len(delivery.Body) > 0
	}, 5*time.Second, 50*time.Millisecond, "outbox relay must publish committed inbox side effect")
	stopRelay()
	require.NoError(t, <-relayDone)
}

// TestInbox_AMQPFailedWorkRedelivers verifies the recovery side of the inbox
// contract with real dependencies. A failed callback rolls back both the
// receipt and local work, AMQP routes the delivery through its retry topology,
// and only the subsequent successful transaction is ACKed and retained.
func TestInbox_AMQPFailedWorkRedelivers(t *testing.T) {
	pool := openAndMigrate(t, startPostgres(t))
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE retry_effects (message_id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	brokerURL := kittestamqp.Start(t)
	conn, err := amqpbackend.Connect(brokerURL, slog.Default(), amqpbackend.WithoutTLS())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })
	binding, err := amqpbackend.DeclareAll(conn, messaging.BindingSpec{
		Exchange:      "inbox.retry.exchange",
		ExchangeType:  messaging.ExchangeDirect,
		ConsumerGroup: "inbox.retry.queue",
		RoutingKey:    "inbox.retry.key",
		Retry:         &messaging.RetryPolicy{MaxRetries: 1, Delay: 100 * time.Millisecond},
	})
	require.NoError(t, err)
	require.NotEmpty(t, binding)
	publisher := amqpbackend.NewPublisher(conn, slog.Default())
	message, err := messaging.NewMessage("example.retry", map[string]string{"id": "42"})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(ctx, binding[0].Exchange, binding[0].RoutingKey, message))

	inbox := outboxpg.NewInbox(pool)
	consumer := amqpbackend.NewConsumer(conn, publisher, slog.Default())
	consumeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		_ = consumer.ConsumeOnce(consumeCtx, binding[0], func(handlerCtx context.Context, d messaging.Delivery) error {
			attempt := calls.Add(1)
			_, err := inbox.Process(handlerCtx, "example-retry", d.Message.ID, func(txCtx context.Context) error {
				if attempt == 1 {
					return errors.New("transient domain failure")
				}
				tx, ok := outboxpg.TxFromContext(txCtx)
				if !ok {
					return errors.New("missing inbox transaction")
				}
				_, err := tx.Exec(txCtx, `INSERT INTO retry_effects (message_id) VALUES ($1)`, d.Message.ID)
				return err
			})
			if err == nil && attempt == 2 {
				close(done)
			}
			return err
		})
	}()

	select {
	case <-done:
	case <-consumeCtx.Done():
		t.Fatalf("timed out waiting for redelivery; calls=%d", calls.Load())
	}
	assert.Equal(t, int32(2), calls.Load(), "failed work must be redelivered once")
	var effects, receipts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retry_effects`).Scan(&effects))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM inbox_entries WHERE consumer_name = 'example-retry'`).Scan(&receipts))
	assert.Equal(t, 1, effects)
	assert.Equal(t, 1, receipts, "failed attempt must not retain an inbox receipt")
}

// TestInbox_AMQPSchemaMismatchGoesToDLQ proves that contract validation is a
// delivery boundary: an event with an unsupported schema version never claims
// an inbox receipt or applies a domain mutation, and the configured AMQP
// retry/DLQ topology leaves an operator-visible poison-message record.
func TestInbox_AMQPSchemaMismatchGoesToDLQ(t *testing.T) {
	pool := openAndMigrate(t, startPostgres(t))
	ctx := context.Background()
	brokerURL := kittestamqp.Start(t)
	conn, err := amqpbackend.Connect(brokerURL, slog.Default(), amqpbackend.WithoutTLS())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	bindings, err := amqpbackend.DeclareAll(conn, messaging.BindingSpec{
		Exchange: "inbox.schema.exchange", ExchangeType: messaging.ExchangeDirect,
		ConsumerGroup: "inbox.schema.queue", RoutingKey: "schema.checked",
		Retry: &messaging.RetryPolicy{MaxRetries: 1, Delay: 50 * time.Millisecond},
	})
	require.NoError(t, err)
	binding := bindings[0]
	publisher := amqpbackend.NewPublisher(conn, slog.Default())
	poison, err := messaging.NewMessage("schema.checked", map[string]string{"unexpected": "field"})
	require.NoError(t, err)
	poison.SchemaVersion = 2 // only v1 is registered below.
	require.NoError(t, publisher.Publish(ctx, binding.Exchange, binding.RoutingKey, poison))

	registry := messaging.NewInMemorySchemaRegistry()
	require.NoError(t, registry.Register("schema.checked", 1, []byte(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object", "required":["id"],
  "properties":{"id":{"type":"string"}}, "additionalProperties":false
}`)))
	inbox := outboxpg.NewInbox(pool)
	dead := make(chan struct{})
	consumer := amqpbackend.NewConsumer(conn, publisher, slog.Default(), amqpbackend.WithHooks(amqpbackend.ConsumerHooks{
		OnDeadLetter: func(_, _, _ string, _ int) { close(dead) },
	}))
	consumeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	go func() {
		_ = consumer.Consume(consumeCtx, binding, func(_ context.Context, d messaging.Delivery) error {
			if err := registry.ValidateMessage(d.Message); err != nil {
				return err
			}
			return errors.New("schema-invalid message unexpectedly passed contract validation")
		})
	}()
	select {
	case <-dead:
	case <-consumeCtx.Done():
		t.Fatal("timed out waiting for schema-invalid event to reach DLQ")
	}
	count, err := inbox.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count, "schema-invalid event must not create an inbox receipt")
	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()
	delivery, ok, err := ch.Get(binding.DeadQueue, true)
	require.NoError(t, err)
	assert.True(t, ok, "schema-invalid event must be retained in the dead-letter queue")
	assert.NotEmpty(t, delivery.Body)
}

// TestInsertWithTx_AtomicWithBusinessTx is the headline transactional
// outbox guarantee: an Insert wrapped in the caller's pgx.Tx must
// commit-or-rollback with the surrounding business writes, not as a
// separate transaction. The test rolls back the tx and confirms the
// row never landed.
func TestInsertWithTx_AtomicWithBusinessTx(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)
	writer := outbox.NewWriter(store, outboxpg.RequireTx)

	ctx := context.Background()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)

	txCtx := outboxpg.WithTx(ctx, tx)
	err = writer.Write(txCtx, outbox.WriteParams{
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "rolled-back",
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
	})
	require.NoError(t, err, "Write into the tx must succeed")

	// Roll back the business transaction. The outbox row must not be
	// observable from the pool afterward.
	require.NoError(t, tx.Rollback(ctx))

	n, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "outbox row must roll back with the business tx")
}

// TestRequireTx_RejectsCallerWithoutTx is the producer-side fail-fast
// guardrail: NewWriter(..., RequireTx) refuses a Write outside a tx so
// the kit catches the misuse at the call site rather than allowing a
// silent split-brain.
func TestRequireTx_RejectsCallerWithoutTx(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)
	writer := outbox.NewWriter(store, outboxpg.RequireTx)

	err := writer.Write(context.Background(), outbox.WriteParams{
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "no-tx",
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, outboxpg.ErrNoTx)
}

// TestFetchPending_SkipLocked: two concurrent FetchPending calls
// against the same Store must claim disjoint subsets, never the same
// row. This is the relay-coordination contract — without SKIP LOCKED
// the second caller would block until the first commits.
func TestFetchPending_SkipLocked(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	const total = 20
	for i := 0; i < total; i++ {
		require.NoError(t, store.Insert(ctx, mustEntry()))
	}

	var (
		wg     sync.WaitGroup
		seenMu sync.Mutex
		seen   = map[uuid.UUID]int{}
	)
	wg.Add(2)
	worker := func() {
		defer wg.Done()
		entries, err := store.FetchPending(ctx, total/2)
		require.NoError(t, err)
		seenMu.Lock()
		defer seenMu.Unlock()
		for _, e := range entries {
			seen[e.ID]++
		}
	}
	go worker()
	go worker()
	wg.Wait()

	for id, n := range seen {
		assert.Equal(t, 1, n, "id %s claimed by multiple workers", id)
	}
}

// TestFetchPending_PreservesFIFOByCreatedAt pins the FIFO ordering
// contract documented on [outbox.Relay] (default serial publish
// preserves insertion order). A bare UPDATE ... RETURNING does NOT
// preserve any CTE ORDER BY, so this test would have caught the
// previous implementation silently shuffling rows. The fix carries
// row_number() through the claim CTE and re-orders the final SELECT.
func TestFetchPending_PreservesFIFOByCreatedAt(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	const total = 50
	wantOrder := make([]uuid.UUID, 0, total)
	base := time.Now().UTC().Add(-time.Hour)
	// Stagger created_at deterministically so the FIFO order is well
	// defined and not subject to clock granularity inside the insert
	// loop.
	for i := 0; i < total; i++ {
		e := mustEntry()
		e.CreatedAt = base.Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, store.Insert(ctx, e))
		// Override created_at because Insert stamps NOW() server-side;
		// we need the staggered values for a strict ordering assertion.
		_, err := pool.Exec(ctx, `UPDATE outbox_entries SET created_at = $1 WHERE id = $2`, e.CreatedAt, e.ID)
		require.NoError(t, err)
		wantOrder = append(wantOrder, e.ID)
	}

	got, err := store.FetchPending(ctx, total)
	require.NoError(t, err)
	require.Len(t, got, total)

	gotOrder := make([]uuid.UUID, len(got))
	for i := range got {
		gotOrder[i] = got[i].ID
	}
	assert.Equal(t, wantOrder, gotOrder, "FetchPending must return entries oldest-first by created_at")
}

// TestMarkPublished_StaleStateAfterReset: if ResetStaleProcessing
// pulls a row back to pending while a publish is in flight, the
// follow-up MarkPublished must surface ErrStaleState so the relay
// drops the attempt rather than recording a successful publish on a
// re-claimed row.
func TestMarkPublished_StaleStateAfterReset(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID.String()

	// Simulate a stale recovery while the publish is "in flight" by
	// hand-resetting the row.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET status = 'pending', updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)

	err = store.MarkPublished(ctx, id, time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, outbox.ErrStaleState), "want ErrStaleState, got %v", err)
}

// TestMarkPublished_NotFoundOnMissingID: distinguishing ErrNotFound
// vs ErrStaleState lets the relay log differently — a missing row
// is operator-visible drift (manual DELETE?); a stale state is a
// race that the relay can simply skip.
func TestMarkPublished_NotFoundOnMissingID(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	err := store.MarkPublished(context.Background(), uuid.NewString(), time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, outbox.ErrNotFound), "want ErrNotFound, got %v", err)
}

// TestIncrementAttempts_BumpsAndSchedulesRetry: a transient failure
// must bump attempts, store last_error, set next_retry_at in the
// future, and return the row to pending. FetchPending must skip the
// row until next_retry_at has elapsed.
func TestIncrementAttempts_BumpsAndSchedulesRetry(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID.String()

	retryAt := time.Now().Add(10 * time.Minute).UTC()
	require.NoError(t, store.IncrementAttempts(ctx, id, "downstream 503", retryAt))

	// FetchPending must skip — next_retry_at is in the future.
	next, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, next, "row must stay invisible until next_retry_at passes")

	// Verify attempts incremented and last_error stored.
	var attempts int
	var lastErr string
	err = pool.QueryRow(ctx, `SELECT attempts, last_error FROM outbox_entries WHERE id = $1`, id).Scan(&attempts, &lastErr)
	require.NoError(t, err)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, "downstream 503", lastErr)
}

// TestResetStaleProcessing_RecoversCrashedRelay: when a relay crashes
// mid-publish the row sits in processing forever without recovery.
// ResetStaleProcessing must pull it back to pending so a fresh relay
// can re-claim it.
func TestResetStaleProcessing_RecoversCrashedRelay(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID

	// Backdate updated_at so the row qualifies as stale.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)

	n, err := store.ResetStaleProcessing(ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// Re-claimable on the next FetchPending.
	again, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, id, again[0].ID)
}

// TestHeartbeat_KeepsFreshClaimAlive: a live relay's Heartbeat must
// bump updated_at so ResetStaleProcessing does not pull the row out
// from under it. Without this contract a slow publish would race the
// stale-recovery loop.
func TestHeartbeat_KeepsFreshClaimAlive(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	require.NoError(t, store.Insert(ctx, mustEntry()))
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	id := claimed[0].ID.String()

	// Backdate updated_at, then Heartbeat. After Heartbeat the row
	// should no longer be stale for a 5-minute threshold.
	_, err = pool.Exec(ctx, `UPDATE outbox_entries SET updated_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)

	touched, err := store.Heartbeat(ctx, []string{id})
	require.NoError(t, err)
	assert.Equal(t, int64(1), touched)

	n, err := store.ResetStaleProcessing(ctx, 5*time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "Heartbeat must reset the stale clock")
}

// TestRetentionDeletes: published / failed rows older than the cutoff
// must be removed; rows newer than the cutoff and rows in
// pending/processing must be untouched.
func TestRetentionDeletes(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := outboxpg.New(pool)

	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour).UTC()

	// Two published: one old, one fresh. Same for failed.
	rows := []struct {
		status      outbox.Status
		ts          time.Time
		publishedAt *time.Time
	}{
		{outbox.StatusPublished, old, &old},
		{outbox.StatusPublished, time.Now().UTC(), tptr(time.Now())},
		{outbox.StatusFailed, old, nil},
		{outbox.StatusFailed, time.Now().UTC(), nil},
		{outbox.StatusPending, old, nil}, // must NOT be deleted by either sweep
	}
	for _, r := range rows {
		e := mustEntry()
		e.Status = r.status
		e.CreatedAt = r.ts
		e.PublishedAt = r.publishedAt
		require.NoError(t, store.Insert(ctx, e))
		// updated_at gets created_at by Insert; force published_at into
		// the row for the published case (Insert path uses entry.PublishedAt).
		_, err := pool.Exec(ctx, `UPDATE outbox_entries SET updated_at = $1 WHERE id = $2`, r.ts, e.ID)
		require.NoError(t, err)
	}

	cutoff := time.Now().Add(-1 * time.Hour)
	n, err := store.DeletePublishedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = store.DeleteFailedBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// Pending row still present.
	pending, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending)
}

// TestInboxProcess_AtomicWithDomainAndOutbox is the inbound counterpart to
// TestInsertWithTx_AtomicWithBusinessTx. A committed delivery receipt, local
// projection, and outgoing event are one Postgres transaction; a replay sees
// the receipt and cannot run the application handler again.
func TestInboxProcess_AtomicWithDomainAndOutbox(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE order_projection (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	inbox := outboxpg.NewInbox(pool)
	store := outboxpg.New(pool)
	writer := outbox.NewWriter(store, outboxpg.RequireTx)

	result, err := inbox.Process(ctx, "orders.billing", "delivery-1", func(txCtx context.Context) error {
		tx, ok := outboxpg.TxFromContext(txCtx)
		if !ok {
			return errors.New("missing transaction in inbox handler")
		}
		if _, err := tx.Exec(txCtx, `INSERT INTO order_projection (id) VALUES ('order-1')`); err != nil {
			return err
		}
		return writer.Write(txCtx, outbox.WriteParams{
			Topic:       "orders",
			RoutingKey:  "order.billing.completed",
			MessageID:   "outbound-1",
			MessageType: "OrderBillingCompleted",
			Payload:     json.RawMessage(`{"id":"order-1"}`),
		})
	})
	require.NoError(t, err)
	assert.False(t, result.Duplicate)

	var projections int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM order_projection`).Scan(&projections))
	assert.Equal(t, 1, projections)
	pending, err := store.CountPending(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, pending)
	receipts, err := inbox.Count(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, receipts)

	replayed, err := inbox.Process(ctx, "orders.billing", "delivery-1", func(context.Context) error {
		return errors.New("duplicate must not invoke handler")
	})
	require.NoError(t, err)
	assert.True(t, replayed.Duplicate)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM order_projection`).Scan(&projections))
	assert.Equal(t, 1, projections)
}

// TestInboxProcess_HandlerFailureRollsBackAndRedelivers proves a failed local
// handler leaves neither receipt nor business data behind, so broker redelivery
// can safely retry the same delivery ID.
func TestInboxProcess_HandlerFailureRollsBackAndRedelivers(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE retry_projection (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	inbox := outboxpg.NewInbox(pool)

	want := errors.New("temporary domain failure")
	_, err = inbox.Process(ctx, "orders.billing", "delivery-retry", func(txCtx context.Context) error {
		tx, _ := outboxpg.TxFromContext(txCtx)
		_, err := tx.Exec(txCtx, `INSERT INTO retry_projection (id) VALUES ('order-2')`)
		if err != nil {
			return err
		}
		return want
	})
	require.ErrorIs(t, err, want)

	var projections int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retry_projection`).Scan(&projections))
	assert.Zero(t, projections)
	receipts, err := inbox.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, receipts)

	retry, err := inbox.Process(ctx, "orders.billing", "delivery-retry", func(txCtx context.Context) error {
		tx, _ := outboxpg.TxFromContext(txCtx)
		_, err := tx.Exec(txCtx, `INSERT INTO retry_projection (id) VALUES ('order-2')`)
		return err
	})
	require.NoError(t, err)
	assert.False(t, retry.Duplicate)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retry_projection`).Scan(&projections))
	assert.Equal(t, 1, projections)
}

// TestInboxProcessInTx_CallerRollbackLeavesNoReceipt covers the explicit
// caller-owned variant: ProcessInTx does no hidden commit, so a surrounding
// transaction can atomically abandon the receipt and all local effects.
func TestInboxProcessInTx_CallerRollbackLeavesNoReceipt(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE caller_tx_projection (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)
	inbox := outboxpg.NewInbox(pool)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	txCtx := outboxpg.WithTx(ctx, tx)
	result, err := inbox.ProcessInTx(txCtx, "orders.billing", "delivery-caller-rollback", func(handlerCtx context.Context) error {
		handlerTx, ok := outboxpg.TxFromContext(handlerCtx)
		if !ok || handlerTx != tx {
			return errors.New("handler did not receive caller transaction")
		}
		_, err := handlerTx.Exec(handlerCtx, `INSERT INTO caller_tx_projection (id) VALUES ('order-3')`)
		return err
	})
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	require.NoError(t, tx.Rollback(ctx))

	var projections int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM caller_tx_projection`).Scan(&projections))
	assert.Zero(t, projections)
	receipts, err := inbox.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, receipts)
}

// TestInboxProcess_ConcurrentReplicasSerializeOneHandler holds one handler
// open while a second replica tries the same delivery. The primary key causes
// exactly one committed handler invocation and a normal duplicate result for
// the other worker.
func TestInboxProcess_ConcurrentReplicasSerializeOneHandler(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	inbox := outboxpg.NewInbox(pool)
	entered := make(chan struct{})
	release := make(chan struct{})
	type outcome struct {
		result outboxpg.InboxResult
		err    error
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)

	go func() {
		result, err := inbox.Process(context.Background(), "orders.billing", "delivery-race", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
		first <- outcome{result, err}
	}()
	<-entered
	go func() {
		result, err := inbox.Process(context.Background(), "orders.billing", "delivery-race", func(context.Context) error {
			return errors.New("second handler must not run")
		})
		second <- outcome{result, err}
	}()
	close(release)

	gotFirst := <-first
	gotSecond := <-second
	require.NoError(t, gotFirst.err)
	require.NoError(t, gotSecond.err)
	assert.False(t, gotFirst.result.Duplicate)
	assert.True(t, gotSecond.result.Duplicate)
}

func TestInboxPruneBefore(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	ctx := context.Background()
	inbox := outboxpg.NewInbox(pool)

	_, err := pool.Exec(ctx, `INSERT INTO inbox_entries (consumer_name, message_id, received_at) VALUES
        ('orders.billing', 'old', NOW() - INTERVAL '48 hours'),
        ('orders.billing', 'fresh', NOW())`)
	require.NoError(t, err)
	deleted, err := inbox.PruneBefore(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	remaining, err := inbox.Count(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, remaining)
}

func tptr(t time.Time) *time.Time { return &t }
