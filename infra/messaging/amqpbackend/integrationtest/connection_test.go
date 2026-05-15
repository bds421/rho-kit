//go:build integration

package amqpbackend_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest"
	"github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
)

func TestDial_Success(t *testing.T) {
	url := rabbitmqtest.Start(t)
	logger := slog.Default()

	conn, err := dialLocalRabbitMQ(t, url, logger)
	require.NoError(t, err)
	require.NotNil(t, conn)

	t.Cleanup(func() { _ = conn.Stop(context.Background()) })
}

func TestDial_InvalidURL(t *testing.T) {
	logger := slog.Default()

	conn, err := amqpbackend.Connect("amqp://invalid:5672/", logger, amqpbackend.WithoutTLS())
	assert.Error(t, err)
	assert.Nil(t, conn)
}

func TestConnection_Channel(t *testing.T) {
	url := rabbitmqtest.Start(t)
	logger := slog.Default()

	conn, err := dialLocalRabbitMQ(t, url, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Stop(context.Background()) })

	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.NoError(t, ch.Close())
}

func TestConnection_CloseIdempotent(t *testing.T) {
	url := rabbitmqtest.Start(t)
	logger := slog.Default()

	conn, err := dialLocalRabbitMQ(t, url, logger)
	require.NoError(t, err)

	assert.NoError(t, conn.Stop(context.Background()))
	assert.NoError(t, conn.Stop(context.Background()))
}
