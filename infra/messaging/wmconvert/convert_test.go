package wmconvert_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/infra/messaging/wmconvert"
)

func TestToWatermill_RoundTrip(t *testing.T) {
	original := messaging.Message{
		ID:            "msg-123",
		Type:          "order.created",
		Payload:       json.RawMessage(`{"order_id":"abc"}`),
		Timestamp:     time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC),
		SchemaVersion: 2,
		Headers: map[string]string{
			"X-Correlation-Id": "corr-456",
			"X-Request-Id":     "req-789",
		},
	}

	wmMsg := wmconvert.ToWatermill(original, "orders", "order.created")

	assert.Equal(t, "msg-123", wmMsg.UUID)
	assert.Equal(t, `{"order_id":"abc"}`, string(wmMsg.Payload))
	assert.Equal(t, "order.created", wmMsg.Metadata.Get(wmconvert.MetaMessageType))
	assert.Equal(t, "2", wmMsg.Metadata.Get(wmconvert.MetaSchemaVersion))
	assert.Equal(t, "orders", wmMsg.Metadata.Get(wmconvert.MetaExchange))
	assert.Equal(t, "order.created", wmMsg.Metadata.Get(wmconvert.MetaRoutingKey))
	assert.Equal(t, "corr-456", wmMsg.Metadata.Get("X-Correlation-Id"))
	assert.Equal(t, "req-789", wmMsg.Metadata.Get("X-Request-Id"))

	restored, err := wmconvert.FromWatermill(wmMsg)
	require.NoError(t, err)

	assert.Equal(t, original.ID, restored.ID)
	assert.Equal(t, original.Type, restored.Type)
	assert.Equal(t, original.Payload, restored.Payload)
	assert.Equal(t, original.Timestamp, restored.Timestamp)
	assert.Equal(t, original.SchemaVersion, restored.SchemaVersion)
	assert.Equal(t, original.Headers["X-Correlation-Id"], restored.Headers["X-Correlation-Id"])
	assert.Equal(t, original.Headers["X-Request-Id"], restored.Headers["X-Request-Id"])
}

func TestFromWatermill_MissingType(t *testing.T) {
	wmMsg := wmconvert.ToWatermill(messaging.Message{
		ID:      "msg-123",
		Payload: json.RawMessage(`{}`),
	}, "", "")

	// Clear the message type metadata to simulate a bad message.
	wmMsg.Metadata.Set(wmconvert.MetaMessageType, "")

	_, err := wmconvert.FromWatermill(wmMsg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rho_message_type")
}

func TestToWatermill_NoSchemaVersion(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
	}

	wmMsg := wmconvert.ToWatermill(msg, "", "")
	assert.Empty(t, wmMsg.Metadata.Get(wmconvert.MetaSchemaVersion))

	restored, err := wmconvert.FromWatermill(wmMsg)
	require.NoError(t, err)
	assert.Equal(t, uint(0), restored.SchemaVersion)
}

func TestToWatermill_EmptyHeaders(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
	}

	wmMsg := wmconvert.ToWatermill(msg, "", "")
	restored, err := wmconvert.FromWatermill(wmMsg)
	require.NoError(t, err)
	assert.Nil(t, restored.Headers)
}

func TestToDelivery(t *testing.T) {
	msg := messaging.Message{
		ID:            "msg-1",
		Type:          "order.created",
		Payload:       json.RawMessage(`{"id":"abc"}`),
		Timestamp:     time.Now().UTC().Truncate(time.Millisecond),
		SchemaVersion: 3,
		Headers: map[string]string{
			"custom-header": "value",
		},
	}

	wmMsg := wmconvert.ToWatermill(msg, "orders-exchange", "order.created")
	wmMsg.Metadata.Set(wmconvert.MetaReplyTo, "reply-queue")
	wmMsg.Metadata.Set(wmconvert.MetaCorrelationID, "corr-1")
	wmMsg.Metadata.Set(wmconvert.MetaRedelivered, "true")

	delivery, err := wmconvert.ToDelivery(wmMsg)
	require.NoError(t, err)

	assert.Equal(t, msg.ID, delivery.Message.ID)
	assert.Equal(t, msg.Type, delivery.Message.Type)
	assert.Equal(t, "orders-exchange", delivery.Exchange)
	assert.Equal(t, "order.created", delivery.RoutingKey)
	assert.Equal(t, "reply-queue", delivery.ReplyTo)
	assert.Equal(t, "corr-1", delivery.CorrelationID)
	assert.Equal(t, uint(3), delivery.SchemaVersion)
	assert.True(t, delivery.Redelivered)
	assert.Equal(t, "value", delivery.Headers["custom-header"])
}

func TestReservedKeysNotInHeaders(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
		Headers: map[string]string{
			"user-key": "user-value",
		},
	}

	wmMsg := wmconvert.ToWatermill(msg, "ex", "rk")
	restored, err := wmconvert.FromWatermill(wmMsg)
	require.NoError(t, err)

	assert.Equal(t, "user-value", restored.Headers["user-key"])
	assert.NotContains(t, restored.Headers, wmconvert.MetaMessageType)
	assert.NotContains(t, restored.Headers, wmconvert.MetaExchange)
	assert.NotContains(t, restored.Headers, wmconvert.MetaRoutingKey)
}
