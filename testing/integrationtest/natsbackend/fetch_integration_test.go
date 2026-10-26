//go:build integration

package natsbackend_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	natsmodule "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
)

// nats:2.11-alpine, pinned so the pull protocol under test cannot move.
const pinnedNATS = "nats:2.11-alpine@sha256:e4bf19f15fd3218814a4e3c9e0064e1334bd8aa20d5984b9f1a0afd084f8cc00"

func pullFixture(t *testing.T) (jetstream.JetStream, func(n, size int)) {
	t.Helper()
	ctx := context.Background()
	c, err := natsmodule.Run(ctx, pinnedNATS)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	url, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"events.>"}, Storage: jetstream.MemoryStorage})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{Durable: "lane", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute})
	require.NoError(t, err)
	publish := func(n, size int) {
		for range n {
			_, err := js.Publish(ctx, "events.a.b", bytes.Repeat([]byte("x"), size))
			require.NoError(t, err)
		}
	}
	return js, publish
}

func TestFetch_CapsMessagesAndBytesTogether(t *testing.T) {
	js, publish := pullFixture(t)
	publish(10, 1000)
	ctx := context.Background()

	msgs, err := natsbackend.Fetch(ctx, js, "EVENTS", "lane", natsbackend.PullRequest{MaxMessages: 4, MaxBytes: 1 << 20, Expires: time.Second})
	require.NoError(t, err)
	assert.Len(t, msgs, 4, "the message cap")

	msgs2, err := natsbackend.Fetch(ctx, js, "EVENTS", "lane", natsbackend.PullRequest{MaxMessages: 100, MaxBytes: 2500, Expires: time.Second})
	require.NoError(t, err)
	assert.Len(t, msgs2, 2, "the byte cap")

	for _, m := range append(msgs, msgs2...) {
		md, err := m.Metadata()
		require.NoError(t, err)
		assert.Equal(t, "lane", md.Consumer)
		require.NoError(t, m.Ack())
	}
	consumer, err := js.Consumer(ctx, "EVENTS", "lane")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, err := consumer.Info(ctx)
		return err == nil && info.NumAckPending == 0 && info.NumPending == 4
	}, 5*time.Second, 50*time.Millisecond, "acked messages leave the consumer")
}

func TestFetch_NakWithDelayAndTermRedeliverAsAsked(t *testing.T) {
	js, publish := pullFixture(t)
	publish(2, 10)
	ctx := context.Background()
	pull := natsbackend.PullRequest{MaxMessages: 10, Expires: 500 * time.Millisecond}
	msgs, err := natsbackend.Fetch(ctx, js, "EVENTS", "lane", pull)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.NoError(t, msgs[0].NakWithDelay(200*time.Millisecond))
	require.NoError(t, msgs[1].Term())
	time.Sleep(300 * time.Millisecond)
	again, err := natsbackend.Fetch(ctx, js, "EVENTS", "lane", pull)
	require.NoError(t, err)
	require.Len(t, again, 1)
	md, err := again[0].Metadata()
	require.NoError(t, err)
	assert.Equal(t, uint64(2), md.NumDelivered)
}

func TestFetch_EndsQuietlyWhenNothingArrives(t *testing.T) {
	js, _ := pullFixture(t)
	start := time.Now()
	msgs, err := natsbackend.Fetch(context.Background(), js, "EVENTS", "lane", natsbackend.PullRequest{MaxMessages: 5, Expires: 3 * time.Second, Heartbeat: 500 * time.Millisecond})
	require.NoError(t, err, "heartbeats keep the pull alive")
	assert.Empty(t, msgs)
	assert.GreaterOrEqual(t, time.Since(start), 2900*time.Millisecond)
}

func TestFetch_ReportsWhatEndsAPull(t *testing.T) {
	js, publish := pullFixture(t)
	ctx := context.Background()

	_, err := natsbackend.Fetch(ctx, js, "EVENTS", "missing", natsbackend.PullRequest{MaxMessages: 1, Expires: time.Second})
	assert.ErrorIs(t, err, nats.ErrNoResponders, "a missing consumer")

	publish(1, 5000)
	_, err = natsbackend.Fetch(ctx, js, "EVENTS", "lane", natsbackend.PullRequest{MaxMessages: 1, MaxBytes: 1000, Expires: time.Second})
	assert.ErrorIs(t, err, natsbackend.ErrMessageTooLarge, "a message beyond the byte cap")

	cancelled, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, err = natsbackend.Fetch(cancelled, js, "EVENTS", "lane", natsbackend.PullRequest{MaxMessages: 1, MaxBytes: 1000, Expires: 5 * time.Second})
	if err == nil {
		t.Fatal("a pull outlived its context")
	}

	deleted := make(chan error, 1)
	_, err = js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{Durable: "gone", AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: "events.none"})
	require.NoError(t, err)
	go func() {
		_, err := natsbackend.Fetch(ctx, js, "EVENTS", "gone", natsbackend.PullRequest{MaxMessages: 1, Expires: 5 * time.Second})
		deleted <- err
	}()
	time.Sleep(300 * time.Millisecond)
	require.NoError(t, js.DeleteConsumer(ctx, "EVENTS", "gone"))
	select {
	case err := <-deleted:
		assert.ErrorIs(t, err, jetstream.ErrConsumerDeleted)
	case <-time.After(5 * time.Second):
		t.Fatal("a deleted consumer kept the pull open")
	}
}
