package signedrequest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

func testResolver() signedrequest.KeyResolver {
	return func(_ context.Context, _ string) ([]byte, error) {
		return make([]byte, 32), nil
	}
}

func testStore() signedrequest.NonceStore {
	return signedrequest.NewMemoryNonceStore(10 * time.Minute)
}

func TestModule_Name(t *testing.T) {
	m := Module(testResolver(), testStore())
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "signed-request", m.Name())
}

func TestModule_PanicsOnNilResolver(t *testing.T) {
	assert.PanicsWithValue(t, "app/signedrequest: Module requires a non-nil KeyResolver", func() {
		Module(nil, testStore())
	})
}

func TestModule_PanicsOnNilStore(t *testing.T) {
	assert.PanicsWithValue(t, "app/signedrequest: Module requires a non-nil NonceStore (no-store means trivially-replayable signatures)", func() {
		Module(testResolver(), nil)
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/signedrequest: Module option must not be nil", func() {
		Module(testResolver(), testStore(), nil)
	})
}

func TestModule_PublicMiddlewareEmptyBeforeInit(t *testing.T) {
	m := Module(testResolver(), testStore())
	mi := m.(app.MiddlewareInstaller)
	assert.Empty(t, mi.PublicMiddleware())
}

func TestModule_InitInstallsAtPhaseSignedRequest(t *testing.T) {
	m := Module(testResolver(), testStore())
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	mi := m.(app.MiddlewareInstaller)
	mws := mi.PublicMiddleware()
	require.Len(t, mws, 1)
	assert.Equal(t, app.PhaseSignedRequest, mws[0].Phase)
	require.NotNil(t, mws[0].Func)
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(testResolver(), testStore())
	require.NoError(t, m.Stop(context.Background()))
}

func TestModule_HealthChecksEmpty(t *testing.T) {
	m := Module(testResolver(), testStore())
	assert.Empty(t, m.HealthChecks())
}
