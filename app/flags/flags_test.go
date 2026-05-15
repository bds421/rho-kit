package flags

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitflags "github.com/bds421/rho-kit/flags/v2"
)

func TestModule_Name(t *testing.T) {
	m := Module(kitflags.NewMemoryProvider())
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "flags", m.Name())
}

func TestModule_PanicsOnNilProvider(t *testing.T) {
	assert.PanicsWithValue(t, "app/flags: Module requires a non-nil Provider", func() {
		Module(nil)
	})
}

func TestModule_PopulatePublishesClient(t *testing.T) {
	m := Module(kitflags.NewMemoryProvider())
	mc := app.ModuleContext{ServiceName: "test", Logger: slog.Default()}
	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Client(infra)
	require.NotNil(t, got, "Client(infra) must return the wrapped *kitflags.Client")
}

func TestClient_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Client(infra))
}
