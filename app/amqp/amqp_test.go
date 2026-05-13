package amqp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
)

func TestModule_PanicsOnEmptyURL(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for empty URL")
		assert.Contains(t, r, "non-empty URL or WithURLProvider")
	}()
	_ = Module("")
}

func TestWithURLProvider_PanicsOnNil(t *testing.T) {
	require.Panics(t, func() {
		_ = WithURLProvider(nil)
	})
}

func TestModule_Name(t *testing.T) {
	m := Module("amqp://localhost")
	assert.Equal(t, "rabbitmq", m.Name())
}

func TestModule_AllowsLoopbackPlaintext(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module("amqp://localhost")
	})
	assert.NotPanics(t, func() {
		_ = Module("amqp://127.0.0.1:5672")
	})
}

func TestModule_AllowsAMQPS(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module("amqps://rabbit.example.com:5671")
	})
}

func TestModule_PanicsOnNonLoopbackPlaintext(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for plaintext non-loopback")
		assert.Contains(t, r, "amqps://")
	}()
	_ = Module("amqp://rabbit.example.com:5672")
}

func TestModule_AllowsPlaintextWithOptOut(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = Module("amqp://rabbit.example.com:5672", WithoutTLS())
	})
}

func TestModule_TLSSafety_Table(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		opts         []Option
		wantPanic    bool
	}{
		{name: "amqp loopback name", url: "amqp://localhost"},
		{name: "amqp loopback IPv4", url: "amqp://127.0.0.1:5672"},
		{name: "amqps prod", url: "amqps://rabbit.prod.example:5671"},
		{name: "amqp prod hostname panics", url: "amqp://rabbit.prod.example:5672", wantPanic: true},
		{name: "amqp prod IP panics", url: "amqp://10.0.0.5:5672", wantPanic: true},
		{name: "amqp prod with WithoutTLS opt-out", url: "amqp://rabbit.prod.example:5672", opts: []Option{WithoutTLS()}},
		{name: "amqp 0.0.0.0 is not loopback", url: "amqp://0.0.0.0:5672", wantPanic: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := func() { _ = Module(tc.url, tc.opts...) }
			if tc.wantPanic {
				assert.Panics(t, fn)
			} else {
				assert.NotPanics(t, fn)
			}
		})
	}
}

func TestWithURLProvider_AllowsModuleWithoutURL(t *testing.T) {
	provider := func(context.Context) (string, error) {
		return "amqp://user:rotated@localhost:5672/", nil
	}
	assert.NotPanics(t, func() {
		_ = Module("", WithURLProvider(provider))
	})
}

func TestModule_HealthChecksBeforeInit(t *testing.T) {
	m := Module("amqp://localhost").(*messagingModule)
	assert.Nil(t, m.HealthChecks())
}

func TestModule_StopBeforeInit(t *testing.T) {
	m := Module("amqp://localhost").(*messagingModule)
	require.NoError(t, m.Stop(context.Background()))
}

func TestConnection_NilWhenAdapterNotRegistered(t *testing.T) {
	infra := app.TestInfrastructure()
	assert.Nil(t, Connection(infra))
	assert.Nil(t, Publisher(infra))
	assert.Nil(t, Consumer(infra))
}

// TestModule_WithoutTLS_PropagatesToBackend pins H-004: the app-level
// WithoutTLS opt-out must reach the backend Connect path. Before the
// fix, Module accepted amqp:// loopback but Init still rejected the
// same URL because amqpbackend.WithAllowPlaintext was not appended.
func TestModule_WithoutTLS_PropagatesToBackend(t *testing.T) {
	m := Module("amqp://localhost:5672", WithoutTLS()).(*messagingModule)
	assert.True(t, m.allowPlaintext,
		"WithoutTLS must be threaded into messagingModule so Init/Connect see it")
}

// TestModule_WithoutTLS_AllowsURLProviderPath pins H-004 for the
// URL-provider case: Module construction skips the static URL check
// when only a provider is supplied, but the backend still needs the
// allow-plaintext flag to accept what the provider returns.
func TestModule_WithoutTLS_AllowsURLProviderPath(t *testing.T) {
	provider := func(context.Context) (string, error) {
		return "amqp://broker.dev.example:5672/", nil
	}
	m := Module("", WithURLProvider(provider), WithoutTLS()).(*messagingModule)
	assert.True(t, m.allowPlaintext)
	assert.NotNil(t, m.urlProvider)
}
