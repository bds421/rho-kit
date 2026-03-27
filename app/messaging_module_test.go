package app

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMessagingModule_PanicsOnEmptyURL(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for empty URL")
		assert.Contains(t, r, "non-empty URL")
	}()
	newMessagingModule("")
}

func TestMessagingModule_Name(t *testing.T) {
	m := newMessagingModule("amqp://localhost")
	assert.Equal(t, "rabbitmq", m.Name())
}

func TestMessagingModule_HealthChecksBeforeInit(t *testing.T) {
	m := newMessagingModule("amqp://localhost")
	checks := m.HealthChecks()
	assert.Nil(t, checks, "should return nil health checks before Init")
}

func TestMessagingModule_CloseBeforeInit(t *testing.T) {
	m := newMessagingModule("amqp://localhost")
	err := m.Close(context.TODO())
	require.NoError(t, err, "Close before Init should not error")
}

func TestMessagingModule_PopulateBeforeInit(t *testing.T) {
	m := newMessagingModule("amqp://localhost")
	infra := &Infrastructure{}
	m.Populate(infra)
	assert.Nil(t, infra.Broker, "Broker should be nil before Init")
	assert.Nil(t, infra.Publisher, "Publisher should be nil before Init")
	assert.Nil(t, infra.Consumer, "Consumer should be nil before Init")
}

func TestMessagingModule_CriticalBrokerFlag(t *testing.T) {
	m := newMessagingModule("amqp://localhost")
	assert.False(t, m.criticalBroker)

	m.criticalBroker = true
	assert.True(t, m.criticalBroker)
}

func TestBuildIntegrationModules_Messaging(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithRabbitMQ("amqp://localhost")

	modules, _ := b.buildIntegrationModules()
	require.Len(t, modules, 1)
	assert.Equal(t, "rabbitmq", modules[0].Name())
}

func TestBuildIntegrationModules_MessagingCritical(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithRabbitMQ("amqp://localhost").
		WithCriticalBroker()

	modules, _ := b.buildIntegrationModules()
	require.Len(t, modules, 1)
	mm, ok := modules[0].(*messagingModule)
	require.True(t, ok)
	assert.True(t, mm.criticalBroker)
}

func TestBuildIntegrationModules_NoMessaging(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, _ := b.buildIntegrationModules()
	assert.Empty(t, modules)
}

func TestBuildIntegrationModules_Both(t *testing.T) {
	b := &Builder{
		name:      "test",
		version:   "v1",
		redisOpts: &goredis.Options{Addr: "localhost:6379"},
		mqURL:     "amqp://localhost",
	}

	modules, _ := b.buildIntegrationModules()
	require.Len(t, modules, 2)
	assert.Equal(t, "redis", modules[0].Name())
	assert.Equal(t, "rabbitmq", modules[1].Name())
}
