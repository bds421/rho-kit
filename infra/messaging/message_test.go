package messaging_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
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

func TestNewMessage_InvalidType(t *testing.T) {
	for _, msgType := range []string{
		"bad type",
		"bad\nline",
		strings.Repeat("x", messaging.MaxMessageTypeBytes+1),
		string([]byte{'o', 'k', 0xff}),
	} {
		t.Run(msgType, func(t *testing.T) {
			_, err := messaging.NewMessage(msgType, nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, messaging.ErrInvalidMessage)
		})
	}
}

func TestNewMessage_UnmarshalablePayload(t *testing.T) {
	_, err := messaging.NewMessage("test.event", make(chan int))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marshal payload")
}

func TestValidateMessage(t *testing.T) {
	valid := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: json.RawMessage(`{"ok":true}`),
		Headers: map[string]string{"X-Trace": "trace-1"},
	}
	require.NoError(t, messaging.ValidateMessage(valid))

	for name, mutate := range map[string]func(*messaging.Message){
		"empty id":       func(m *messaging.Message) { m.ID = "" },
		"id whitespace":  func(m *messaging.Message) { m.ID = "msg 1" },
		"id too long":    func(m *messaging.Message) { m.ID = strings.Repeat("m", messaging.MaxMessageIDBytes+1) },
		"id invalid utf": func(m *messaging.Message) { m.ID = string([]byte{'m', 0xff}) },
		"empty type":     func(m *messaging.Message) { m.Type = "" },
		"type newline":   func(m *messaging.Message) { m.Type = "test\nevent" },
		"type too long":  func(m *messaging.Message) { m.Type = strings.Repeat("t", messaging.MaxMessageTypeBytes+1) },
		"invalid json":   func(m *messaging.Message) { m.Payload = json.RawMessage(`{"broken"`) },
	} {
		t.Run(name, func(t *testing.T) {
			msg := valid.Clone()
			mutate(&msg)
			err := messaging.ValidateMessage(msg)
			require.Error(t, err)
			assert.ErrorIs(t, err, messaging.ErrInvalidMessage)
			if strings.Contains(name, "too long") {
				assert.NotContains(t, err.Error(), "255")
				assert.NotContains(t, err.Error(), "256")
				assert.NotContains(t, err.Error(), "257")
			}
		})
	}

	invalidHeader := valid.Clone()
	invalidHeader.Headers = map[string]string{"Bad Header": "value"}
	err := messaging.ValidateMessage(invalidHeader)
	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrInvalidMessageHeader)
}

func TestMessage_WithHeader(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", "hello")
	require.NoError(t, err)

	msg2, err := msg.WithHeader("X-Correlation-Id", "abc-123")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", msg2.CorrelationID())

	// Original should be unmodified (immutability)
	assert.Empty(t, msg.CorrelationID())
}

func TestMessage_WithHeader_ErrorsOnInvalidHeader(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", "hello")
	require.NoError(t, err)

	cases := map[string]struct{ k, v string }{
		"empty name":     {"", "value"},
		"space in name":  {"Bad Header", "value"},
		"newline value":  {"X-Trace", "bad\nvalue"},
		"oversize value": {"X-Trace", strings.Repeat("x", messaging.MaxMessageHeaderValueBytes+1)},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			got, herr := msg.WithHeader(h.k, h.v)
			assert.ErrorIs(t, herr, messaging.ErrInvalidMessageHeader)
			assert.Zero(t, got, "invalid header returns the zero Message")
		})
	}
}

func TestValidateMessageHeaders(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"X-Request-Id":     "req-1",
		"traceparent":      "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
		"x_custom.header":  "value",
		"X-Schema-Version": "3",
	}
	require.NoError(t, messaging.ValidateMessageHeaders(valid))

	for name, headers := range map[string]map[string]string{
		"empty name":      {"": "value"},
		"space in name":   {"Bad Header": "value"},
		"colon in name":   {"Bad:Header": "value"},
		"newline value":   {"X-Trace": "bad\nvalue"},
		"oversize name":   {strings.Repeat("a", messaging.MaxMessageHeaderNameBytes+1): "value"},
		"oversize value":  {"X-Trace": strings.Repeat("x", messaging.MaxMessageHeaderValueBytes+1)},
		"non-ascii name":  {"X-Tenant-\xc3\x84": "value"},
		"null byte value": {"X-Trace": "bad\x00value"},
		"invalid utf8":    {"X-Trace": string([]byte{'o', 'k', 0xff})},
	} {
		t.Run(name, func(t *testing.T) {
			err := messaging.ValidateMessageHeaders(headers)
			require.Error(t, err)
			assert.True(t, errors.Is(err, messaging.ErrInvalidMessageHeader))
			if strings.Contains(name, "oversize") {
				assert.NotContains(t, err.Error(), "128")
				assert.NotContains(t, err.Error(), "129")
				assert.NotContains(t, err.Error(), "8192")
				assert.NotContains(t, err.Error(), "8193")
			}
		})
	}
}

func TestValidateMessageHeaders_DoesNotReflectHeaderMetadata(t *testing.T) {
	t.Parallel()

	for name, headers := range map[string]map[string]string{
		"invalid name":  {"Bad Header secret-token": "value"},
		"invalid value": {"X-Secret-Token": "bad\nvalue"},
	} {
		t.Run(name, func(t *testing.T) {
			err := messaging.ValidateMessageHeaders(headers)
			require.Error(t, err)
			assert.True(t, errors.Is(err, messaging.ErrInvalidMessageHeader), "err=%v", err)
			assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
		})
	}
}

func TestMessage_CloneDetachesPayloadAndHeaders(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{"key":"value"}`),
		Headers: map[string]string{"X-Trace-Id": "trace-1"},
	}

	cloned := msg.Clone()
	msg.Payload[8] = 'X'
	msg.Headers["X-Trace-Id"] = "mutated"

	assert.JSONEq(t, `{"key":"value"}`, string(cloned.Payload))
	assert.Equal(t, "trace-1", cloned.Headers["X-Trace-Id"])

	cloned.Payload[8] = 'Y'
	cloned.Headers["X-Trace-Id"] = "clone-mutated"
	assert.Equal(t, byte('X'), msg.Payload[8])
	assert.Equal(t, "mutated", msg.Headers["X-Trace-Id"])
}

func TestMessage_WithHeader_PreservesExisting(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	msg, err = msg.WithHeader("X-Request-Id", "req-1")
	require.NoError(t, err)
	msg, err = msg.WithHeader("X-Correlation-Id", "corr-1")
	require.NoError(t, err)

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
		ID:      "secret-token-id",
		Payload: []byte(`not valid json`),
	}
	var target map[string]string
	err := msg.DecodePayload(&target)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode payload")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestMessage_WithSchemaVersion(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", "hello")
	require.NoError(t, err)

	msg2 := msg.WithSchemaVersion(2)
	assert.Equal(t, uint(2), msg2.SchemaVersion)

	// Original should be unmodified (immutability).
	assert.Equal(t, uint(0), msg.SchemaVersion)
}

func TestMessage_WithSchemaVersion_PreservesHeaders(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	msg, err = msg.WithHeader("X-Request-Id", "req-1")
	require.NoError(t, err)
	msg = msg.WithSchemaVersion(3)

	assert.Equal(t, uint(3), msg.SchemaVersion)
	assert.Equal(t, "req-1", msg.Headers["X-Request-Id"])
}

func TestMessage_WithSchemaVersion_HeaderImmutability(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)
	msg, err = msg.WithHeader("key", "value")
	require.NoError(t, err)

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
	assert.Equal(t, uint(5), decoded.SchemaVersion)
	assert.Equal(t, original.ID, decoded.ID)
	assert.Equal(t, original.Type, decoded.Type)
}

func TestMessage_WithHeader_PreservesSchemaVersion(t *testing.T) {
	msg, err := messaging.NewMessage("test.event", nil)
	require.NoError(t, err)

	msg = msg.WithSchemaVersion(3)
	msg, err = msg.WithHeader("X-Trace-Id", "trace-1")
	require.NoError(t, err)

	assert.Equal(t, uint(3), msg.SchemaVersion)
	assert.Equal(t, "trace-1", msg.Headers["X-Trace-Id"])
}
