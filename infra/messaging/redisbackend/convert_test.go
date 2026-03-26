package redisbackend

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
	stream "github.com/bds421/rho-kit/data/stream/redisstream"
)

func TestToStreamMessage(t *testing.T) {
	msg, err := messaging.NewMessage("order.created", map[string]string{"id": "42"})
	require.NoError(t, err)
	msg = msg.WithHeader("trace-id", "abc-123")

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

	d := toDelivery(sm, "users-stream")

	assert.Equal(t, "msg-id", d.Message.ID)
	assert.Equal(t, "user.updated", d.Message.Type)
	assert.Equal(t, "users-stream", d.Exchange)
	assert.Equal(t, "user.updated", d.RoutingKey)
	assert.Equal(t, "xyz", d.Message.Headers["trace-id"])
	assert.Equal(t, "xyz", d.Headers["trace-id"])
}

func TestToDelivery_EmptyHeaders(t *testing.T) {
	sm := stream.Message{
		ID:   "msg-id",
		Type: "test.event",
	}

	d := toDelivery(sm, "stream")

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

	d := toDelivery(sm, "stream")

	assert.Equal(t, 5, d.SchemaVersion)
	assert.Equal(t, 5, d.Message.SchemaVersion)
}

func TestToDelivery_SchemaVersionZeroWhenAbsent(t *testing.T) {
	sm := stream.Message{
		ID:      "msg-id",
		Type:    "test.event",
		Headers: map[string]string{"other": "value"},
	}

	d := toDelivery(sm, "stream")

	assert.Equal(t, 0, d.SchemaVersion)
	assert.Equal(t, 0, d.Message.SchemaVersion)
}

// --- parseSchemaVersion ---

func TestParseSchemaVersion_ValidPositive(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "7"})
	assert.Equal(t, 7, v)
}

func TestParseSchemaVersion_MissingHeader(t *testing.T) {
	v := parseSchemaVersion(map[string]string{"other": "value"})
	assert.Equal(t, 0, v)
}

func TestParseSchemaVersion_NilHeaders(t *testing.T) {
	v := parseSchemaVersion(nil)
	assert.Equal(t, 0, v)
}

func TestParseSchemaVersion_InvalidString(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "abc"})
	assert.Equal(t, 0, v)
}

func TestParseSchemaVersion_NegativeValue(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "-3"})
	assert.Equal(t, 0, v)
}

func TestParseSchemaVersion_Zero(t *testing.T) {
	v := parseSchemaVersion(map[string]string{messaging.HeaderSchemaVersion: "0"})
	assert.Equal(t, 0, v)
}
