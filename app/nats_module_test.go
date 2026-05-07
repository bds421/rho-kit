package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend"
)

func TestNewNatsModule_PanicsOnEmptyURL(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty URL")
		}
	}()
	newNatsModule(natsbackend.Config{})
}

func TestWithNATS_PanicsOnEmptyURL(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty URL")
		}
	}()
	b.WithNATS(natsbackend.Config{})
}

func TestWithNATS_RegistersOnBuilder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithNATS(natsbackend.Config{URL: "nats://localhost:4222"})
	if b.natsCfg == nil {
		t.Fatal("expected natsCfg to be set")
	}
	assert.Equal(t, "nats://localhost:4222", b.natsCfg.URL)
}

func TestWithNATS_AndWithRabbitMQCoexist(t *testing.T) {
	// The two backends are independent — both can be configured.
	b := New("test", "v1", BaseConfig{}).
		WithRabbitMQ("amqp://localhost:5672").
		WithNATS(natsbackend.Config{URL: "nats://localhost:4222"})
	assert.NotEmpty(t, b.mqURL)
	assert.NotNil(t, b.natsCfg)
}
