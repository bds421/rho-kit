//go:build integration

package outboxpg

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	outboxpg "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

type publishedTo chan outbox.Entry

func (p publishedTo) Publish(_ context.Context, e outbox.Entry) error {
	p <- e
	return nil
}

// A relay in one process publishes an entry another process commits as
// soon as the commit's notification arrives, long before its poll.
func TestNotify_WakesARelayInAnotherProcess(t *testing.T) {
	dsn := startPostgres(t)
	writerPool := openAndMigrate(t, dsn)
	relayPool, err := pgxpool.New(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(relayPool.Close)

	published := make(publishedTo, 1)
	relay := outbox.NewRelay(outboxpg.New(relayPool), published, slog.New(slog.DiscardHandler), outbox.WithPollInterval(time.Hour))
	listening := make(chan struct{}, 1)
	wake := func() {
		select {
		case listening <- struct{}{}:
		default:
		}
		relay.Wake()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 2)
	go func() { done <- relay.Start(ctx) }()
	go func() {
		done <- outboxpg.Listen(ctx, relayPool, "kit_outbox", wake, outboxpg.WithListenRetry(10*time.Millisecond))
	}()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
		require.NoError(t, <-done)
	})
	<-listening // LISTEN is in place

	writer := outboxpg.NewWithOptions(writerPool, outboxpg.WithNotify("kit_outbox"))
	tx, err := writerPool.Begin(t.Context())
	require.NoError(t, err)
	entry := mustEntry()
	require.NoError(t, writer.Insert(outboxpg.WithTx(t.Context(), tx), entry))
	require.NoError(t, tx.Commit(t.Context()))
	select {
	case got := <-published:
		require.Equal(t, entry.ID, got.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("the relay did not hear of the commit")
	}
}
