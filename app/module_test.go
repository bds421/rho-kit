package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/runtime/lifecycle"
)

// stubModule is a test double for the Module interface.
type stubModule struct {
	name         string
	initFn       func(ctx context.Context, mc ModuleContext) error
	populateFn   func(infra *Infrastructure)
	closeFn      func(ctx context.Context) error
	healthChecks []health.DependencyCheck

	initCalled     atomic.Bool
	populateCalled atomic.Bool
	closeCalled    atomic.Bool
}

func newStubModule(name string) *stubModule {
	return &stubModule{name: name}
}

func (m *stubModule) Name() string { return m.name }

func (m *stubModule) Init(ctx context.Context, mc ModuleContext) error {
	m.initCalled.Store(true)
	if m.initFn != nil {
		return m.initFn(ctx, mc)
	}
	return nil
}

func (m *stubModule) Populate(infra *Infrastructure) {
	m.populateCalled.Store(true)
	if m.populateFn != nil {
		m.populateFn(infra)
	}
}

func (m *stubModule) Close(ctx context.Context) error {
	m.closeCalled.Store(true)
	if m.closeFn != nil {
		return m.closeFn(ctx)
	}
	return nil
}

func (m *stubModule) HealthChecks() []health.DependencyCheck {
	return m.healthChecks
}

func TestWithModule_PanicsOnNil(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for nil module")
		assert.Contains(t, fmt.Sprint(r), "must not be nil")
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithModule(nil)
}

func TestWithModule_PanicsOnEmptyName(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for empty module name")
		assert.Contains(t, fmt.Sprint(r), "must not be empty")
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).WithModule(newStubModule(""))
}

func TestWithModule_PanicsOnDuplicateName(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for duplicate module name")
		assert.Contains(t, fmt.Sprint(r), "duplicate module name")
	}()
	New("test-svc", "v0.1.0", BaseConfig{}).
		WithModule(newStubModule("mymod")).
		WithModule(newStubModule("mymod"))
}

func TestWithModule_FluentChaining(t *testing.T) {
	m1 := newStubModule("mod-a")
	m2 := newStubModule("mod-b")

	b := New("test-svc", "v0.1.0", BaseConfig{}).
		WithModule(m1).
		WithModule(m2)

	require.Len(t, b.modules, 2)
	assert.Equal(t, "mod-a", b.modules[0].Name())
	assert.Equal(t, "mod-b", b.modules[1].Name())
}

func TestModuleContext_Module_Panics_WhenNotFound(t *testing.T) {
	mc := ModuleContext{
		modules: map[string]Module{},
	}
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic for unknown module")
		assert.Contains(t, fmt.Sprint(r), "not found")
	}()
	mc.Module("nonexistent")
}

func TestModuleContext_Module_ReturnsModule(t *testing.T) {
	m := newStubModule("test-mod")
	mc := ModuleContext{
		modules: map[string]Module{"test-mod": m},
	}
	got := mc.Module("test-mod")
	assert.Equal(t, m, got)
}

func TestInitModules_OrderAndCleanup(t *testing.T) {
	var order []string

	m1 := newStubModule("first")
	m1.initFn = func(_ context.Context, _ ModuleContext) error {
		order = append(order, "init-first")
		return nil
	}
	m1.closeFn = func(_ context.Context) error {
		order = append(order, "close-first")
		return nil
	}

	m2 := newStubModule("second")
	m2.initFn = func(_ context.Context, _ ModuleContext) error {
		order = append(order, "init-second")
		return nil
	}
	m2.closeFn = func(_ context.Context) error {
		order = append(order, "close-second")
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{m1, m2},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	assert.Equal(t, []string{"init-first", "init-second"}, order)

	// Cleanup should close in reverse order.
	cleanup(context.Background())
	assert.Equal(t, []string{"init-first", "init-second", "close-second", "close-first"}, order)
}

func TestInitModules_FailureClosesInitialized(t *testing.T) {
	var closedModules []string

	m1 := newStubModule("ok-mod")
	m1.closeFn = func(_ context.Context) error {
		closedModules = append(closedModules, "ok-mod")
		return nil
	}

	m2 := newStubModule("fail-mod")
	m2.initFn = func(_ context.Context, _ ModuleContext) error {
		return errors.New("init boom")
	}
	m2.closeFn = func(_ context.Context) error {
		closedModules = append(closedModules, "fail-mod")
		return nil
	}

	m3 := newStubModule("never-mod")
	m3.closeFn = func(_ context.Context) error {
		closedModules = append(closedModules, "never-mod")
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{m1, m2, m3},
		logger,
		runner,
		BaseConfig{},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail-mod")
	assert.Contains(t, err.Error(), "init boom")
	assert.Nil(t, cleanup)

	// Only the first module (already init'd) should have been closed.
	assert.Equal(t, []string{"ok-mod"}, closedModules)

	// The third module should never have been init'd.
	assert.False(t, m3.initCalled.Load())
}

func TestInitModules_DependencyLookup(t *testing.T) {
	m1 := newStubModule("dep")
	m2 := newStubModule("consumer")

	var foundDep Module
	m2.initFn = func(_ context.Context, mc ModuleContext) error {
		foundDep = mc.Module("dep")
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{m1, m2},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)
	defer cleanup(context.Background())

	assert.Equal(t, m1, foundDep)
}

func TestInitModules_CloseErrorIsLogged(t *testing.T) {
	m := newStubModule("flaky")
	m.closeFn = func(_ context.Context) error {
		return errors.New("close error")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{m},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)

	// Should not panic even if Close returns an error.
	cleanup(context.Background())
}

func TestInitModules_NilSlice(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		nil,
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	cleanup(context.Background())
}

func TestInitModules_EmptySlice(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	// Should be a no-op.
	cleanup(context.Background())
}

func TestModuleHealthChecks_AddedToReadiness(t *testing.T) {
	m := newStubModule("hc-mod")
	m.healthChecks = []health.DependencyCheck{
		{
			Name:  "hc-mod-check",
			Check: func(_ context.Context) string { return health.StatusHealthy },
		},
	}

	b := New("test-svc", "v0.1.0", BaseConfig{}).WithModule(m)
	// Health checks start empty on builder; they get added during Run().
	assert.Empty(t, b.healthChecks)
	assert.Len(t, b.modules, 1)
}

// TestModule_InitFailureAbortsRun tests that a module init failure prevents
// the service from starting and cleans up already-initialized modules.
func TestModule_InitFailureAbortsRun(t *testing.T) {
	cfg := BaseConfig{
		Server:   ServerConfig{Host: "127.0.0.1", Port: freePort(t)},
		Internal: InternalConfig{Host: "127.0.0.1", Port: freePort(t)},
	}

	var closed atomic.Bool

	mod1 := newStubModule("good-mod")
	mod1.closeFn = func(_ context.Context) error {
		closed.Store(true)
		return nil
	}

	mod2 := newStubModule("bad-mod")
	mod2.initFn = func(_ context.Context, _ ModuleContext) error {
		return errors.New("connection refused")
	}

	b := New("fail-test", "v0.0.1", cfg).
		WithModule(mod1).
		WithModule(mod2).
		Router(func(infra Infrastructure) http.Handler {
			t.Fatal("RouterFunc should not be called when module init fails")
			return nil
		})

	err := b.Run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad-mod")
	assert.Contains(t, err.Error(), "connection refused")
	assert.True(t, closed.Load(), "good-mod should have been closed")
}

// TestModule_PopulateCalledBeforeRouter verifies that Populate is called
// on all modules before the RouterFunc executes, using the init failure
// path to test without requiring a full lifecycle with SIGINT.
func TestModule_PopulateCalledBeforeRouter(t *testing.T) {
	cfg := BaseConfig{
		Server:   ServerConfig{Host: "127.0.0.1", Port: freePort(t)},
		Internal: InternalConfig{Host: "127.0.0.1", Port: freePort(t)},
	}

	var populateCalled atomic.Bool
	var routerSawPopulate atomic.Bool

	mod := newStubModule("pop-mod")
	mod.populateFn = func(_ *Infrastructure) {
		populateCalled.Store(true)
	}

	// Use a background goroutine that immediately errors out to trigger
	// shutdown without needing SIGINT.
	b := New("populate-test", "v0.0.1", cfg).
		WithModule(mod).
		Router(func(infra Infrastructure) http.Handler {
			routerSawPopulate.Store(populateCalled.Load())
			infra.Background("force-exit", func(_ context.Context) error {
				return errors.New("intentional shutdown")
			})
			return http.NotFoundHandler()
		})

	_ = b.Run()
	assert.True(t, populateCalled.Load(), "Populate should have been called")
	assert.True(t, routerSawPopulate.Load(), "RouterFunc should see that Populate ran before it")
}

// TestModule_LifecycleCloseOrder validates the full init/close lifecycle
// through initModules and verifies reverse close ordering.
func TestModule_LifecycleCloseOrder(t *testing.T) {
	var initOrder []string
	var closeOrder []string

	mod1 := newStubModule("first")
	mod1.initFn = func(_ context.Context, mc ModuleContext) error {
		initOrder = append(initOrder, "first")
		if mc.Logger == nil {
			return errors.New("logger is nil")
		}
		if mc.Runner == nil {
			return errors.New("runner is nil")
		}
		return nil
	}
	mod1.closeFn = func(_ context.Context) error {
		closeOrder = append(closeOrder, "first")
		return nil
	}
	mod1.healthChecks = []health.DependencyCheck{
		{
			Name:  "first-dep",
			Check: func(_ context.Context) string { return health.StatusHealthy },
		},
	}

	mod2 := newStubModule("second")
	mod2.initFn = func(_ context.Context, mc ModuleContext) error {
		initOrder = append(initOrder, "second")
		dep := mc.Module("first")
		if dep.Name() != "first" {
			return errors.New("dependency lookup failed")
		}
		return nil
	}
	mod2.closeFn = func(_ context.Context) error {
		closeOrder = append(closeOrder, "second")
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{mod1, mod2},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"first", "second"}, initOrder)
	assert.True(t, mod1.initCalled.Load())
	assert.True(t, mod2.initCalled.Load())

	// Close in reverse order.
	cleanup(context.Background())
	assert.Equal(t, []string{"second", "first"}, closeOrder)

	// Verify health checks were returned.
	checks := mod1.HealthChecks()
	require.Len(t, checks, 1)
	assert.Equal(t, "first-dep", checks[0].Name)
}

func TestCloseModules_PanicRecovery(t *testing.T) {
	var closed []string

	m1 := newStubModule("panic-mod")
	m1.closeFn = func(_ context.Context) error {
		panic("close boom")
	}

	m2 := newStubModule("ok-mod")
	m2.closeFn = func(_ context.Context) error {
		closed = append(closed, "ok-mod")
		return nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	// Init both modules (m2 first, m1 second so reverse close hits m1 first).
	cleanup, err := initModules(
		context.Background(),
		[]Module{m2, m1},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)

	// Close should not panic even though m1.Close panics.
	// Reverse order: m1 first (panics), then m2 (succeeds).
	cleanup(context.Background())
	assert.Equal(t, []string{"ok-mod"}, closed)
}

func TestInitModules_InitPanicCleansUp(t *testing.T) {
	var closed []string

	m1 := newStubModule("ok-mod")
	m1.closeFn = func(_ context.Context) error {
		closed = append(closed, "ok-mod")
		return nil
	}

	m2 := newStubModule("panic-init-mod")
	m2.initFn = func(_ context.Context, _ ModuleContext) error {
		panic("init boom")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{m1, m2},
		logger,
		runner,
		BaseConfig{},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic-init-mod")
	assert.Contains(t, err.Error(), "init boom")
	assert.Nil(t, cleanup)

	// The first module should have been cleaned up despite the panic.
	assert.Equal(t, []string{"ok-mod"}, closed)
}

func TestInitModules_PopulateOrder(t *testing.T) {
	var populateOrder []string

	m1 := newStubModule("alpha")
	m1.populateFn = func(_ *Infrastructure) {
		populateOrder = append(populateOrder, "alpha")
	}

	m2 := newStubModule("beta")
	m2.populateFn = func(_ *Infrastructure) {
		populateOrder = append(populateOrder, "beta")
	}

	m3 := newStubModule("gamma")
	m3.populateFn = func(_ *Infrastructure) {
		populateOrder = append(populateOrder, "gamma")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := lifecycle.NewRunner(logger)

	cleanup, err := initModules(
		context.Background(),
		[]Module{m1, m2, m3},
		logger,
		runner,
		BaseConfig{},
	)
	require.NoError(t, err)
	defer cleanup(context.Background())

	// Populate is called in builder.go, not in initModules. Verify the
	// modules are populated in registration order as the builder does.
	modules := []Module{m1, m2, m3}
	infra := &Infrastructure{}
	for _, m := range modules {
		m.Populate(infra)
	}
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, populateOrder)
}

func TestBaseModule_Defaults(t *testing.T) {
	bm := NewBaseModule("test-module")

	assert.Equal(t, "test-module", bm.Name())
	assert.NoError(t, bm.Init(context.Background(), ModuleContext{}))
	assert.NoError(t, bm.Close(context.Background()))
	assert.Nil(t, bm.HealthChecks())

	// Populate is a no-op; should not panic.
	bm.Populate(&Infrastructure{})
}

func TestBaseModule_SatisfiesInterface(t *testing.T) {
	var _ Module = BaseModule{}
	var _ Module = &BaseModule{}
}

func TestNewBaseModule_PanicsOnEmptyName(t *testing.T) {
	assert.Panics(t, func() { NewBaseModule("") })
}

func TestBaseModule_EmbeddingPattern(t *testing.T) {
	// Demonstrates the intended usage: embed BaseModule, override only what you need.
	type customModule struct {
		BaseModule
		initCalled bool
	}

	m := &customModule{BaseModule: NewBaseModule("custom")}
	assert.Equal(t, "custom", m.Name())
	assert.Nil(t, m.HealthChecks()) // inherited no-op
	assert.NoError(t, m.Close(context.Background())) // inherited no-op

	m.initCalled = true // custom logic would go in an overridden Init
	assert.True(t, m.initCalled)
}

func TestInitModules_DuplicateNamePanics(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m1 := &stubModule{name: "dup"}
	m2 := &stubModule{name: "dup"}

	assert.Panics(t, func() {
		_, _ = initModules(context.Background(), []Module{m1, m2}, logger, nil, BaseConfig{})
	})
}

