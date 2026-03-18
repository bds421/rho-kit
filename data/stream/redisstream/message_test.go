package redisstream

import (
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

func TestMessage_WithHeader(t *testing.T) {
	msg, err := NewMessage("test.event", "data")
	require.NoError(t, err)

	withHeader := msg.WithHeader("X-Trace", "abc123")

	// Original unchanged (immutability).
	assert.Nil(t, msg.Headers)

	// New message has the header.
	assert.Equal(t, "abc123", withHeader.Headers["X-Trace"])
	assert.Equal(t, msg.ID, withHeader.ID)
}

func TestMessage_WithHeader_PanicsOnInvalid(t *testing.T) {
	msg, _ := NewMessage("test.event", "data")

	assert.Panics(t, func() { msg.WithHeader("", "value") })
	assert.Panics(t, func() { msg.WithHeader("bad\x00key", "value") })
	assert.Panics(t, func() { msg.WithHeader("key", "bad\nvalue") })
}
