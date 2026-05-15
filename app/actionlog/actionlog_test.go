package actionlog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitactionlog "github.com/bds421/rho-kit/data/v2/actionlog"
	actionlogmem "github.com/bds421/rho-kit/data/v2/actionlog/memory"
)

func testLogger(t *testing.T) kitactionlog.Logger {
	t.Helper()
	signer, err := kitactionlog.NewCursorSigner(make([]byte, kitactionlog.MinCursorSigningKeyBytes))
	require.NoError(t, err)
	store := actionlogmem.New(signer)
	secrets := kitactionlog.NewStaticSecrets("k1", map[string][]byte{
		"k1": make([]byte, 32),
	})
	return kitactionlog.New(store, secrets)
}

func TestModule_Name(t *testing.T) {
	m := Module(testLogger(t))
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "actionlog", m.Name())
}

func TestModule_PanicsOnNil(t *testing.T) {
	assert.PanicsWithValue(t, "app/actionlog: Module requires a non-nil Logger", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesLogger(t *testing.T) {
	want := testLogger(t)
	m := Module(want)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Logger(infra)
	require.NotNil(t, got, "Logger(infra) must return the registered logger")
	assert.Same(t, want, got, "Logger(infra) returned a different instance")
}

func TestLogger_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Logger(infra))
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(testLogger(t))
	require.NoError(t, m.Stop(context.Background()))
}

func TestModule_HealthChecksEmpty(t *testing.T) {
	m := Module(testLogger(t))
	assert.Empty(t, m.HealthChecks())
}
