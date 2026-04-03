//go:build integration

package amqpbackend_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging/amqpbackend"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/rabbitmqtest"
)

func TestDial_Success(t *testing.T) {
	url := rabbitmqtest.Start(t)
	logger := slog.Default()

	conn, err := amqpbackend.Dial(url, logger)
	require.NoError(t, err)
	require.NotNil(t, conn)

	t.Cleanup(func() { _ = conn.Close() })
}

func TestDial_InvalidURL(t *testing.T) {
	logger := slog.Default()

	conn, err := amqpbackend.Dial("amqp://invalid:5672/", logger)
	assert.Error(t, err)
	assert.Nil(t, conn)
}

func TestConnection_Channel(t *testing.T) {
	url := rabbitmqtest.Start(t)
	logger := slog.Default()

	conn, err := amqpbackend.Dial(url, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.NoError(t, ch.Close())
}

func TestConnection_CloseIdempotent(t *testing.T) {
	url := rabbitmqtest.Start(t)
	logger := slog.Default()

	conn, err := amqpbackend.Dial(url, logger)
	require.NoError(t, err)

	assert.NoError(t, conn.Close())
	assert.NoError(t, conn.Close())
}
