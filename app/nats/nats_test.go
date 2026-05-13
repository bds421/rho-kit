package nats

import (
	"crypto/tls"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestModule_PanicsOnEmptyURL(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty URL")
		}
	}()
	_ = Module(natsbackend.Config{})
}

func TestModule_PanicsOnInvalidURL(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on invalid URL")
		}
	}()
	_ = Module(natsbackend.Config{URL: "nats://user:pass@localhost:4222"})
}

func TestModule_Name(t *testing.T) {
	m := Module(natsbackend.Config{URL: "nats://localhost:4222"})
	assert.Equal(t, "nats", m.Name())
}

func TestModule_ClonesConfig(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS10, NextProtos: []string{"h2"}, ServerName: "before.example"}
	extraOptions := []nats.Option{nats.NoReconnect()}

	m := Module(natsbackend.Config{
		URL:          "nats://localhost:4222",
		TLS:          tlsCfg,
		ExtraOptions: extraOptions,
	}).(*natsModule)
	tlsCfg.ServerName = "after.example"
	tlsCfg.NextProtos[0] = "http/1.1"
	extraOptions[0] = nil

	assert.NotSame(t, tlsCfg, m.cfg.TLS)
	assert.Equal(t, uint16(tls.VersionTLS12), m.cfg.TLS.MinVersion)
	assert.Equal(t, []string{"h2"}, m.cfg.TLS.NextProtos)
	assert.Equal(t, "before.example", m.cfg.TLS.ServerName)
	assert.NotNil(t, m.cfg.ExtraOptions[0])
}

func TestModule_MessageSizeLimiterThreads(t *testing.T) {
	limiter := messaging.NewMessageSizeLimiter(64).WithRouteMaxBytes("events", "large.event", 512)
	m := Module(natsbackend.Config{URL: "nats://localhost:4222"}, WithMessageSizeLimiter(limiter)).(*natsModule)
	assert.Equal(t, 64, m.messageSizeLimiter.LimitFor("events", "small.event"))
	assert.Equal(t, 512, m.messageSizeLimiter.LimitFor("events", "large.event"))
}

func TestConnection_NilWhenAdapterNotRegistered(t *testing.T) {
	infra := app.TestInfrastructure()
	assert.Nil(t, Connection(infra))
	assert.Nil(t, Publisher(infra))
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	require.Panics(t, func() {
		_ = Module(natsbackend.Config{URL: "nats://localhost:4222"}, nil)
	})
}
