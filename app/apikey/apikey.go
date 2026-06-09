package apikey

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	apikeymw "github.com/bds421/rho-kit/httpx/v2/middleware/apikey"
	"github.com/bds421/rho-kit/observability/v2/health"
	apikeycore "github.com/bds421/rho-kit/security/v2/apikey"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "api-key"

// Option configures the module.
type Option func(*module)

// WithPrefix overrides the expected token prefix (defaults to the core
// package's DefaultPrefix).
func WithPrefix(prefix string) Option {
	return func(m *module) { m.prefix = prefix }
}

// WithClock overrides the verification clock (defaults to time.Now).
func WithClock(now func() time.Time) Option {
	return func(m *module) {
		if now != nil {
			m.now = now
		}
	}
}

// WithLogger sets the logger used to record authentication failures at debug
// level. When unset, the module uses the app logger from [app.ModuleContext].
func WithLogger(logger *slog.Logger) Option {
	return func(m *module) { m.logger = logger }
}

// Module returns an [app.Module] that contributes the API-key authentication
// middleware to the public mux at [app.PhaseAuth].
//
// repo looks keys up by id and is required — passing nil panics at
// construction. opts flow through to the middleware (prefix, clock, logger).
func Module(repo apikeycore.Repository, opts ...Option) app.Module {
	if repo == nil {
		panic("app/apikey: Module requires a non-nil Repository")
	}
	for _, opt := range opts {
		if opt == nil {
			panic("app/apikey: Module option must not be nil")
		}
	}
	m := &module{repo: repo}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

type module struct {
	repo   apikeycore.Repository
	prefix string
	now    func() time.Time
	logger *slog.Logger

	middleware func(http.Handler) http.Handler
}

func (m *module) Name() string { return ModuleName }

func (m *module) Init(_ context.Context, mc app.ModuleContext) error {
	logger := m.logger
	if logger == nil {
		logger = mc.Logger
	}
	m.middleware = apikeymw.Middleware(apikeymw.Config{
		Repository: m.repo,
		Prefix:     m.prefix,
		Now:        m.now,
		Logger:     logger,
	})
	return nil
}

func (m *module) Populate(_ *app.Infrastructure) {}

func (m *module) Stop(_ context.Context) error { return nil }

func (m *module) HealthChecks() []health.DependencyCheck { return nil }

func (m *module) PublicMiddleware() []app.PhasedMiddleware {
	if m.middleware == nil {
		return nil
	}
	return []app.PhasedMiddleware{{
		Phase: app.PhaseAuth,
		Func:  m.middleware,
	}}
}
