package approval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitapproval "github.com/bds421/rho-kit/data/v2/approval"
	approvalmem "github.com/bds421/rho-kit/data/v2/approval/memory"
)

func testStore(t *testing.T) kitapproval.Store {
	t.Helper()
	signer, err := kitapproval.NewCursorSigner(make([]byte, kitapproval.MinCursorSigningKeyBytes))
	require.NoError(t, err)
	return approvalmem.New(signer)
}

func TestModule_Name(t *testing.T) {
	m := Module(testStore(t))
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "approval", m.Name())
}

func TestModule_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/approval: Module requires a non-nil Store", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesStore(t *testing.T) {
	want := testStore(t)
	m := Module(want)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Store(infra)
	require.NotNil(t, got, "Store(infra) must return the registered store")
	assert.Same(t, want, got, "Store(infra) returned a different store instance")
}

func TestStore_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Store(infra),
		"Store(infra) must return nil when no approval module was registered")
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(testStore(t))
	require.NoError(t, m.Stop(context.Background()))
}

func TestModule_HealthChecksEmpty(t *testing.T) {
	m := Module(testStore(t))
	assert.Empty(t, m.HealthChecks())
}
