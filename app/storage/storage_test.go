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

// TestModule_ClonesOptionSlices verifies post-construction mutation of
// the option-captured slices cannot affect the module state. This was
// the wave-99 fix; pin the contract here so a regression shows up
// immediately.
//
// The aliasing vector: an Option closure is handed the *config and can
// keep a reference to the slice headers it appended to. Once Module
// returns, the closure mutates those slices. Without the defensive
// clone in Module, the module's cfg shares the same backing arrays, so
// the mutations would leak into the live module state. Removing the
// clone at storage.go therefore turns this test red.
func TestModule_ClonesOptionSlices(t *testing.T) {
	uploads := membackend.New()
	check := health.DependencyCheck{Name: "uploads", Check: func(context.Context) string { return "ok" }}

	// A custom Option that performs the standard appends AND captures the
	// SLICE HEADERS it built (the backing arrays), not the *config. This
	// is the aliasing a caller-supplied Option enables: the closure keeps
	// references to the same backing arrays the module will hold unless
	// Module clones them. Capturing the headers (rather than &cfg) is what
	// survives Module's clone-and-reassign — &cfg would observe the post-
	// clone copies and prove nothing.
	var capturedNamed []namedSpec
	var capturedChecks []health.DependencyCheck
	capture := func(c *config) {
		c.named = append(c.named, namedSpec{name: "uploads", backend: uploads})
		c.checks = append(c.checks, check)
		capturedNamed = c.named
		capturedChecks = c.checks
	}

	m := Module(membackend.New(), capture)
	require.Len(t, capturedChecks, 1)
	require.Len(t, capturedNamed, 1)

	// Overwrite the captured backing-array elements in place, after
	// construction. In-place writes alias the shared backing array
	// regardless of slice capacity, so they deterministically leak when
	// Module did not clone. With the clone (the wave-99 fix) the module
	// holds an independent copy and these writes are invisible.
	capturedChecks[0] = health.DependencyCheck{Name: "rogue", Check: func(context.Context) string { return "leaked" }}
	capturedNamed[0] = namedSpec{name: "rogue", backend: membackend.New()}

	got := m.HealthChecks()
	require.Len(t, got, 1)
	assert.Equal(t, "uploads", got[0].Name, "in-place write to the captured checks slice leaked into the module")

	// The named-backend set must likewise be unaffected: only "uploads"
	// should be registered on the manager Init builds.
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))
	infra := app.Infrastructure{}
	m.Populate(&infra)
	mgr := Manager(infra)
	require.NotNil(t, mgr)
	assert.True(t, mgr.Has("uploads"), "in-place write to the captured named slice leaked into the module")
	assert.False(t, mgr.Has("rogue"), "in-place write to the captured named slice leaked into the module")
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
