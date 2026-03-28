package app

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/runtime/lifecycle"
)

func TestHTTPClientModule_Name(t *testing.T) {
	m := newHTTPClientModule(false)
	assert.Equal(t, "httpclient", m.Name())
}

func TestHTTPClientModule_InitWithoutTracing(t *testing.T) {
	m := newHTTPClientModule(false)
	mc := testModuleContext(t)

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)
	assert.NotNil(t, m.Client(), "client should be initialized")
}

func TestHTTPClientModule_InitWithTracingModule(t *testing.T) {
	tm := newTracingModule(tracingConfigForTest())
	hcm := newHTTPClientModule(true)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	// Init tracing first.
	mc := ModuleContext{
		Logger:  logger,
		Runner:  runner,
		Config:  BaseConfig{},
		modules: map[string]Module{},
	}
	require.NoError(t, tm.Init(context.Background(), mc))
	mc.modules["tracing"] = tm

	// Init httpclient with tracing available.
	require.NoError(t, hcm.Init(context.Background(), mc))
	assert.NotNil(t, hcm.Client())
}

func TestHTTPClientModule_PopulateSetsFields(t *testing.T) {
	m := newHTTPClientModule(false)
	mc := testModuleContext(t)
	require.NoError(t, m.Init(context.Background(), mc))

	infra := &Infrastructure{}
	m.Populate(infra)
	assert.NotNil(t, infra.HTTPClient, "HTTPClient should be set")
	// ClientTLS is nil when no TLS is configured, which is fine.
}

func TestHTTPClientModule_ClientBeforeInit(t *testing.T) {
	m := newHTTPClientModule(false)
	assert.Nil(t, m.Client(), "Client should be nil before Init")
}

func TestHTTPClientModule_AlwaysPresent(t *testing.T) {
	// The httpclient module should be present even without any With*() calls.
	b := New("test", "v1", BaseConfig{})
	modules, _ := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
}

// testModuleContext creates a ModuleContext suitable for unit tests.
func testModuleContext(t *testing.T) ModuleContext {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return ModuleContext{
		Logger:  logger,
		Runner:  lifecycle.NewRunner(logger),
		Config:  BaseConfig{},
		modules: map[string]Module{},
	}
}
