package authz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitauthz "github.com/bds421/rho-kit/authz/v2"
)

type stubDecider struct{}

func (stubDecider) Allow(_ context.Context, _, _, _ string) error { return nil }

func TestModule_Name(t *testing.T) {
	m := Module(stubDecider{})
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "authz", m.Name())
}

func TestModule_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/authz: Module requires a non-nil decider", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesDecider(t *testing.T) {
	want := stubDecider{}
	m := Module(want)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Decider(infra)
	require.NotNil(t, got, "Decider(infra) must return the registered decider")
	_, ok := got.(stubDecider)
	assert.True(t, ok, "Decider returned wrong concrete type: %T", got)

	// Capability interface should be satisfied.
	var _ kitauthz.Decider = stubDecider{}
}

func TestDecider_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Decider(infra),
		"Decider(infra) must return nil when no authz module was registered")
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(stubDecider{})
	require.NoError(t, m.Stop(context.Background()))
}

func TestModule_HealthChecksEmpty(t *testing.T) {
	m := Module(stubDecider{})
	assert.Empty(t, m.HealthChecks())
}
