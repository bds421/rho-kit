package redisbackend

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestToStreamMessage(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", map[string]string{"id": "42"})
	require.NoError(t, err)
	msg, err = msg.WithHeader("trace-id", "abc-123")
	require.NoError(t, err)

	sm := toStreamMessage(msg)

	assert.Equal(t, msg.ID, sm.ID)
	assert.Equal(t, msg.Type, sm.Type)
	assert.Equal(t, msg.Timestamp, sm.Timestamp)
	assert.Equal(t, "abc-123", sm.Headers["trace-id"])

	var msgPayload, smPayload any
	require.NoError(t, json.Unmarshal(msg.Payload, &msgPayload))
	require.NoError(t, json.Unmarshal(sm.Payload, &smPayload))
	assert.Equal(t, msgPayload, smPayload)
}

func TestToStreamMessage_DetachesPayloadAndHeaders(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-id",
		Type:    "test.event",
		Payload: json.RawMessage(`{"ok":true}`),
		Headers: map[string]string{"trace-id": "abc-123"},
	}

	sm := toStreamMessage(msg)
	msg.Payload[1] = 'X'
	msg.Headers["trace-id"] = "changed"

	assert.Equal(t, `{"ok":true}`, string(sm.Payload))
	assert.Equal(t, "abc-123", sm.Headers["trace-id"])
}

func TestToStreamMessage_NilHeaders(t *testing.T) {
	msg := messaging.Message{
		ID:        "test-id",
		Type:      "test.event",
		Payload:   json.RawMessage(`{}`),
		Timestamp: time.Now().UTC(),
	}

	sm := toStreamMessage(msg)

	assert.Equal(t, "test-id", sm.ID)
	assert.Empty(t, sm.Headers)
}

func TestToDelivery(t *testing.T) {
	sm := stream.Message{
		ID:        "msg-id",
		Type:      "user.updated",
		Payload:   json.RawMessage(`{"name":"test"}`),
		Timestamp: time.Now().UTC(),
		Headers:   map[string]string{"trace-id": "xyz"},
	}

	d, err := toDelivery(sm, "users-stream")
	require.NoError(t, err)

	assert.Equal(t, "msg-id", d.Message.ID)
	assert.Equal(t, "user.updated", d.Message.Type)
	assert.Equal(t, "users-stream", d.Exchange)
	assert.Equal(t, "user.updated", d.RoutingKey)
	assert.Equal(t, "xyz", d.Message.Headers["trace-id"])
	assert.Equal(t, "xyz", d.Headers["trace-id"])
}

func TestToDelivery_DetachesPayloadAndHeaders(t *testing.T) {
	sm := stream.Message{
		ID:      "msg-id",
		Type:    "user.updated",
		Payload: json.RawMessage(`{"ok":true}`),
		Headers: map[string]string{"trace-id": "xyz"},
	}

	d, err := toDelivery(sm, "users-stream")
	require.NoError(t, err)
	sm.Payload[1] = 'X'
	sm.Headers["trace-id"] = "changed"
	assert.Equal(t, `{"ok":true}`, string(d.Message.Payload))
	assert.Equal(t, "xyz", d.Message.Headers["trace-id"])
	assert.Equal(t, "xyz", d.Headers["trace-id"])

	d.Message.Payload[1] = 'Y'
	d.Message.Headers["trace-id"] = "message"

	assert.Equal(t, `{Xok":true}`, string(sm.Payload))
	assert.Equal(t, "changed", sm.Headers["trace-id"])
	assert.Equal(t, `{Yok":true}`, string(d.Message.Payload))
	assert.Equal(t, "message", d.Message.Headers["trace-id"])
	assert.Equal(t, "xyz", d.Headers["trace-id"])
}

func TestToDelivery_PrefersRoutingKeyHeader(t *testing.T) {
	sm := stream.Message{
		ID:      "msg-id",
		Type:    "user.updated",
		Headers: map[string]string{headerRoutingKey: "users.v2.updated"},
	}

	d, err := toDelivery(sm, "users-stream")
	require.NoError(t, err)

	assert.Equal(t, "users.v2.updated", d.RoutingKey)
	// Transport routing_key is stripped from application-visible headers.
	_, ok := d.Headers[headerRoutingKey]
	assert.False(t, ok)
	_, ok = d.Message.Headers[headerRoutingKey]
	assert.False(t, ok)
}

func TestToDelivery_EmptyHeaders(t *testing.T) {
	sm := stream.Message{
		ID:   "msg-id",
		Type: "test.event",
	}

	d, err := toDelivery(sm, "stream")
	require.NoError(t, err)

	assert.Empty(t, d.Message.Headers)
	assert.Empty(t, d.Headers)
}

// --- schema version propagation ---

func TestToStreamMessage_PropagatesSchemaVersion(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", nil)
	require.NoError(t, err)
	msg = msg.WithSchemaVersion(3)

	sm := toStreamMessage(msg)

	assert.Equal(t, "3", sm.Headers[messaging.HeaderSchemaVersion])
}

func TestToStreamMessage_OmitsSchemaVersionWhenZero(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", nil)
	require.NoError(t, err)

	sm := toStreamMessage(msg)

	_, exists := sm.Headers[messaging.HeaderSchemaVersion]
	assert.False(t, exists, "schema version header should be absent for version 0")
}

func TestToDelivery_ExtractsSchemaVersion(t *testing.T) {
	sm := stream.Message{
		ID:      "msg-id",
		Type:    "test.event",
		Headers: map[string]string{messaging.HeaderSchemaVersion: "5"},
	}

	d, err := toDelivery(sm, "stream")
	require.NoError(t, err)

	assert.Equal(t, uint(5), d.SchemaVersion)
	assert.Equal(t, uint(5), d.Message.SchemaVersion)
}

func TestToDelivery_SchemaVersionZeroWhenAbsent(t *testing.T) {
	sm := stream.Message{
		ID:      "msg-id",
		Type:    "test.event",
		Headers: map[string]string{"other": "value"},
	}

	d, err := toDelivery(sm, "stream")
	require.NoError(t, err)

	assert.Equal(t, uint(0), d.SchemaVersion)
	assert.Equal(t, uint(0), d.Message.SchemaVersion)
}

// --- parseSchemaVersion ---

func TestParseSchemaVersion_ValidPositive(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "7"})
	assert.Equal(t, uint(7), v)
}

func TestParseSchemaVersion_MissingHeader(t *testing.T) {
	v := parseSchemaVersion(map[string]string{"other": "value"})
	assert.Equal(t, uint(0), v)
}

func TestParseSchemaVersion_NilHeaders(t *testing.T) {
	v := parseSchemaVersion(nil)
	assert.Equal(t, uint(0), v)
}

func TestParseSchemaVersion_InvalidString(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "abc"})
	assert.Equal(t, uint(0), v)
}

func TestParseSchemaVersion_NegativeValue(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "-3"})
	assert.Equal(t, uint(0), v)
}

func TestParseSchemaVersion_Zero(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "0"})
	assert.Equal(t, uint(0), v)
}
