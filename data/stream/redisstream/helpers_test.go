package redisstream

import (
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func fakeXMessage(values map[string]any) goredis.XMessage {
	return goredis.XMessage{
		ID:     "1234567890-0",
		Values: values,
	}
}

func TestIsGroupExistsError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"exact BUSYGROUP error", errors.New("BUSYGROUP Consumer Group name already exists"), true},
		{"BUSYGROUP with extra detail", errors.New("BUSYGROUP Consumer Group name already exists (extra detail)"), true},
		{"unrelated error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isGroupExistsError(tt.err))
		})
	}
}

func TestParseMessage(t *testing.T) {
	t.Run("full message", func(t *testing.T) {
		raw := fakeXMessage(map[string]any{
			"id":      "019c-uuid",
			"type":    "test.event",
			"payload": `{"key":"value"}`,
			"ts":      "2026-01-01T00:00:00Z",
			"headers": `{"X-Trace":"abc"}`,
		})
		msg := parseMessage(raw)

		assert.Equal(t, "019c-uuid", msg.ID)
		assert.Equal(t, "test.event", msg.Type)
		assert.Equal(t, `{"key":"value"}`, string(msg.Payload))
		assert.Equal(t, "abc", msg.Headers["X-Trace"])
		assert.False(t, msg.Timestamp.IsZero())
		assert.Equal(t, raw.ID, msg.RedisStreamID)
	})

	t.Run("empty values", func(t *testing.T) {
		raw := fakeXMessage(map[string]any{})
		msg := parseMessage(raw)

		assert.Empty(t, msg.ID)
		assert.Empty(t, msg.Type)
		assert.Nil(t, msg.Payload)
		assert.True(t, msg.Timestamp.IsZero())
	})

	t.Run("invalid timestamp", func(t *testing.T) {
		raw := fakeXMessage(map[string]any{"ts": "not-a-time"})
		msg := parseMessage(raw)
		assert.True(t, msg.Timestamp.IsZero())
	})

	t.Run("invalid headers JSON", func(t *testing.T) {
		raw := fakeXMessage(map[string]any{"headers": "not json"})
		msg := parseMessage(raw)
		assert.Nil(t, msg.Headers)
	})
}
