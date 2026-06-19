package kafkabackend

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// TestSubscriber_Consume_RejectsRetryBinding guards the wave-141 hard
// refusal: a Binding carrying a non-nil Retry policy must be rejected at
// Consume entry with messaging.ErrRetryUnsupported, before any Reader is
// constructed (so no broker connection is attempted). Kafka has no
// per-message delayed-redelivery primitive that maps to RetryPolicy, so
// the operator must explicitly opt into ack-and-discard or wrap the
// handler in resilience/retry.
func TestSubscriber_Consume_RejectsRetryBinding(t *testing.T) {
	sub := mustSubscriber(t)
	err := sub.Consume(context.Background(), messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange: "events",
			Retry:    &messaging.RetryPolicy{MaxRetries: 3, Delay: 0},
		},
	}, func(context.Context, messaging.Delivery) error { return nil })
	require.ErrorIs(t, err, messaging.ErrRetryUnsupported)
}

// TestDispatch_HandlerPanicCommitsToSkip guards the poison-pill recovery
// path: a panicking handler must not propagate the panic out of dispatch
// and must commit the offset so the consumer makes forward progress
// instead of redelivering the same record forever.
func TestDispatch_HandlerPanicCommitsToSkip(t *testing.T) {
	sub := mustSubscriber(t)
	fc := &fakeCommitter{}

	var retry bool
	assert.NotPanics(t, func() {
		retry = sub.dispatch(context.Background(), fc, validKafkaMessage(t, "events"),
			func(context.Context, messaging.Delivery) error { panic("boom") })
	})

	assert.False(t, retry, "a panic is a poison pill, not a transient retry")
	assert.Len(t, fc.commits, 1, "panic must commit the offset to skip the poison pill")
}

// TestDispatch_ValidationFailureCommitsToSkip guards that a record whose
// body decodes into a Message but fails messaging.ValidateMessage (here
// an id with a control/whitespace character) is committed-to-skip rather
// than handed to the handler or left for redelivery.
func TestDispatch_ValidationFailureCommitsToSkip(t *testing.T) {
	sub := mustSubscriber(t)
	fc := &fakeCommitter{}

	// Decodes fine as JSON / messaging.Message, but the id contains a
	// space so ValidateMessage rejects it.
	body, err := json.Marshal(messaging.Message{ID: "bad id", Type: "event.test"})
	require.NoError(t, err)
	km := kafka.Message{Topic: "events", Value: body}

	handlerCalled := false
	retry := sub.dispatch(context.Background(), fc, km, func(context.Context, messaging.Delivery) error {
		handlerCalled = true
		return nil
	})

	assert.False(t, retry, "validation failure is skip-and-ack, not retry")
	assert.False(t, handlerCalled, "handler must not run for a record that fails validation")
	assert.Len(t, fc.commits, 1, "validation failure must commit the offset to skip")
}

// errCommitter fails every commit so the commitWithOutcome failure
// branch can be exercised without a live broker.
type errCommitter struct {
	calls int
}

func (e *errCommitter) CommitMessages(context.Context, ...kafka.Message) error {
	e.calls++
	return errors.New("broker rejected commit")
}

// TestDispatch_CommitFailureIsSwallowed guards commitWithOutcome's
// failure branch: when CommitMessages errors, dispatch must not panic
// and must not turn a successful handler into a transient retry (the
// commit-failed outcome is observed via metrics, but dispatch still
// reports retry=false for a successful handler).
func TestDispatch_CommitFailureIsSwallowed(t *testing.T) {
	sub := mustSubscriber(t)
	ec := &errCommitter{}

	retry := sub.dispatch(context.Background(), ec, validKafkaMessage(t, "events"),
		func(context.Context, messaging.Delivery) error { return nil })

	assert.False(t, retry, "a commit failure on a successful handler must not signal retry")
	assert.Equal(t, 1, ec.calls, "dispatch must attempt exactly one commit on success")
}
