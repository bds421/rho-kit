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
	assert.Equal(t, "amqp", m.Name())
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
		name      string
		url       string
		opts      []Option
		wantPanic bool
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
// same URL because amqpbackend.WithoutTLS was not appended.
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

// TestModule_LoopbackPlaintext_PropagatesToBackend pins the loopback
// exemption contract: a bare amqp:// loopback URL skips the
// construction-time transport-safety panic (it "bypasses the check"
// per the docs), so the same plaintext URL must also be accepted by
// the backend dial path. Without threading the exemption into
// allowPlaintext, Init appends no amqpbackend.WithoutTLS and the lazy
// dial later rejects ("amqp URL must use amqps or WithTLS") or — with
// service TLS configured — silently upgrades amqp:// to amqps:// and
// fails the handshake against a plaintext local broker.
func TestModule_LoopbackPlaintext_PropagatesToBackend(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "loopback name", url: "amqp://localhost"},
		{name: "loopback name with port", url: "amqp://localhost:5672"},
		{name: "loopback IPv4", url: "amqp://127.0.0.1:5672"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Module(tc.url).(*messagingModule)
			assert.True(t, m.allowPlaintext,
				"loopback exemption must thread plaintext acceptance into the backend dial path")
		})
	}
}

// TestModule_LoopbackPlaintext_NotSetForAMQPS guards against
// over-broadening the loopback exemption: an amqps:// URL is already
// TLS and must not flip the plaintext flag via the loopback heuristic.
func TestModule_LoopbackPlaintext_NotSetForAMQPS(t *testing.T) {
	m := Module("amqps://localhost:5671").(*messagingModule)
	assert.False(t, m.allowPlaintext,
		"amqps:// is already TLS; the loopback heuristic must not set the plaintext flag")
}

// TestModule_LoopbackPlaintext_NotSetForProviderOnly guards that the
// static-URL loopback heuristic does not fire when only a provider is
// supplied: the provider's URL is unknown at construction time, so
// callers needing plaintext must opt in explicitly via WithoutTLS.
func TestModule_LoopbackPlaintext_NotSetForProviderOnly(t *testing.T) {
	provider := func(context.Context) (string, error) {
		return "amqp://localhost:5672/", nil
	}
	m := Module("", WithURLProvider(provider)).(*messagingModule)
	assert.False(t, m.allowPlaintext,
		"provider-only construction has no static URL to apply the loopback heuristic to")
}

// TestModule_LoopbackStaticURL_NotExemptWhenURLProviderPresent pins the
// review finding: a leftover amqp://localhost static URL must not set
// allowPlaintext when WithURLProvider is also supplied (the static URL
// is ignored at dial time, so the exemption would silently cover any
// non-loopback amqp:// URL the provider returns).
func TestModule_LoopbackStaticURL_NotExemptWhenURLProviderPresent(t *testing.T) {
	provider := func(context.Context) (string, error) {
		return "amqp://rabbit.prod.internal:5672/", nil
	}
	m := Module("amqp://localhost:5672", WithURLProvider(provider)).(*messagingModule)
	assert.False(t, m.allowPlaintext,
		"static loopback URL must not mint a global plaintext exemption when a URL provider is present")
	assert.NotNil(t, m.urlProvider)
}

// TestModule_StaticURLTransportCheck_SkippedWithProvider confirms that
// a non-loopback static amqp:// URL is ignored (not panicking) when a
// provider is supplied — the static URL is not used for dials.
func TestModule_StaticURLTransportCheck_SkippedWithProvider(t *testing.T) {
	provider := func(context.Context) (string, error) {
		return "amqps://rabbit.prod.internal:5671/", nil
	}
	assert.NotPanics(t, func() {
		_ = Module("amqp://rabbit.prod.example:5672", WithURLProvider(provider))
	})
}
