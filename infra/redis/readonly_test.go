package redis

import (
	"errors"
	"fmt"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestIsReadOnlyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("connection refused"), false},
		{"sentinel-ErrPrimaryReadOnly", ErrPrimaryReadOnly, true},
		{"wrapped sentinel", fmt.Errorf("write failed: %w", ErrPrimaryReadOnly), true},
		{
			name: "goredis READONLY reply",
			err:  goredis.Error(nil),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsReadOnlyError(tc.err))
		})
	}
}

func TestIsReadOnlyError_RedisError(t *testing.T) {
	// Simulate a server READONLY reply: go-redis converts these into a
	// proto.RedisError which implements the redis.Error interface.
	err := serverReplyError("READONLY You can't write against a read only replica.")
	assert.True(t, IsReadOnlyError(err))

	wrapped := fmt.Errorf("redis: set failed: %w", err)
	assert.True(t, IsReadOnlyError(wrapped))

	notRO := serverReplyError("MOVED 1234 127.0.0.1:7001")
	assert.False(t, IsReadOnlyError(notRO))
}

func TestIsReadOnlyError_PlainStringFallback(t *testing.T) {
	// Some intermediaries flatten the redis.Error type to a plain error.
	err := errors.New("READONLY You can't write against a read only replica.")
	assert.True(t, IsReadOnlyError(err))
}

// serverReplyError returns an error value that implements the goredis.Error
// interface, matching the shape go-redis surfaces for server reply errors.
type readOnlyServerErr string

func (e readOnlyServerErr) Error() string { return string(e) }
func (readOnlyServerErr) RedisError()     {}

func serverReplyError(msg string) goredis.Error { return readOnlyServerErr(msg) }

func TestConnection_MarkReadOnly_FlipsHealthy(t *testing.T) {
	c := &Connection{
		healthy:  true,
		instance: "test",
		metrics:  defaultMetrics(),
	}
	assert.True(t, c.Healthy())
	assert.False(t, c.ReadOnly())

	c.MarkReadOnly()

	assert.False(t, c.Healthy())
	assert.True(t, c.ReadOnly())
}

func TestConnection_MarkReadOnly_NilSafe(t *testing.T) {
	var c *Connection
	c.MarkReadOnly()
	assert.False(t, c.ReadOnly())
	assert.False(t, c.Healthy())
}
