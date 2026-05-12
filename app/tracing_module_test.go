package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/tracing"
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
	require.NoError(t, m.Stop(context.Background()))
}

func TestTracingModuleEnabledLogDoesNotExposeEndpoint(t *testing.T) {
	const endpoint = "collector-secret.internal:4317"
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mc := testModuleContext(t)
	mc.Logger = logger
	m := newTracingModule(tracing.Config{
		ServiceName: "test",
		Endpoint:    endpoint,
		Insecure:    true,
		InitTimeout: time.Millisecond,
	})

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	rendered := buf.String()
	for _, forbidden := range []string{endpoint, "collector-secret.internal"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("tracing enabled log leaked %q in %s", forbidden, rendered)
		}
	}
	assert.Contains(t, rendered, "endpoint_configured=true")
}

func TestTracingModule_ActiveDefaultFalse(t *testing.T) {
	m := newTracingModule(tracing.Config{})
	assert.False(t, m.Active(), "Active should be false before Init")
}

func TestTracingModule_HealthChecksReturnsDetachedSlice(t *testing.T) {
	m := newTracingModule(tracing.Config{})
	m.healthChecks_ = []health.DependencyCheck{{Name: "tracing"}}

	checks := m.HealthChecks()
	require.Len(t, checks, 1)
	checks[0].Name = "mutated"

	fresh := m.HealthChecks()
	require.Len(t, fresh, 1)
	assert.Equal(t, "tracing", fresh[0].Name)
}

func TestTracingModule_StopBeforeInit(t *testing.T) {
	m := newTracingModule(tracing.Config{})
	err := m.Stop(context.Background())
	require.NoError(t, err, "Stop before Init should not error")
}

func TestBuildIntegrationModules_Tracing(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"})

	modules := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "tracing"), "tracing module should be present")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
}

func TestBuildIntegrationModules_NoTracing(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	modules := b.buildIntegrationModules()
	assert.False(t, hasModule(modules, "tracing"), "tracing should not be present without config")
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
}

func TestBuildIntegrationModules_TracingOrder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).
		WithTracing(tracing.Config{ServiceName: "test"}).
		WithJWT("https://example.com/.well-known/jwks.json")

	modules := b.buildIntegrationModules()
	names := moduleNames(modules)
	assert.Equal(t, []string{"tracing", "httpclient", "jwt"}, names)
}
