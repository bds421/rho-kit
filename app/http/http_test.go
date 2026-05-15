package http

import (
	"net/http"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kithttpx "github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
)

func TestModule_Name(t *testing.T) {
	m := Module()
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "http", m.Name())
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/http: Module option must not be nil", func() {
		Module(nil)
	})
}

func TestModule_DefaultsAreHardened(t *testing.T) {
	m := Module()
	p := m.(app.HTTPConfigProvider)

	assert.False(t, p.AllowPlaintext())
	assert.False(t, p.OptionalClientCerts())
	assert.False(t, p.AllowInternalNonLoopback())

	reload, active := p.ReloadingTLSOptions()
	assert.Nil(t, reload)
	assert.False(t, active)

	assert.Empty(t, p.TLSReloadSignals())
	assert.False(t, p.DisableDefaultStack())
	assert.Empty(t, p.StackOptions())
	assert.Empty(t, p.ServerOptions())
	assert.Nil(t, p.CustomReadiness())
}

func TestModule_WithoutTLS(t *testing.T) {
	p := Module(WithoutTLS()).(app.HTTPConfigProvider)
	assert.True(t, p.AllowPlaintext())
}

func TestModule_WithOptionalClientCertificates(t *testing.T) {
	p := Module(WithOptionalClientCertificates()).(app.HTTPConfigProvider)
	assert.True(t, p.OptionalClientCerts())
}

func TestModule_WithInternalNonLoopback(t *testing.T) {
	p := Module(WithInternalNonLoopback()).(app.HTTPConfigProvider)
	assert.True(t, p.AllowInternalNonLoopback())
}

func TestModule_WithReloadingTLS(t *testing.T) {
	p := Module(WithReloadingTLS()).(app.HTTPConfigProvider)
	_, active := p.ReloadingTLSOptions()
	assert.True(t, active)
}

func TestModule_WithTLSReloadOnSignal(t *testing.T) {
	p := Module(WithTLSReloadOnSignal(syscall.SIGHUP)).(app.HTTPConfigProvider)
	sigs := p.TLSReloadSignals()
	require.Len(t, sigs, 1)
	assert.Equal(t, syscall.SIGHUP, sigs[0])
}

func TestWithTLSReloadOnSignal_PanicsOnEmpty(t *testing.T) {
	assert.PanicsWithValue(t, "app/http: WithTLSReloadOnSignal requires at least one signal", func() {
		WithTLSReloadOnSignal()
	})
}

func TestWithTLSReloadOnSignal_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/http: WithTLSReloadOnSignal signal must not be nil", func() {
		WithTLSReloadOnSignal(os.Signal(nil))
	})
}

func TestModule_WithoutDefaultStack(t *testing.T) {
	p := Module(WithoutDefaultStack()).(app.HTTPConfigProvider)
	assert.True(t, p.DisableDefaultStack())
}

func TestModule_WithStackOptions(t *testing.T) {
	opt := stack.WithQuietPaths("/ready")
	p := Module(WithStackOptions(opt)).(app.HTTPConfigProvider)
	assert.Len(t, p.StackOptions(), 1)
}

func TestWithStackOptions_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/http: WithStackOptions option must not be nil", func() {
		WithStackOptions(nil)
	})
}

func TestModule_WithServerOption(t *testing.T) {
	opt := kithttpx.ServerOption(func(_ *http.Server) {})
	p := Module(WithServerOption(opt)).(app.HTTPConfigProvider)
	assert.Len(t, p.ServerOptions(), 1)
}

func TestWithServerOption_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/http: WithServerOption requires a non-nil option", func() {
		WithServerOption(nil)
	})
}

func TestModule_WithCustomReadiness(t *testing.T) {
	h := http.NotFoundHandler()
	p := Module(WithCustomReadiness(h)).(app.HTTPConfigProvider)
	assert.NotNil(t, p.CustomReadiness())
}

func TestWithCustomReadiness_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/http: WithCustomReadiness requires a non-nil handler", func() {
		WithCustomReadiness(nil)
	})
}
