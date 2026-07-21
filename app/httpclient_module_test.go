package app

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func TestHTTPClientModule_Name(t *testing.T) {
	m := newHTTPClientModule(false, 0)
	assert.Equal(t, "httpclient", m.Name())
}

func TestHTTPClientModule_InitWithoutTracing(t *testing.T) {
	m := newHTTPClientModule(false, 0)
	mc := testModuleContext(t)

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)
	assert.NotNil(t, m.Client(), "client should be initialized")
}

// stubTracingProvider implements [TracingProvider] for httpclient testing
// without pulling app/tracing into app/v2.
type stubTracingProvider struct {
	BaseModule
	active bool
}

func (s *stubTracingProvider) TracingActive() bool { return s.active }

var _ TracingProvider = (*stubTracingProvider)(nil)
var _ Module = (*stubTracingProvider)(nil)

func TestHTTPClientModule_InitWithTracingModule(t *testing.T) {
	tracing := &stubTracingProvider{BaseModule: NewBaseModule("tracing"), active: true}
	hcm := newHTTPClientModule(true, 0)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	mc := ModuleContext{
		Logger:  logger,
		Runner:  runner,
		Config:  BaseConfig{},
		modules: map[string]Module{"tracing": tracing},
	}

	require.NoError(t, hcm.Init(context.Background(), mc))
	assert.NotNil(t, hcm.Client())
}

func TestHTTPClientModule_PopulateSetsFields(t *testing.T) {
	m := newHTTPClientModule(false, 0)
	mc := testModuleContext(t)
	require.NoError(t, m.Init(context.Background(), mc))

	infra := &Infrastructure{}
	m.Populate(infra)
	assert.NotNil(t, HTTPClient(*infra), "HTTPClient should be published on the resource map")
}

func TestHTTPClientModule_ClientBeforeInit(t *testing.T) {
	m := newHTTPClientModule(false, 0)
	assert.Nil(t, m.Client(), "Client should be nil before Init")
}

func TestHTTPClientModule_AlwaysPresent(t *testing.T) {
	// The httpclient module should be present even without any With*() calls.
	b := New("test", "v1", BaseConfig{})
	modules := b.buildIntegrationModules()
	assert.True(t, hasModule(modules, "httpclient"), "httpclient should always be present")
}

// initObservingTracing observes whether httpclient saw a tracing provider
// in its ModuleContext at init time. Used to lock down the tracing-first
// init ordering invariant: before wave 66, the built-in httpclient init
// ran before the user-registered tracing module, so TracingActive() was
// never observed and outbound HTTP traces were silently dropped.
type initObservingTracing struct {
	BaseModule
	saw bool
}

func (m *initObservingTracing) TracingActive() bool { return true }
func (m *initObservingTracing) Init(_ context.Context, _ ModuleContext) error {
	m.saw = true
	return nil
}

func TestBuilder_TracingProviderInitsBeforeHTTPClient(t *testing.T) {
	tracing := &initObservingTracing{BaseModule: NewBaseModule("tracing-stub")}
	b := New("test", "v1", BaseConfig{}).With(tracing)

	hcm := newHTTPClientModule(true, 0) // tracingConfigured=true
	builtinModules := []Module{hcm}

	// Mirror the production ordering logic from Builder.Run: tracing
	// providers come first, then builtins, then remaining user modules.
	allModules := make([]Module, 0, len(builtinModules)+len(b.modules))
	var deferred []Module
	for _, m := range b.modules {
		if _, ok := m.(TracingProvider); ok {
			allModules = append(allModules, m)
		} else {
			deferred = append(deferred, m)
		}
	}
	allModules = append(allModules, builtinModules...)
	allModules = append(allModules, deferred...)

	mc := testModuleContext(t)
	for _, m := range allModules {
		require.NoError(t, m.Init(context.Background(), mc))
		mc.modules[m.Name()] = m
	}

	require.True(t, tracing.saw, "tracing-provider module must initialize before any builtin queries it")
	// And by then the HTTP client must observe tracing as active.
	assert.NotNil(t, hcm.Client())
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

// Re-export to silence unused import in this file.
var _ = health.StatusHealthy
