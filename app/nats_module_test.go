package app

import (
	"crypto/tls"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
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

func TestWithNATS_PanicsOnInvalidURL(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on invalid URL")
		}
	}()
	b.WithNATS(natsbackend.Config{URL: "nats://user:pass@localhost:4222"})
}

func TestWithNATS_RegistersOnBuilder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithNATS(natsbackend.Config{URL: "nats://localhost:4222"})
	if b.natsCfg == nil {
		t.Fatal("expected natsCfg to be set")
	}
	assert.Equal(t, "nats://localhost:4222", b.natsCfg.URL)
}

func TestWithNATS_ClonesConfig(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS10, NextProtos: []string{"h2"}, ServerName: "before.example"}
	extraOptions := []nats.Option{nats.NoReconnect()}

	b := New("test", "v1", BaseConfig{}).WithNATS(natsbackend.Config{
		URL:          "nats://localhost:4222",
		TLS:          tlsCfg,
		ExtraOptions: extraOptions,
	})
	tlsCfg.ServerName = "after.example"
	tlsCfg.NextProtos[0] = "http/1.1"
	extraOptions[0] = nil

	if b.natsCfg == nil {
		t.Fatal("expected natsCfg to be set")
	}
	assert.NotSame(t, tlsCfg, b.natsCfg.TLS)
	assert.Equal(t, uint16(tls.VersionTLS12), b.natsCfg.TLS.MinVersion)
	assert.Equal(t, []string{"h2"}, b.natsCfg.TLS.NextProtos)
	assert.Equal(t, "before.example", b.natsCfg.TLS.ServerName)
	assert.NotNil(t, b.natsCfg.ExtraOptions[0])
}

func TestNewNatsModule_ClonesConfig(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS10, NextProtos: []string{"h2"}, ServerName: "before.example"}
	extraOptions := []nats.Option{nats.NoReconnect()}

	m := newNatsModule(natsbackend.Config{
		URL:          "nats://localhost:4222",
		TLS:          tlsCfg,
		ExtraOptions: extraOptions,
	})
	tlsCfg.ServerName = "after.example"
	tlsCfg.NextProtos[0] = "http/1.1"
	extraOptions[0] = nil

	assert.NotSame(t, tlsCfg, m.cfg.TLS)
	assert.Equal(t, uint16(tls.VersionTLS12), m.cfg.TLS.MinVersion)
	assert.Equal(t, []string{"h2"}, m.cfg.TLS.NextProtos)
	assert.Equal(t, "before.example", m.cfg.TLS.ServerName)
	assert.NotNil(t, m.cfg.ExtraOptions[0])
}

func TestWithNATS_AndWithRabbitMQCoexist(t *testing.T) {
	// The two backends are independent — both can be configured.
	b := New("test", "v1", BaseConfig{}).
		WithRabbitMQ("amqp://localhost:5672").
		WithNATS(natsbackend.Config{URL: "nats://localhost:4222"})
	assert.NotEmpty(t, b.mqURL)
	assert.NotNil(t, b.natsCfg)
}

func TestBuildIntegrationModules_MessageSizeLimiterThreadsToNATS(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithMaxMessageBytes(64).
		WithRouteMaxMessageBytes("events", "large.event", 512).
		WithNATS(natsbackend.Config{URL: "nats://localhost:4222"})

	modules := b.buildIntegrationModules()
	var nm *natsModule
	for _, m := range modules {
		if candidate, ok := m.(*natsModule); ok {
			nm = candidate
			break
		}
	}
	if nm == nil {
		t.Fatal("expected nats module")
	}
	assert.Equal(t, 64, nm.messageSizeLimiter.LimitFor("events", "small.event"))
	assert.Equal(t, 512, nm.messageSizeLimiter.LimitFor("events", "large.event"))
}
