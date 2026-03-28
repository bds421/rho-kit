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

	modules, _, _ := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "rabbitmq"), "rabbitmq module should be present")
}

func TestBuildIntegrationModules_MessagingCritical(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithRabbitMQ("amqp://localhost").
		WithCriticalBroker()

	modules, _, _ := b.buildIntegrationModules()
	var mm *messagingModule
	for _, m := range modules {
		if mqm, ok := m.(*messagingModule); ok {
			mm = mqm
			break
		}
	}
	require.NotNil(t, mm, "messaging module should be present")
	assert.True(t, mm.criticalBroker)
}

func TestBuildIntegrationModules_NoMessaging(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, _, _ := b.buildIntegrationModules()
	// httpclient is always present.
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
	assert.False(t, hasModule(modules, "rabbitmq"), "rabbitmq should not be present")
}

func TestBuildIntegrationModules_Both(t *testing.T) {
	b := &Builder{
		name:      "test",
		version:   "v1",
		redisOpts: &goredis.Options{Addr: "localhost:6379"},
		mqURL:     "amqp://localhost",
	}

	modules, _, _ := b.buildIntegrationModules()
	// Order: httpclient -> redis -> rabbitmq
	names := moduleNames(modules)
	assert.Equal(t, []string{"httpclient", "redis", "rabbitmq"}, names)
}
