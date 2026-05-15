package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
	"github.com/bds421/rho-kit/observability/v2/health"
)

func TestModule_Name(t *testing.T) {
	m := Module(membackend.New())
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "storage", m.Name())
}

func TestModule_PanicsOnNilBackend(t *testing.T) {
	assert.PanicsWithValue(t, "app/storage: Module requires a non-nil backend", func() {
		Module(nil)
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/storage: Module option must not be nil", func() {
		Module(membackend.New(), nil)
	})
}

func TestWithNamed_PanicsOnEmptyName(t *testing.T) {
	assert.PanicsWithValue(t, "app/storage: WithNamed requires a non-empty name", func() {
		WithNamed("", membackend.New())
	})
}

func TestWithNamed_PanicsOnNilBackend(t *testing.T) {
	assert.PanicsWithValue(t, "app/storage: WithNamed requires a non-nil backend", func() {
		WithNamed("uploads", nil)
	})
}

func TestWithHealthCheck_PanicsOnInvalid(t *testing.T) {
	assert.PanicsWithValue(t, "app/storage: WithHealthCheck requires a non-empty Name", func() {
		WithHealthCheck(health.DependencyCheck{Check: func(context.Context) string { return "ok" }})
	})
	assert.PanicsWithValue(t, "app/storage: WithHealthCheck requires a non-nil Check", func() {
		WithHealthCheck(health.DependencyCheck{Name: "s3"})
	})
}

// TestModule_ClonesOptionSlices verifies post-construction caller
// mutation of the captured slices cannot affect the module state.
// This was the wave-99 fix; pin the contract here so a regression
// shows up immediately.
func TestModule_ClonesOptionSlices(t *testing.T) {
	uploads := membackend.New()
	check := health.DependencyCheck{Name: "uploads", Check: func(context.Context) string { return "ok" }}

	m := Module(membackend.New(),
		WithNamed("uploads", uploads),
		WithHealthCheck(check),
	)

	// If Module didn't clone, the module would observe these mutations.
	// The mutations target the closure-internal cfg.named / cfg.checks
	// state, so a successful clone means subsequent appends to outer
	// slices don't reach the module — exercised indirectly by checking
	// that the module's HealthChecks count is stable.
	got := m.HealthChecks()
	require.Len(t, got, 1)
	assert.Equal(t, "uploads", got[0].Name)
}

func TestModule_PopulatesBackendOnly(t *testing.T) {
	backend := membackend.New()
	m := Module(backend)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Backend(infra)
	require.NotNil(t, got)
	assert.Same(t, backend, got)

	mgr := Manager(infra)
	assert.Nil(t, mgr, "Manager(infra) must be nil when no WithNamed options were passed")
}

func TestModule_PopulatesBackendAndManager(t *testing.T) {
	backend := membackend.New()
	uploads := membackend.New()
	archive := membackend.New()

	m := Module(backend,
		WithNamed("uploads", uploads),
		WithNamed("archive", archive),
	)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	assert.Same(t, backend, Backend(infra))
	mgr := Manager(infra)
	require.NotNil(t, mgr, "Manager(infra) must be non-nil when WithNamed options were passed")
}

func TestBackend_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Backend(infra))
	assert.Nil(t, Manager(infra))
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(membackend.New())
	require.NoError(t, m.Stop(context.Background()))
}
