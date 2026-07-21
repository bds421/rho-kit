package redisstream

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewConsumer_PanicsWhenClaimMinIdleTooSmall pins the review-15
// cross-validation: claimMinIdle must exceed handlerTimeout+ackTimeout so
// XAUTOCLAIM cannot reassign a still-running handler's message.
func TestNewConsumer_PanicsWhenClaimMinIdleTooSmall(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	assert.Panics(t, func() {
		_, _ = NewConsumer(client, "g",
			WithHandlerTimeout(10*time.Minute),
			// default claimMinIdle is 5m < 10m+10s
		)
	})

	assert.Panics(t, func() {
		_, _ = NewConsumer(client, "g",
			WithHandlerTimeout(30*time.Second),
			WithClaimMinIdle(30*time.Second), // == handler, not > handler+ack
		)
	})

	// Safe combo: claimMinIdle strictly above handler+ack.
	c, err := NewConsumer(client, "g",
		WithHandlerTimeout(2*time.Minute),
		WithClaimMinIdle(3*time.Minute),
	)
	require.NoError(t, err)
	assert.Equal(t, 3*time.Minute, c.claimMinIdle)
}

// TestConsumeOnce_RejectsSelfFeedDLQ pins the review-15 self-feed guard:
// dead-letter stream equal to the source stream must fail fast.
func TestConsumeOnce_RejectsSelfFeedDLQ(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	c, err := NewConsumer(client, "g", WithDeadLetterStream("orders"))
	require.NoError(t, err)

	err = c.consumeOnce(context.Background(), "orders", func(context.Context, Message) error {
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dead-letter stream must not equal source stream")
}

// TestWithConsumerRegisterer_PanicsOnNil matches the family-wide
// panic-on-nil registerer contract (review-15).
func TestWithConsumerRegisterer_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		WithConsumerRegisterer(nil)
	})
}

func TestWithProducerRegisterer_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		WithProducerRegisterer(nil)
	})
}

// TestValidateHeader_RejectsC0Controls pins review-15: header values must
// reject the full C0/DEL control class, not only NUL/CR/LF.
func TestValidateHeader_RejectsC0Controls(t *testing.T) {
	for name, value := range map[string]string{
		"ESC":    "trace\x1bid",
		"BEL":    "a\x07b",
		"TAB":    "a\tb", // tab is control
		"DEL":    "a\x7fb",
		"VT":     "a\x0bb",
		"NUL":    "a\x00b",
		"CR":     "a\rb",
		"LF":     "a\nb",
	} {
		t.Run(name, func(t *testing.T) {
			err := validateHeader("X-Trace", value)
			require.ErrorIs(t, err, ErrInvalidHeader)
		})
	}
	require.NoError(t, validateHeader("X-Trace", "plain-value"))
}

// TestMaxStreamHeadersBytes_CoversProducerTotalCap pins review-15:
// consumer pre-unmarshal cap must accept a producer-max header set.
func TestMaxStreamHeadersBytes_CoversProducerTotalCap(t *testing.T) {
	// A header set at MaxTotalHeaderBytes must fit under maxStreamHeadersBytes
	// after JSON marshaling (worst-case structural overhead is << 64 KiB).
	assert.GreaterOrEqual(t, maxStreamHeadersBytes, MaxTotalHeaderBytes)
	// One max-value header plus JSON framing stays under the wire cap.
	rawJSONOverhead := len(`{"":""}`) + 2 // quotes around name
	assert.GreaterOrEqual(t, maxStreamHeadersBytes, MaxHeaderValueBytes+rawJSONOverhead)
}

// TestNewConsumer_CustomRegistererStillWorks is a smoke pin that the
// panic-on-nil change did not break the happy path (existing fixes_test
// covers registry routing more thoroughly).
func TestNewConsumer_CustomRegistererStillWorks(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })
	reg := prometheus.NewRegistry()
	c, err := NewConsumer(client, "g", WithConsumerRegisterer(reg))
	require.NoError(t, err)
	require.NotNil(t, c.metrics)
}
