package kafkabackend

import (
	"context"
	"errors"
	"testing"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// fakeCommitter records commit calls without touching a broker, so the
// per-record dispatch decision can be unit-tested.
type fakeCommitter struct {
	commits [][]kafka.Message
}

func (f *fakeCommitter) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	f.commits = append(f.commits, msgs)
	return nil
}

func validKafkaMessage(t *testing.T, topic string) kafka.Message {
	t.Helper()
	msg, err := messaging.NewMessage("event.test", map[string]string{"k": "v"})
	require.NoError(t, err)
	km, err := toKafkaMessage(topic, "", msg)
	require.NoError(t, err)
	return km
}

// TestDispatch_TransientErrorLeavesOffsetUncommitted guards the
// Kafka-watermark drop bug: a transient (non-permanent) handler error
// must NOT commit the offset AND must tell the Consume loop to reset
// the reader (retry=true), otherwise a later record on the same
// partition would commit past the failed one and silently drop it.
func TestDispatch_TransientErrorLeavesOffsetUncommitted(t *testing.T) {
	transient := errors.New("temporary downstream blip")
	permanent := apperror.NewPermanentWithCause("poison pill", errors.New("bad record"))

	tests := []struct {
		name        string
		handler     messaging.Handler
		message     func(t *testing.T) kafka.Message
		wantRetry   bool
		wantCommits int
	}{
		{
			name:        "success commits and does not retry",
			handler:     func(context.Context, messaging.Delivery) error { return nil },
			message:     func(t *testing.T) kafka.Message { return validKafkaMessage(t, "events") },
			wantRetry:   false,
			wantCommits: 1,
		},
		{
			name:        "transient error skips commit and signals retry",
			handler:     func(context.Context, messaging.Delivery) error { return transient },
			message:     func(t *testing.T) kafka.Message { return validKafkaMessage(t, "events") },
			wantRetry:   true,
			wantCommits: 0,
		},
		{
			name:        "permanent error commits poison pill and does not retry",
			handler:     func(context.Context, messaging.Delivery) error { return permanent },
			message:     func(t *testing.T) kafka.Message { return validKafkaMessage(t, "events") },
			wantRetry:   false,
			wantCommits: 1,
		},
		{
			name:    "malformed body commits to skip and does not retry",
			handler: func(context.Context, messaging.Delivery) error { return transient },
			message: func(t *testing.T) kafka.Message {
				return kafka.Message{Topic: "events", Value: []byte("not-json")}
			},
			wantRetry:   false,
			wantCommits: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub := mustSubscriber(t)
			fc := &fakeCommitter{}
			retry := sub.dispatch(context.Background(), fc, tc.message(t), tc.handler)
			assert.Equal(t, tc.wantRetry, retry, "retry signal")
			assert.Len(t, fc.commits, tc.wantCommits, "commit count")
		})
	}
}
