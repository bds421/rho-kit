package amqpbackend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// fakeAcknowledger records Ack/Nack calls for test assertions.
type fakeAcknowledger struct {
	acked    bool
	nacked   bool
	requeued bool
	ackErr   error
	nackErr  error
}

func (a *fakeAcknowledger) Ack(tag uint64, multiple bool) error {
	a.acked = true
	return a.ackErr
}

func (a *fakeAcknowledger) Nack(tag uint64, multiple bool, requeue bool) error {
	a.nacked = true
	a.requeued = requeue
	return a.nackErr
}

func (a *fakeAcknowledger) Reject(tag uint64, requeue bool) error {
	return nil
}

// fakeDeadLetterPublisher records PublishRaw calls.
type fakeDeadLetterPublisher struct {
	called bool
	err    error
}

func (f *fakeDeadLetterPublisher) PublishRaw(_ context.Context, exchange, routingKey string, body []byte, msgID string) error {
	f.called = true
	return f.err
}

type panicDeadLetterPublisher struct{}

func (panicDeadLetterPublisher) PublishRaw(context.Context, string, string, []byte, string) error {
	panic("dead-letter publisher exploded")
}

func newTestConsumer(dlPublisher DeadLetterPublisher, hooks ConsumerHooks) *Consumer {
	return &Consumer{
		logger:    discardLogger(),
		publisher: dlPublisher,
		prefetch:  defaultPrefetch,
		hooks:     hooks,
	}
}

func makeAMQPDelivery(ack *fakeAcknowledger, msg messaging.Message) amqp.Delivery {
	body, _ := json.Marshal(msg)
	return amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Exchange:     "test-exchange",
		RoutingKey:   msg.Type,
	}
}

// --- unmarshal ---

func TestUnmarshal_ValidMessage(t *testing.T) {
	msg, _ := messaging.NewMessage("test.event", map[string]string{"key": "val"})
	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: &fakeAcknowledger{},
		Body:         body,
	}

	result, err := unmarshal(delivery)

	require.NoError(t, err)
	assert.Equal(t, msg.ID, result.ID)
	assert.Equal(t, msg.Type, result.Type)
}

func TestUnmarshal_InvalidJSON_ReturnsError(t *testing.T) {
	ack := &fakeAcknowledger{}
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         []byte(`not valid json`),
	}

	_, err := unmarshal(delivery)

	require.Error(t, err)
	assert.False(t, ack.acked, "unmarshal is a pure parse — it must not ACK")
}

func TestHandleDelivery_UnmarshalFailure_AcksAndDiscards(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discarded bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(_, _, _ string) { discarded = true },
	})
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         []byte(`invalid`),
	}
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	handler := func(_ context.Context, _ messaging.Delivery) error { return nil }
	c.handleDelivery(context.Background(), delivery, handler, binding)

	assert.True(t, ack.acked, "malformed messages should be acked by handleDelivery")
	assert.True(t, discarded, "malformed messages should fire OnDiscard hook")
}

// --- handleDelivery ---

func TestConsumer_HandleDelivery_Success_AcksMessage(t *testing.T) {
	ack := &fakeAcknowledger{}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	handler := func(_ context.Context, _ messaging.Delivery) error { return nil }
	c.handleDelivery(context.Background(), delivery, handler, binding)

	assert.True(t, ack.acked)
	assert.False(t, ack.nacked)
}

func TestConsumer_HandleDelivery_HandlerError_CallsHandleFailure(t *testing.T) {
	ack := &fakeAcknowledger{}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	handler := func(_ context.Context, _ messaging.Delivery) error {
		return errors.New("processing failed")
	}
	c.handleDelivery(context.Background(), delivery, handler, binding)

	// With no retry configured, the failure action is actionDiscard which acks
	// to defensively prevent unexpected routing if a DLX is manually added.
	assert.True(t, ack.acked)
	assert.False(t, ack.nacked)
}

// TestConsumer_HandleDelivery_ForeignProducerInvalidMessage_AcksAndDiscards
// pins H-002: a publisher bypassing kit validation can send a message
// with metadata that fails messaging.ValidateMessage (oversized ID,
// invalid header chars, etc.). The consumer must ack-discard before
// reaching the handler — otherwise handlers see metadata the contract
// says cannot exist.
func TestConsumer_HandleDelivery_ForeignProducerInvalidMessage_AcksAndDiscards(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discardCalled bool
	var handlerCalled bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(_, _, queue string) {
			discardCalled = true
			assert.Equal(t, "test-queue", queue)
		},
	})
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	// Craft a wire-level message that JSON-decodes cleanly but has an
	// oversized message Type (foreign producer bypassing kit
	// publisher validation). The kit publisher would have rejected
	// this at publish time.
	oversizedType := strings.Repeat("a", messaging.MaxMessageTypeBytes+1)
	rawMsg := messaging.Message{
		ID:      "id-123",
		Type:    oversizedType,
		Payload: json.RawMessage(`{}`),
	}
	body, _ := json.Marshal(rawMsg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Exchange:     "test-exchange",
		RoutingKey:   "ignored",
	}
	handler := func(_ context.Context, _ messaging.Delivery) error {
		handlerCalled = true
		return nil
	}

	c.handleDelivery(context.Background(), delivery, handler, binding)

	assert.True(t, ack.acked, "validation failure must ack-discard")
	assert.False(t, ack.nacked, "validation failure must not nack — never validates, would loop")
	assert.True(t, discardCalled, "OnDiscard hook must fire for invalid inbound message")
	assert.False(t, handlerCalled, "handler must NOT see a message that fails ValidateMessage")
}

func TestConsumer_HandleDelivery_UnmarshalError_DiscardHookCalled(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discardCalled bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(msgID, msgType, queue string) {
			discardCalled = true
			assert.Equal(t, "test-queue", queue)
		},
	})
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         []byte(`bad json`),
	}
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	handler := func(_ context.Context, _ messaging.Delivery) error { return nil }
	c.handleDelivery(context.Background(), delivery, handler, binding)

	assert.True(t, discardCalled)
}

// --- handleFailure ---

func TestConsumer_HandleFailure_PermanentError_AcksAndDiscards(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discardCalled bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(msgID, msgType, queue string) {
			discardCalled = true
		},
	})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	// No DeadExchange configured — fall back to ack-discard.
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	c.handleFailure(context.Background(), delivery, msg, binding, apperror.NewPermanent("bad data"))

	assert.True(t, ack.acked, "permanent errors should be acked when no DLE configured")
	assert.True(t, discardCalled)
}

// FR-071 [HIGH] regression: when a DeadExchange is configured a
// permanent handler error must be routed to the DLE, not silently
// discarded. Pre-fix the consumer always ack-discarded permanent
// errors regardless of binding configuration, so poison messages
// vanished without a broker-visible audit trail.
func TestConsumer_HandleFailure_PermanentError_DeadLettersWhenDLEConfigured(t *testing.T) {
	ack := &fakeAcknowledger{}
	dlPub := &fakeDeadLetterPublisher{}
	var deadLetterCalled bool
	var discardCalled bool
	c := newTestConsumer(dlPub, ConsumerHooks{
		OnDeadLetter: func(_, _, _ string, _ int) { deadLetterCalled = true },
		OnDiscard:    func(_, _, _ string) { discardCalled = true },
	})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{
		BindingSpec:  messaging.BindingSpec{ConsumerGroup: "test-queue", RoutingKey: "test.event"},
		DeadExchange: "test-exchange.dead",
	}

	c.handleFailure(context.Background(), delivery, msg, binding, apperror.NewPermanent("bad data"))

	assert.True(t, dlPub.called, "permanent error must publish to dead exchange when configured (FR-071)")
	assert.True(t, ack.acked, "delivery must be acked after successful DLE publish")
	assert.True(t, deadLetterCalled, "OnDeadLetter hook must fire for permanent-error DLE routing")
	assert.False(t, discardCalled, "OnDiscard must NOT fire when message is dead-lettered")
}

func TestConsumer_HandleFailure_NoRetryConfig_Discards(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discardCalled bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(_, _, _ string) { discardCalled = true },
	})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("transient error"))

	assert.True(t, ack.acked, "no retry config should ack to prevent unexpected DLX routing")
	assert.True(t, discardCalled)
}

func TestConsumer_HandleFailure_Retry_Nacks(t *testing.T) {
	ack := &fakeAcknowledger{}
	var retryCalled bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnRetry: func(_, _, _ string, _ int) { retryCalled = true },
	})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("transient"))

	assert.True(t, ack.nacked)
	assert.True(t, retryCalled)
}

func TestConsumer_HandleFailure_RetryHookPanic_DoesNotPanic(t *testing.T) {
	ack := &fakeAcknowledger{}
	c := newTestConsumer(nil, ConsumerHooks{
		OnRetry: func(_, _, _ string, _ int) {
			panic("hook exploded")
		},
	})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
	}

	assert.NotPanics(t, func() {
		c.handleFailure(context.Background(), delivery, msg, binding, errors.New("transient"))
	})
	assert.True(t, ack.nacked, "hook panic must not prevent retry nack")
}

func TestConsumer_HandleFailure_DeadLetter_PublishesAndAcks(t *testing.T) {
	ack := &fakeAcknowledger{}
	dlPub := &fakeDeadLetterPublisher{}
	var deadLetterCalled bool
	c := newTestConsumer(dlPub, ConsumerHooks{
		OnDeadLetter: func(_, _, _ string, _ int) { deadLetterCalled = true },
	})
	msg, _ := messaging.NewMessage("test.event", "payload")

	// Create delivery with x-death headers showing max retries exceeded.
	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Exchange:     "test-exchange",
		RoutingKey:   "test.event",
		Headers: amqp.Table{
			"x-death": []any{
				amqp.Table{
					"queue":  "test-queue",
					"reason": "rejected",
					"count":  int64(3),
				},
			},
		},
	}

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
		DeadExchange: "test-exchange.dead",
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("too many retries"))

	assert.True(t, dlPub.called, "should publish to dead-letter exchange")
	assert.True(t, ack.acked, "should ack after dead-letter publish")
	assert.True(t, deadLetterCalled)
}

func TestConsumer_HandleFailure_DeadLetter_PublishFails_Nacks(t *testing.T) {
	ack := &fakeAcknowledger{}
	dlPub := &fakeDeadLetterPublisher{err: errors.New("publish failed")}
	c := newTestConsumer(dlPub, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")

	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Exchange:     "test-exchange",
		RoutingKey:   "test.event",
		Headers: amqp.Table{
			"x-death": []any{
				amqp.Table{
					"queue":  "test-queue",
					"reason": "rejected",
					"count":  int64(3),
				},
			},
		},
	}

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
		DeadExchange: "test-exchange.dead",
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("too many retries"))

	assert.True(t, dlPub.called)
	assert.True(t, ack.nacked, "should nack when dead-letter publish fails")
	assert.False(t, ack.acked)
}

func TestConsumer_HandleFailure_DeadLetterPublisherPanic_Nacks(t *testing.T) {
	ack := &fakeAcknowledger{}
	c := newTestConsumer(panicDeadLetterPublisher{}, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")

	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Exchange:     "test-exchange",
		RoutingKey:   "test.event",
		Headers: amqp.Table{
			"x-death": []any{
				amqp.Table{
					"queue":  "test-queue",
					"reason": "rejected",
					"count":  int64(3),
				},
			},
		},
	}
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
		DeadExchange: "test-exchange.dead",
	}

	assert.NotPanics(t, func() {
		c.handleFailure(context.Background(), delivery, msg, binding, errors.New("too many retries"))
	})
	assert.True(t, ack.nacked, "dead-letter publisher panic should follow publish-failure path")
	assert.False(t, ack.acked)
}

func TestConsumer_HandleFailure_ForceDiscard_AcksAndDiscards(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discardCalled bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(_, _, _ string) { discardCalled = true },
	})
	msg, _ := messaging.NewMessage("test.event", "payload")

	// Create x-death count exceeding safetyMaxBounceMultiplier * MaxRetries.
	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Exchange:     "test-exchange",
		RoutingKey:   "test.event",
		Headers: amqp.Table{
			"x-death": []any{
				amqp.Table{
					"queue":  "test-queue",
					"reason": "rejected",
					"count":  int64(10), // 3 * 3 = 9, so 10 exceeds safety limit
				},
			},
		},
	}

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("stuck"))

	assert.True(t, ack.acked, "force discard should ack")
	assert.True(t, discardCalled)
}

// --- handleDelivery edge cases ---

func TestConsumer_HandleDelivery_AckFailure_DoesNotPanic(t *testing.T) {
	ack := &fakeAcknowledger{ackErr: errors.New("ack broken")}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	handler := func(_ context.Context, _ messaging.Delivery) error { return nil }
	c.handleDelivery(context.Background(), delivery, handler, binding)

	assert.True(t, ack.acked, "ack should be attempted even if it fails")
}

// --- handleFailure edge cases ---

func TestConsumer_HandleFailure_PermanentError_AckFailure(t *testing.T) {
	ack := &fakeAcknowledger{ackErr: errors.New("ack broken")}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	c.handleFailure(context.Background(), delivery, msg, binding, apperror.NewPermanent("bad"))
	assert.True(t, ack.acked)
}

func TestConsumer_HandleFailure_Retry_NackFailure(t *testing.T) {
	ack := &fakeAcknowledger{nackErr: errors.New("nack broken")}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue", RoutingKey: "test.event", Retry: &messaging.RetryPolicy{MaxRetries: 3}},
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("transient"))
	assert.True(t, ack.nacked)
}

func TestConsumer_HandleFailure_Discard_AckFailure(t *testing.T) {
	ack := &fakeAcknowledger{ackErr: errors.New("ack broken")}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("transient"))
	assert.True(t, ack.acked)
}

func TestConsumer_HandleFailure_ForceDiscard_AckFailure(t *testing.T) {
	ack := &fakeAcknowledger{ackErr: errors.New("ack broken")}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")

	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Headers: amqp.Table{
			"x-death": []any{amqp.Table{
				"queue": "test-queue", "reason": "rejected", "count": int64(10),
			}},
		},
	}
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue", RoutingKey: "test.event", Retry: &messaging.RetryPolicy{MaxRetries: 3}},
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("stuck"))
	assert.True(t, ack.acked)
}

func TestConsumer_HandleFailure_DeadLetter_PublishFails_NackFailure(t *testing.T) {
	ack := &fakeAcknowledger{nackErr: errors.New("nack broken")}
	dlPub := &fakeDeadLetterPublisher{err: errors.New("dl publish failed")}
	c := newTestConsumer(dlPub, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")

	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Headers: amqp.Table{
			"x-death": []any{amqp.Table{
				"queue": "test-queue", "reason": "rejected", "count": int64(3),
			}},
		},
	}
	binding := messaging.Binding{
		BindingSpec:  messaging.BindingSpec{ConsumerGroup: "test-queue", RoutingKey: "test.event", Retry: &messaging.RetryPolicy{MaxRetries: 3}},
		DeadExchange: "test-exchange.dead",
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("too many retries"))
	assert.True(t, ack.nacked)
}

func TestConsumer_HandleFailure_DeadLetter_AckFailure(t *testing.T) {
	ack := &fakeAcknowledger{ackErr: errors.New("ack broken")}
	dlPub := &fakeDeadLetterPublisher{}
	c := newTestConsumer(dlPub, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")

	body, _ := json.Marshal(msg)
	delivery := amqp.Delivery{
		Acknowledger: ack,
		Body:         body,
		Headers: amqp.Table{
			"x-death": []any{amqp.Table{
				"queue": "test-queue", "reason": "rejected", "count": int64(3),
			}},
		},
	}
	binding := messaging.Binding{
		BindingSpec:  messaging.BindingSpec{ConsumerGroup: "test-queue", RoutingKey: "test.event", Retry: &messaging.RetryPolicy{MaxRetries: 3}},
		DeadExchange: "test-exchange.dead",
	}

	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("too many retries"))
	assert.True(t, dlPub.called)
	assert.True(t, ack.acked, "ack attempted even when it fails")
}

// --- DLQ consecutive-failure force-discard cap (consumer.go routeToDeadExchange) ---

// togglingDeadLetterPublisher returns nextErr on each PublishRaw call so a
// test can drive a streak of failures and then a success to exercise the
// consecutive-failure cap and its reset.
type togglingDeadLetterPublisher struct {
	nextErr error
	calls   int
}

func (p *togglingDeadLetterPublisher) PublishRaw(context.Context, string, string, []byte, string) error {
	p.calls++
	return p.nextErr
}

func dleBinding() messaging.Binding {
	return messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
		DeadExchange: "test-exchange.dead",
	}
}

// TestRouteToDeadExchange_ForceDiscardsAfterCapCrossed pins the loss-inducing
// shedding path: with a small cap, the first `cap` consecutive DLE publish
// failures nack-to-retry (dlq_publish_failed); the failure that crosses the
// cap acks+OnDiscard and reports force_discarded. Before, newTestConsumer
// left the cap at 0 (uncapped) so this branch was never exercised.
func TestRouteToDeadExchange_ForceDiscardsAfterCapCrossed(t *testing.T) {
	const cap = 2
	dlPub := &togglingDeadLetterPublisher{nextErr: errors.New("dead exchange is broken")}
	var discards int
	c := newTestConsumer(dlPub, ConsumerHooks{
		OnDiscard: func(_, _, queue string) {
			discards++
			assert.Equal(t, "test-queue", queue)
		},
	})
	c.maxDLQConsecutiveFail = cap
	msg, _ := messaging.NewMessage("test.event", "payload")
	binding := dleBinding()

	// First `cap` failures: nack-to-retry, counter climbs but stays at/under cap.
	for i := 1; i <= cap; i++ {
		ack := &fakeAcknowledger{}
		delivery := makeAMQPDelivery(ack, msg)
		outcome := c.routeToDeadExchange(context.Background(), delivery, msg, binding, errors.New("boom"), 0)
		assert.Equal(t, amqpConsumeOutcomeDLQPublishFailed, outcome,
			"failure %d is at/under the cap and must nack-to-retry", i)
		assert.True(t, ack.nacked, "under-cap DLE failure must nack")
		assert.False(t, ack.acked, "under-cap DLE failure must not ack")
	}
	assert.Equal(t, 0, discards, "no force-discard before the cap is crossed")

	// The failure that crosses the cap: ack + OnDiscard + force_discarded.
	ack := &fakeAcknowledger{}
	delivery := makeAMQPDelivery(ack, msg)
	outcome := c.routeToDeadExchange(context.Background(), delivery, msg, binding, errors.New("boom"), 0)
	assert.Equal(t, amqpConsumeOutcomeForceDiscarded, outcome,
		"crossing the cap must force-discard to break the loop")
	assert.True(t, ack.acked, "force-discard must ack (nack would re-enter the retry cycle forever)")
	assert.False(t, ack.nacked, "force-discard must not nack")
	assert.Equal(t, 1, discards, "force-discard must fire OnDiscard exactly once")
}

// TestRouteToDeadExchange_SuccessResetsConsecutiveFailureCounter pins the
// reset-on-success branch (the per-exchange counter is Store(0)'d): a
// successful publish between failures must clear the streak so a later
// transient failure is nacked-to-retry, not force-discarded.
func TestRouteToDeadExchange_SuccessResetsConsecutiveFailureCounter(t *testing.T) {
	const cap = 2
	dlPub := &togglingDeadLetterPublisher{}
	var deadLettered int
	var discards int
	c := newTestConsumer(dlPub, ConsumerHooks{
		OnDeadLetter: func(_, _, _ string, _ int) { deadLettered++ },
		OnDiscard:    func(_, _, _ string) { discards++ },
	})
	c.maxDLQConsecutiveFail = cap
	msg, _ := messaging.NewMessage("test.event", "payload")
	binding := dleBinding()

	route := func() string {
		ack := &fakeAcknowledger{}
		delivery := makeAMQPDelivery(ack, msg)
		return c.routeToDeadExchange(context.Background(), delivery, msg, binding, errors.New("boom"), 0)
	}

	// Build the streak right up to the cap (still nack-to-retry).
	dlPub.nextErr = errors.New("dead exchange flaky")
	for i := 1; i <= cap; i++ {
		require.Equal(t, amqpConsumeOutcomeDLQPublishFailed, route(),
			"failure %d must nack-to-retry", i)
	}

	// A success resets the counter.
	dlPub.nextErr = nil
	require.Equal(t, amqpConsumeOutcomeDeadLettered, route(),
		"successful publish must dead-letter")
	assert.Equal(t, 1, deadLettered)

	// Because the counter reset, the next `cap` failures must nack-to-retry
	// again rather than immediately force-discard.
	dlPub.nextErr = errors.New("dead exchange flaky again")
	for i := 1; i <= cap; i++ {
		require.Equal(t, amqpConsumeOutcomeDLQPublishFailed, route(),
			"post-reset failure %d must nack-to-retry, proving the streak was cleared", i)
	}
	assert.Equal(t, 0, discards, "no force-discard while the streak stays at/under the cap")
}

// TestRouteToDeadExchange_Uncapped_NeverForceDiscards pins the
// WithoutMaxDLQConsecutiveFailures semantics: cap == 0 disables the shedding
// branch so even a long failure streak only ever nacks-to-retry.
func TestRouteToDeadExchange_Uncapped_NeverForceDiscards(t *testing.T) {
	dlPub := &togglingDeadLetterPublisher{nextErr: errors.New("permanently broken")}
	var discards int
	c := newTestConsumer(dlPub, ConsumerHooks{
		OnDiscard: func(_, _, _ string) { discards++ },
	})
	c.maxDLQConsecutiveFail = 0 // uncapped (WithoutMaxDLQConsecutiveFailures)
	msg, _ := messaging.NewMessage("test.event", "payload")
	binding := dleBinding()

	for i := 0; i < 50; i++ {
		ack := &fakeAcknowledger{}
		delivery := makeAMQPDelivery(ack, msg)
		outcome := c.routeToDeadExchange(context.Background(), delivery, msg, binding, errors.New("boom"), 0)
		require.Equal(t, amqpConsumeOutcomeDLQPublishFailed, outcome,
			"uncapped consumer must always nack-to-retry, never force-discard")
		require.True(t, ack.nacked)
	}
	assert.Equal(t, 0, discards, "uncapped consumer must never force-discard")
}

// TestRouteToDeadExchange_CounterIsolatedPerDeadExchange pins the per-binding
// fix: a broken dead exchange on one binding must not push an unrelated
// binding past the cap. A single Consumer often serves multiple bindings;
// when the counter was per-Consumer, queue A's failure streak force-discarded
// queue B's very first (possibly transient) DLE failure — message loss.
func TestRouteToDeadExchange_CounterIsolatedPerDeadExchange(t *testing.T) {
	const cap = 2
	dlPub := &togglingDeadLetterPublisher{nextErr: errors.New("dead exchange A is broken")}
	var discards int
	c := newTestConsumer(dlPub, ConsumerHooks{
		OnDiscard: func(_, _, _ string) { discards++ },
	})
	c.maxDLQConsecutiveFail = cap
	msg, _ := messaging.NewMessage("test.event", "payload")

	bindingA := messaging.Binding{
		BindingSpec:  messaging.BindingSpec{ConsumerGroup: "queue-a", RoutingKey: "test.event", Retry: &messaging.RetryPolicy{MaxRetries: 3}},
		DeadExchange: "dead.a",
	}
	bindingB := messaging.Binding{
		BindingSpec:  messaging.BindingSpec{ConsumerGroup: "queue-b", RoutingKey: "test.event", Retry: &messaging.RetryPolicy{MaxRetries: 3}},
		DeadExchange: "dead.b",
	}

	route := func(b messaging.Binding) string {
		ack := &fakeAcknowledger{}
		delivery := makeAMQPDelivery(ack, msg)
		return c.routeToDeadExchange(context.Background(), delivery, msg, b, errors.New("boom"), 0)
	}

	// Drive binding A's broken DLE well past the cap.
	for i := 0; i < cap+5; i++ {
		_ = route(bindingA)
	}
	require.Positive(t, discards, "binding A's broken DLE must eventually force-discard")
	discardsAfterA := discards

	// Binding B's FIRST failure must still nack-to-retry — its own counter
	// is independent of A's exhausted streak.
	outcome := route(bindingB)
	assert.Equal(t, amqpConsumeOutcomeDLQPublishFailed, outcome,
		"binding B's first DLE failure must nack-to-retry, not inherit A's force-discard")
	assert.Equal(t, discardsAfterA, discards,
		"binding B's first failure must not trigger a force-discard from A's streak")
}

// --- Panic recovery ---

func TestConsumer_HandleDelivery_HandlerPanic_DoesNotKillGoroutine(t *testing.T) {
	ack := &fakeAcknowledger{}
	var discarded bool
	c := newTestConsumer(nil, ConsumerHooks{
		OnDiscard: func(_, _, _ string) { discarded = true },
	})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	handler := func(_ context.Context, _ messaging.Delivery) error {
		panic("boom")
	}

	assert.NotPanics(t, func() {
		c.handleDelivery(context.Background(), delivery, handler, binding)
	})
	assert.True(t, ack.acked, "panic should route through permanent-error discard, which acks")
	assert.False(t, ack.nacked, "panic must not requeue (would create poison-pill loop)")
	assert.True(t, discarded, "OnDiscard should fire because panic is converted to permanent error")
}

func TestConsumer_HandleDelivery_HandlerPanicWithRetry_RoutesToDiscard(t *testing.T) {
	ack := &fakeAcknowledger{}
	c := newTestConsumer(&fakeDeadLetterPublisher{}, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
		DeadExchange: "test-exchange.dead",
	}

	handler := func(_ context.Context, _ messaging.Delivery) error {
		panic(errors.New("nil deref"))
	}

	assert.NotPanics(t, func() {
		c.handleDelivery(context.Background(), delivery, handler, binding)
	})
	assert.True(t, ack.acked, "permanent panic must ack, never requeue, even when retry is configured")
	assert.False(t, ack.nacked)
}

// --- Hooks not called when nil ---

func TestConsumer_HandleFailure_NilHooks_DoNotPanic(t *testing.T) {
	ack := &fakeAcknowledger{}
	c := newTestConsumer(nil, ConsumerHooks{})
	msg, _ := messaging.NewMessage("test.event", "payload")
	delivery := makeAMQPDelivery(ack, msg)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "test-queue"}}

	// No hooks set, should not panic.
	c.handleFailure(context.Background(), delivery, msg, binding, errors.New("err"))
}

// --- NewConsumer nil-dependency guards ---

type noopConnector struct{}

func (noopConnector) Channel() (*amqp.Channel, error) { return nil, errors.New("noop") }
func (noopConnector) Healthy() bool                   { return true }
func (noopConnector) Stop(_ context.Context) error    { return nil }

func TestNewConsumer_PanicsOnNilConnector(t *testing.T) {
	assert.Panics(t, func() {
		NewConsumer(nil, nil, discardLogger())
	})
}

func TestNewConsumer_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewConsumer(noopConnector{}, nil, discardLogger(), nil)
	})
}

func TestNewConsumer_NilLoggerNormalisedToDefault(t *testing.T) {
	c := NewConsumer(noopConnector{}, nil, nil)
	require.NotNil(t, c.logger, "nil logger must be replaced with slog.Default()")
}

// --- WithHandlerTimeout ---

func TestWithHandlerTimeout_SetsField(t *testing.T) {
	c := NewConsumer(noopConnector{}, nil, discardLogger(), WithHandlerTimeout(90*time.Second))
	assert.Equal(t, 90*time.Second, c.handlerTimeout)
	assert.Equal(t, 90*time.Second, c.effectiveHandlerTimeout())
}

func TestWithHandlerTimeout_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			assert.Panics(t, func() {
				WithHandlerTimeout(d)
			})
		})
	}
}

func TestNewConsumer_DefaultsHandlerTimeout(t *testing.T) {
	c := NewConsumer(noopConnector{}, nil, discardLogger())
	assert.Equal(t, defaultHandlerTimeout, c.handlerTimeout)
	assert.Equal(t, defaultHandlerTimeout, c.effectiveHandlerTimeout())
}

func TestEffectiveHandlerTimeout_FallsBackForZeroValueConsumer(t *testing.T) {
	// A Consumer built directly (bypassing NewConsumer) leaves the field at
	// zero; the effective timeout must still be the default so a stuck
	// handler cannot stall the goroutine forever.
	c := &Consumer{}
	assert.Equal(t, defaultHandlerTimeout, c.effectiveHandlerTimeout())
}

func TestConsumeOnce_PanicsOnNilHandler(t *testing.T) {
	c := NewConsumer(noopConnector{}, nil, discardLogger())
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{ConsumerGroup: "q"}}
	assert.Panics(t, func() {
		_ = c.ConsumeOnce(context.Background(), binding, nil)
	})
}

// --- ConsumeOnce validation ---

func TestConsumeOnce_RetryWithoutPublisher_ReturnsError(t *testing.T) {
	c := &Consumer{
		logger:    discardLogger(),
		publisher: nil,
		prefetch:  defaultPrefetch,
	}

	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			ConsumerGroup: "test-queue",
			RoutingKey:    "test.event",
			Retry:         &messaging.RetryPolicy{MaxRetries: 3},
		},
	}

	err := c.ConsumeOnce(context.Background(), binding, func(_ context.Context, _ messaging.Delivery) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a publisher")
}
