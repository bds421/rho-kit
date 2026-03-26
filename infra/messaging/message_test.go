package messaging_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

func TestNewMessage_UUIDv7(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", map[string]string{"key": "value"})
	require.NoError(t, err)

	parsed, err := uuid.Parse(msg.ID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(7), parsed.Version(), "expected UUID v7")
}

func TestNewMessage_TimeSorting(t *testing.T) {
	var ids []string
	for range 10 {
		msg, err := messaging.NewMessage("test.event", nil)
		require.NoError(t, err)
		ids = append(ids, msg.ID)
	}

	for i := 1; i < len(ids); i++ {
		assert.True(t, ids[i-1] <= ids[i],
			"expected IDs to be time-sorted: %s should be <= %s", ids[i-1], ids[i])
	}
}

func TestNewMessage_PayloadEncodeDecode(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	input := payload{Name: "test", Count: 42}
	msg, err := messaging.NewMessage("test.event", input)
	require.NoError(t, err)

	var decoded payload
	require.NoError(t, msg.DecodePayload(&decoded))
	assert.Equal(t, input, decoded)
}

func TestNewMessage_NilPayload(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	assert.Equal(t, "null", string(msg.Payload))
}

func TestNewMessage_EmptyType(t *testing.T) {
	_, err := messaging.NewMessage("", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "type must not be empty")
}

func TestNewMessage_UnmarshalablePayload(t *testing.T) {
	_, err := messaging.NewMessage("test.event", make(chan int))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marshal payload")
}

func TestMessage_WithHeader(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", "hello")
	require.NoError(t, err)

	msg2 := msg.WithHeader("X-Correlation-Id", "abc-123")
	assert.Equal(t, "abc-123", msg2.CorrelationID())

	// Original should be unmodified (immutability)
	assert.Empty(t, msg.CorrelationID())
}

func TestMessage_WithHeader_PreservesExisting(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	msg = msg.WithHeader("X-Request-Id", "req-1")
	msg = msg.WithHeader("X-Correlation-Id", "corr-1")

	assert.Equal(t, "corr-1", msg.CorrelationID())
	assert.Equal(t, "req-1", msg.Headers["X-Request-Id"])
}

func TestMessage_CorrelationID_Empty(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	assert.Empty(t, msg.CorrelationID())
}

func TestMessage_DecodePayload_Error(t *testing.T) {
	msg := messaging.Message{
		ID:      "test-id",
		Payload: []byte(`not valid json`),
	}
	var target map[string]string
	err := msg.DecodePayload(&target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode payload")
}

func TestMessage_WithSchemaVersion(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", "hello")
	require.NoError(t, err)

	msg2 := msg.WithSchemaVersion(2)
	assert.Equal(t, 2, msg2.SchemaVersion)

	// Original should be unmodified (immutability).
	assert.Equal(t, 0, msg.SchemaVersion)
}

func TestMessage_WithSchemaVersion_PreservesHeaders(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	msg = msg.WithHeader("X-Request-Id", "req-1")
	msg = msg.WithSchemaVersion(3)

	assert.Equal(t, 3, msg.SchemaVersion)
	assert.Equal(t, "req-1", msg.Headers["X-Request-Id"])
}

func TestMessage_WithSchemaVersion_HeaderImmutability(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	msg = msg.WithHeader("key", "value")

	msg2 := msg.WithSchemaVersion(1)

	// Mutating msg2 headers should not affect msg.
	msg2.Headers["key"] = "changed"
	assert.Equal(t, "value", msg.Headers["key"])
}

func TestMessage_SchemaVersion_JSONOmitEmpty(t *testing.T) {
	msg := messaging.Message{
		ID:      "test-id",
		Type:    "test.event",
		Payload: json.RawMessage(`{}`),
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// SchemaVersion 0 should be omitted from JSON.
	assert.NotContains(t, string(data), "schema_version")
}

func TestMessage_SchemaVersion_JSONIncludedWhenSet(t *testing.T) {
	msg := messaging.Message{
		ID:            "test-id",
		Type:          "test.event",
		Payload:       json.RawMessage(`{}`),
		SchemaVersion: 2,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"schema_version":2`)
}

func TestMessage_SchemaVersion_JSONRoundTrip(t *testing.T) {
	original := messaging.Message{
		ID:            "test-id",
		Type:          "test.event",
		Payload:       json.RawMessage(`{"key":"val"}`),
		SchemaVersion: 5,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded messaging.Message
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, 5, decoded.SchemaVersion)
	assert.Equal(t, original.ID, decoded.ID)
	assert.Equal(t, original.Type, decoded.Type)
}

func TestMessage_WithHeader_PreservesSchemaVersion(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	msg = msg.WithSchemaVersion(3)
	msg = msg.WithHeader("X-Trace-Id", "trace-1")

	assert.Equal(t, 3, msg.SchemaVersion)
	assert.Equal(t, "trace-1", msg.Headers["X-Trace-Id"])
}

func TestMessage_WithSchemaVersion_PanicOnNegative(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	assert.Panics(t, func() {
		msg.WithSchemaVersion(-1)
	})
}
