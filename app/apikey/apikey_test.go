package apikey_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appapikey "github.com/bds421/rho-kit/app/apikey/v2"
	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	apikeycore "github.com/bds421/rho-kit/security/v2/apikey"
)

func TestModule_Name(t *testing.T) {
	m := appapikey.Module(apikeycore.NewMemoryRepository())
	assert.Equal(t, appapikey.ModuleName, m.Name())
}

func TestModule_PanicsOnNilRepository(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil repository")
		}
	}()
	_ = appapikey.Module(nil)
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil option")
		}
	}()
	_ = appapikey.Module(apikeycore.NewMemoryRepository(), nil)
}

func TestModule_ContributesAuthPhaseMiddlewareAfterInit(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	m := appapikey.Module(repo)

	// Before Init, no middleware is exposed.
	mwInstaller, ok := m.(interface {
		PublicMiddleware() []app.PhasedMiddleware
	})
	require.True(t, ok, "module must expose PublicMiddleware")
	assert.Empty(t, mwInstaller.PublicMiddleware())

	require.NoError(t, m.Init(context.Background(), app.ModuleContext{ServiceName: "svc"}))

	mws := mwInstaller.PublicMiddleware()
	require.Len(t, mws, 1)
	assert.Equal(t, app.PhaseAuth, mws[0].Phase)
}

func TestModule_OptionsAreApplied(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	// Issue a key under a custom prefix and a fixed clock; the module must
	// honour both when it builds the middleware.
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	key, token, err := apikeycore.Generate(apikeycore.GenerateOptions{
		Owner: "o", Prefix: "acme", Now: now, ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, repo.Insert(context.Background(), key))

	m := appapikey.Module(repo,
		appapikey.WithPrefix("acme"),
		appapikey.WithClock(func() time.Time { return now }),
		appapikey.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	).(interface {
		Init(context.Context, app.ModuleContext) error
		PublicMiddleware() []app.PhasedMiddleware
	})
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))
	mw := m.PublicMiddleware()[0].Func

	var ok bool
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { ok = true }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", token.RevealString())
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.True(t, ok, "custom prefix + clock must authenticate the key")
}

func TestModule_LifecycleNoOps(t *testing.T) {
	m := appapikey.Module(apikeycore.NewMemoryRepository())
	full := m.(interface {
		Populate(*app.Infrastructure)
		Stop(context.Context) error
		HealthChecks() []health.DependencyCheck
		PublicMiddleware() []app.PhasedMiddleware
	})
	full.Populate(nil)
	assert.NoError(t, full.Stop(context.Background()))
	assert.Nil(t, full.HealthChecks())
}

func TestModule_MiddlewareAuthenticatesRequests(t *testing.T) {
	repo := apikeycore.NewMemoryRepository()
	key, token, err := apikeycore.Generate(apikeycore.GenerateOptions{Owner: "tenant-1"})
	require.NoError(t, err)
	require.NoError(t, repo.Insert(context.Background(), key))

	m := appapikey.Module(repo).(interface {
		Init(context.Context, app.ModuleContext) error
		PublicMiddleware() []app.PhasedMiddleware
	})
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{}))
	mw := m.PublicMiddleware()[0].Func

	var authenticated bool
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		authenticated = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token.RevealString())
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.True(t, authenticated)

	// A request without a key is rejected before reaching the handler.
	authenticated = false
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.False(t, authenticated)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestWithClock_NilPanics(t *testing.T) {
	require.PanicsWithValue(t, "app/apikey: WithClock requires a non-nil clock", func() {
		_ = appapikey.WithClock(nil)
	})
}
