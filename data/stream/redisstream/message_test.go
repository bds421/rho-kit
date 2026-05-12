package redisstream

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMessage(t *testing.T) {
	msg, err := NewMessage("test.event", map[string]string{"key": "value"})
	require.NoError(t, err)

	assert.NotEmpty(t, msg.ID)
	assert.Equal(t, "test.event", msg.Type)
	assert.NotNil(t, msg.Payload)
	assert.False(t, msg.Timestamp.IsZero())
}

func TestNewMessage_RejectsInvalidType(t *testing.T) {
	for _, msgType := range []string{"", "bad type", "bad\ttype", "bad\ntype"} {
		t.Run(msgType, func(t *testing.T) {
			_, err := NewMessage(msgType, map[string]string{"key": "value"})
			assert.ErrorIs(t, err, ErrInvalidMessage)
		})
	}
}

func TestMessage_WithHeader(t *testing.T) {
	msg, err := NewMessage("test.event", "data")
	require.NoError(t, err)

	withHeader, err := msg.WithHeader("X-Trace", "abc123")
	require.NoError(t, err)

	// Original unchanged (immutability).
	assert.Nil(t, msg.Headers)

	// New message has the header.
	assert.Equal(t, "abc123", withHeader.Headers["X-Trace"])
	assert.Equal(t, msg.ID, withHeader.ID)
}

func TestMessage_CloneDetachesPayloadAndHeaders(t *testing.T) {
	msg := Message{
		ID:            "msg-1",
		Type:          "test.event",
		Payload:       []byte(`{"ok":true}`),
		Headers:       map[string]string{"X-Trace": "abc123"},
		RedisStreamID: "1-0",
	}

	clone := msg.Clone()
	clone.Payload[1] = 'X'
	clone.Headers["X-Trace"] = "changed"

	assert.Equal(t, `{"ok":true}`, string(msg.Payload))
	assert.Equal(t, "abc123", msg.Headers["X-Trace"])
	assert.Equal(t, "1-0", clone.RedisStreamID)
}

func TestMessage_WithHeaderDetachesPayload(t *testing.T) {
	msg := Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{"ok":true}`),
	}

	withHeader, err := msg.WithHeader("X-Trace", "abc123")
	require.NoError(t, err)
	withHeader.Payload[1] = 'X'

	assert.Equal(t, `{"ok":true}`, string(msg.Payload))
}

func TestMessage_WithHeader_ErrorsOnInvalid(t *testing.T) {
	msg, _ := NewMessage("test.event", "data")

	for name, h := range map[string]struct{ k, v string }{
		"empty name":     {"", "value"},
		"null byte name": {"bad\x00key", "value"},
		"space in name":  {"Bad Header", "value"},
		"newline value":  {"key", "bad\nvalue"},
		"oversize value": {"key", strings.Repeat("x", MaxHeaderValueBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := msg.WithHeader(h.k, h.v)
			assert.ErrorIs(t, err, ErrInvalidHeader)
			assert.Zero(t, got, "invalid header returns the zero Message")
		})
	}
}

func TestValidateHeaders(t *testing.T) {
	require.NoError(t, ValidateHeaders(map[string]string{
		"X-Trace":     "abc123",
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
	}))

	for name, headers := range map[string]map[string]string{
		"empty name":      {"": "value"},
		"space in name":   {"Bad Header": "value"},
		"colon in name":   {"Bad:Header": "value"},
		"newline value":   {"X-Trace": "bad\nvalue"},
		"oversize name":   {strings.Repeat("a", MaxHeaderNameBytes+1): "value"},
		"oversize value":  {"X-Trace": strings.Repeat("x", MaxHeaderValueBytes+1)},
		"non-ascii name":  {"X-Tenant-\xc3\x84": "value"},
		"null byte value": {"X-Trace": "bad\x00value"},
		"invalid utf8":    {"X-Trace": string([]byte{'o', 'k', 0xff})},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateHeaders(headers)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidHeader))
			if strings.Contains(name, "oversize") {
				assert.NotContains(t, err.Error(), "128")
				assert.NotContains(t, err.Error(), "129")
				assert.NotContains(t, err.Error(), "8192")
				assert.NotContains(t, err.Error(), "8193")
			}
		})
	}
}

func TestValidateHeaders_DoesNotReflectHeaderMetadata(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"invalid name":  {"Bad Header secret-token": "value"},
		"invalid value": {"X-Secret-Token": "bad\nvalue"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateHeaders(headers)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidHeader), "err=%v", err)
			assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
		})
	}
}

func TestValidateMessage(t *testing.T) {
	valid := Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte(`{"ok":true}`),
		Headers: map[string]string{"X-Trace": "abc123"},
	}
	require.NoError(t, ValidateMessage(valid, defaultStreamMaxPayloadSize))

	for name, msg := range map[string]Message{
		"empty type":      {ID: "msg-1", Payload: []byte(`{}`)},
		"space type":      {ID: "msg-1", Type: "bad type", Payload: []byte(`{}`)},
		"tab type":        {ID: "msg-1", Type: "bad\ttype", Payload: []byte(`{}`)},
		"invalid type":    {ID: "msg-1", Type: string([]byte{0xff, 0xfe}), Payload: []byte(`{}`)},
		"oversize type":   {ID: "msg-1", Type: strings.Repeat("x", MaxMessageTypeBytes+1), Payload: []byte(`{}`)},
		"space id":        {ID: "bad id", Type: "test.event", Payload: []byte(`{}`)},
		"invalid id":      {ID: string([]byte{0xff, 0xfe}), Type: "test.event", Payload: []byte(`{}`)},
		"oversize id":     {ID: strings.Repeat("x", MaxMessageIDBytes+1), Type: "test.event", Payload: []byte(`{}`)},
		"invalid payload": {ID: "msg-1", Type: "test.event", Payload: []byte(`not-json`)},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateMessage(msg, defaultStreamMaxPayloadSize)
			assert.True(t, errors.Is(err, ErrInvalidMessage), "err=%v", err)
			if strings.Contains(name, "oversize") {
				assert.NotContains(t, err.Error(), "255")
				assert.NotContains(t, err.Error(), "256")
				assert.NotContains(t, err.Error(), "257")
			}
		})
	}

	err := ValidateMessage(Message{
		ID:      "msg-1",
		Type:    "test.event",
		Payload: []byte("too large"),
	}, 4)
	assert.True(t, errors.Is(err, ErrMessageTooLarge), "err=%v", err)
}
