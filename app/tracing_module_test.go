package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/tracing"
)

func TestTracingModule_Name(t *testing.T) {
	m := newTracingModule(tracing.Config{})
	assert.Equal(t, "tracing", m.Name())
}

func TestTracingModule_InitNoopEndpoint(t *testing.T) {
	m := newTracingModule(tracing.Config{ServiceName: "test"})
	mc := testModuleContext(t)

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)
	// Noop provider is considered active (Init succeeds without error).
	assert.True(t, m.Active())
	assert.Nil(t, m.HealthChecks(), "no health checks for noop tracing")

	// Close should not error.
	require.NoError(t, m.Close(context.Background()))
}

func TestTracingModule_ActiveDefaultFalse(t *testing.T) {
	m := newTracingModule(tracing.Config{})
	assert.False(t, m.Active(), "Active should be false before Init")
}

func TestTracingModule_CloseBeforeInit(t *testing.T) {
	m := newTracingModule(tracing.Config{})
	err := m.Close(context.Background())
	require.NoError(t, err, "Close before Init should not error")
}

func TestBuildIntegrationModules_Tracing(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"})

	modules, _, _ := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "tracing"), "tracing module should be present")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
}

func TestBuildIntegrationModules_NoTracing(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules, _, _ := b.buildIntegrationModules()
	assert.False(t, hasModule(modules, "tracing"), "tracing should not be present without config")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
}

func TestBuildIntegrationModules_TracingOrder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"}).
		WithJWT("https://example.com/.well-known/jwks.json")

	modules, _, _ := b.buildIntegrationModules()
	names := moduleNames(modules)
	assert.Equal(t, []string{"tracing", "httpclient", "jwt"}, names)
}
