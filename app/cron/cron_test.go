package cron

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

func newMC(t *testing.T, modules ...app.Module) app.ModuleContext {
	t.Helper()
	mc, err := app.TestModuleContext(modules...)
	require.NoError(t, err)
	return mc
}

func TestModule_Name(t *testing.T) {
	m := Module()
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "cron", m.Name())
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/cron: Module option must not be nil", func() {
		Module(nil)
	})
}

func TestModule_InitAndPopulateWithoutLeader(t *testing.T) {
	m := Module()
	mc := newMC(t, m)
	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Scheduler(infra)
	require.NotNil(t, got)
}

type stubLeaderModule struct {
	app.BaseModule
	leader bool
}

func (s *stubLeaderModule) Elector() leaderelection.Elector { return stubElector{leader: s.leader} }

type stubElector struct {
	leader bool
}

func (s stubElector) Run(_ context.Context, _ leaderelection.Callbacks) error { return nil }
func (s stubElector) IsLeader() bool                                          { return s.leader }

func TestModule_InitGatesOnLeader(t *testing.T) {
	stub := &stubLeaderModule{
		BaseModule: app.NewBaseModule(app.LeaderModuleName),
		leader:     true,
	}
	m := Module()
	mc := newMC(t, stub, m)
	require.NoError(t, m.Init(context.Background(), mc))

	// Hard to introspect kitcron.Scheduler internals from out here,
	// but the fact that Init returned nil with the leader stub
	// present means the lookup path executed successfully.
	infra := app.Infrastructure{}
	m.Populate(&infra)
	assert.NotNil(t, Scheduler(infra))
}

// nilElectorModule mimics a leader module that is registered but has
// not finished Init yet (Elector() still nil) — the shape of
// leader.PGAdvisoryFromPostgres when cron is registered first.
type nilElectorModule struct {
	app.BaseModule
}

func (nilElectorModule) Elector() leaderelection.Elector { return nil }

func TestModule_InitWithNilElectorFailsLoud(t *testing.T) {
	stub := nilElectorModule{BaseModule: app.NewBaseModule(app.LeaderModuleName)}
	m := Module()
	mc := newMC(t, &stub, m)

	err := m.Init(context.Background(), mc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Elector() is nil")
	assert.Contains(t, err.Error(), "register the leader module before cron")
}

func TestModule_InitUsesLeaderModuleNameConstant(t *testing.T) {
	// Pin the well-known name so a rename of app.LeaderModuleName without
	// updating app/leader.ModuleName would fail this cross-package contract.
	assert.Equal(t, "leader-election", app.LeaderModuleName)
}

func TestScheduler_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Scheduler(infra))
}

func TestModule_StopBeforeInit(t *testing.T) {
	m := Module()
	require.NoError(t, m.Stop(context.Background()))
}
