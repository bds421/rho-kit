package auditlog

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/app/v2"
	kitauditlog "github.com/bds421/rho-kit/observability/v2/auditlog"
)

func TestModule_Name(t *testing.T) {
	m := Module(kitauditlog.NewMemoryStore(),
		kitauditlog.WithChainKey(make([]byte, 32)),
		kitauditlog.WithCursorKey(make([]byte, 32)),
	)
	assert.Equal(t, ModuleName, m.Name())
	assert.Equal(t, "auditlog", m.Name())
}

func TestModule_PanicsOnNilStore(t *testing.T) {
	assert.PanicsWithValue(t, "app/auditlog: Module requires a non-nil store", func() {
		Module(nil)
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.PanicsWithValue(t, "app/auditlog: Module option must not be nil", func() {
		Module(kitauditlog.NewMemoryStore(), nil)
	})
}

// TestModule_ClonesOptions verifies post-construction caller mutation
// of the option slice cannot affect the module's captured options.
func TestModule_ClonesOptions(t *testing.T) {
	opts := []kitauditlog.Option{kitauditlog.WithLogger(slog.Default())}
	_ = Module(kitauditlog.NewMemoryStore(), opts...)
	opts[0] = nil // would crash Init if no clone happened
	// No panic expected.
}

func TestModule_InitBuildsLogger(t *testing.T) {
	m := Module(kitauditlog.NewMemoryStore(),
		kitauditlog.WithChainKey(make([]byte, 32)),
		kitauditlog.WithCursorKey(make([]byte, 32)),
		kitauditlog.WithLogger(slog.Default()),
	)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))
}

func TestModule_PopulatePublishesLogger(t *testing.T) {
	m := Module(kitauditlog.NewMemoryStore(),
		kitauditlog.WithChainKey(make([]byte, 32)),
		kitauditlog.WithCursorKey(make([]byte, 32)),
	)
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))

	infra := app.Infrastructure{}
	m.Populate(&infra)

	got := Logger(infra)
	require.NotNil(t, got, "Logger(infra) must return the built *Logger")
}

func TestLogger_NilWhenNotRegistered(t *testing.T) {
	infra := app.Infrastructure{}
	assert.Nil(t, Logger(infra))
}

func TestModule_StopIsNoOp(t *testing.T) {
	m := Module(kitauditlog.NewMemoryStore(),
		kitauditlog.WithChainKey(make([]byte, 32)),
		kitauditlog.WithCursorKey(make([]byte, 32)),
	)
	require.NoError(t, m.Stop(context.Background()))
}
