//go:build integration

package amqpbackend_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend"
)

// startDedicatedRabbitMQ starts a standalone RabbitMQ container for reconnection
// tests. Unlike the shared container from rabbitmqtest, this one can be
// stopped/killed to simulate outages without affecting other tests.
func startDedicatedRabbitMQ(t *testing.T) (*tcrabbit.RabbitMQContainer, string) {
	t.Helper()
	ctx := context.Background()

	container, err := tcrabbit.Run(ctx, "rabbitmq:4.2.3-management")
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	url, err := container.AmqpURL(ctx)
	require.NoError(t, err)

	return container, url
}

// closeAllRabbitMQConnections uses rabbitmqctl to force-close all client
// connections. This simulates a network partition or server-side disconnect.
func closeAllRabbitMQConnections(t *testing.T, container *tcrabbit.RabbitMQContainer) {
	t.Helper()
	ctx := context.Background()

	code, _, err := container.Exec(ctx, []string{
		"rabbitmqctl", "close_all_connections", "test-reconnect",
	})
	require.NoError(t, err)
	require.Equal(t, 0, code, "rabbitmqctl close_all_connections failed")
}

func TestConnection_Reconnect_OnReconnectCallbackFires(t *testing.T) {
	container, url := startDedicatedRabbitMQ(t)

	var reconnected atomic.Bool
	conn, err := amqpbackend.Dial(url, slog.Default(), amqpbackend.OnReconnect(func(_ amqpbackend.Connector) error {
		reconnected.Store(true)
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	assert.True(t, conn.Healthy(), "should be healthy before disconnect")

	closeAllRabbitMQConnections(t, container)

	require.Eventually(t, func() bool {
		return reconnected.Load()
	}, 30*time.Second, 100*time.Millisecond, "OnReconnect callback should fire after connection drop")

	assert.True(t, conn.Healthy(), "should be healthy after reconnect")
}

func TestConnection_Reconnect_ChannelWorksAfterReconnect(t *testing.T) {
	container, url := startDedicatedRabbitMQ(t)

	reconnected := make(chan struct{}, 1)
	conn, err := amqpbackend.Dial(url, slog.Default(), amqpbackend.OnReconnect(func(_ amqpbackend.Connector) error {
		select {
		case reconnected <- struct{}{}:
		default:
		}
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	closeAllRabbitMQConnections(t, container)

	select {
	case <-reconnected:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for reconnect")
	}

	ch, err := conn.Channel()
	require.NoError(t, err, "should be able to open channel after reconnection")
	assert.NoError(t, ch.Close())
}

func TestConnection_MaxReconnectAttempts_FiresDead(t *testing.T) {
	ctx := context.Background()

	// Dedicated container — will be terminated mid-test to force reconnect failure.
	container, err := tcrabbit.Run(ctx, "rabbitmq:4.2.3-management")
	require.NoError(t, err)

	url, err := container.AmqpURL(ctx)
	require.NoError(t, err)

	conn, err := amqpbackend.Dial(url, slog.Default(), amqpbackend.WithMaxReconnectAttempts(2))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Terminate the container — all reconnect attempts will TCP-refuse.
	require.NoError(t, container.Terminate(ctx))

	select {
	case <-conn.Dead():
		// Expected: Dead() fires after exhausting 2 reconnect attempts.
	case <-time.After(60 * time.Second):
		t.Fatal("timed out waiting for Dead() channel to close")
	}

	assert.False(t, conn.Healthy(), "should not be healthy after all attempts exhausted")
}

func TestConnection_Reconnect_CallbackError_DoesNotBlockRecovery(t *testing.T) {
	container, url := startDedicatedRabbitMQ(t)

	var reconnected atomic.Bool
	conn, err := amqpbackend.Dial(url, slog.Default(), amqpbackend.OnReconnect(func(_ amqpbackend.Connector) error {
		reconnected.Store(true)
		return errors.New("callback error — should be logged, not fatal")
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	closeAllRabbitMQConnections(t, container)

	require.Eventually(t, func() bool {
		return reconnected.Load()
	}, 30*time.Second, 100*time.Millisecond)

	assert.True(t, conn.Healthy(), "callback error should not prevent reconnection")
}

func TestConnection_Reconnect_HealthTransitions(t *testing.T) {
	container, url := startDedicatedRabbitMQ(t)

	reconnected := make(chan struct{}, 1)
	conn, err := amqpbackend.Dial(url, slog.Default(), amqpbackend.OnReconnect(func(_ amqpbackend.Connector) error {
		select {
		case reconnected <- struct{}{}:
		default:
		}
		return nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.True(t, conn.Healthy(), "should start healthy")

	closeAllRabbitMQConnections(t, container)

	// After disconnect, Healthy() should eventually return false (before reconnect succeeds).
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 50*time.Millisecond, "should become unhealthy after disconnect")

	// After reconnect, should be healthy again.
	select {
	case <-reconnected:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for reconnect")
	}

	assert.True(t, conn.Healthy(), "should be healthy after reconnect")
}
