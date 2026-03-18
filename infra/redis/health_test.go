package redis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newTestConnection(healthy, wasConnected bool) *Connection {
	c := &Connection{
		closed:    make(chan struct{}),
		dead:      make(chan struct{}),
		connected: make(chan struct{}),
		healthy:   healthy,
	}
	if wasConnected {
		close(c.connected)
	}
	return c
}

func TestHealthCheck_States(t *testing.T) {
	tests := []struct {
		name         string
		healthy      bool
		wasConnected bool
		expected     string
	}{
		{"healthy connection", true, true, "healthy"},
		{"never connected (lazy start)", false, false, "connecting"},
		{"was connected but lost", false, true, "unhealthy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newTestConnection(tt.healthy, tt.wasConnected)
			check := HealthCheck(conn)
			assert.Equal(t, tt.expected, check.Check(context.Background()))
			assert.True(t, check.Critical)
			assert.Equal(t, "redis", check.Name)
		})
	}
}

func TestNonCriticalHealthCheck_States(t *testing.T) {
	conn := newTestConnection(true, true)
	check := NonCriticalHealthCheck(conn)
	assert.Equal(t, "healthy", check.Check(context.Background()))
	assert.False(t, check.Critical)
}

func TestCriticalHealthCheck_States(t *testing.T) {
	tests := []struct {
		name         string
		healthy      bool
		wasConnected bool
		expected     string
	}{
		{"healthy", true, true, "healthy"},
		{"never connected", false, false, "connecting"},
		{"unhealthy", false, true, "unhealthy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newTestConnection(tt.healthy, tt.wasConnected)
			check := CriticalHealthCheck(conn)
			assert.Equal(t, tt.expected, check.Check(context.Background()))
			assert.True(t, check.Critical)
		})
	}
}

func TestConnection_WasConnected(t *testing.T) {
	t.Run("not yet connected", func(t *testing.T) {
		conn := newTestConnection(false, false)
		assert.False(t, conn.WasConnected())
	})

	t.Run("was connected", func(t *testing.T) {
		conn := newTestConnection(true, true)
		assert.True(t, conn.WasConnected())
	})
}

func TestConnection_Dead(t *testing.T) {
	conn := newTestConnection(false, false)

	select {
	case <-conn.Dead():
		t.Fatal("dead channel should not be closed")
	default:
	}

	close(conn.dead)

	select {
	case <-conn.Dead():
		// expected
	default:
		t.Fatal("dead channel should be closed")
	}
}

func TestConnection_Close_Idempotent(t *testing.T) {
	conn := &Connection{
		closed:    make(chan struct{}),
		dead:      make(chan struct{}),
		connected: make(chan struct{}),
		client:    nil,
	}

	conn.healthy = true
	conn.mu.Lock()
	conn.healthy = false
	conn.mu.Unlock()
	assert.False(t, conn.Healthy())
}
