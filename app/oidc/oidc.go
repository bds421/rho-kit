package oidc

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/app/v2"
	kitoauth "github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/security/v2/identity"
)

// ModuleName is the stable Builder module name.
const ModuleName = "oidc"

// ResourceClientKey publishes the initialized OIDC client in app.Infrastructure.
const ResourceClientKey = "github.com/bds421/rho-kit/app/oidc.client"

// Option configures an OIDC browser composition.
type Option func(*config)

type config struct {
	sessions    kitoauth.SessionStore
	states      kitoauth.StateStore
	profile     identity.MappingProfile
	clientOpts  []kitoauth.Option
	allowMemory bool
}

// WithSessionStore supplies durable browser-session persistence. Multi-replica
// services should use auth/oauth2/redis rather than the memory store.
func WithSessionStore(store kitoauth.SessionStore) Option {
	if store == nil {
		panic("app/oidc: WithSessionStore requires a non-nil store")
	}
	return func(c *config) { c.sessions = store }
}

// WithStateStore supplies durable OIDC state/nonce/PKCE persistence.
func WithStateStore(store kitoauth.StateStore) Option {
	if store == nil {
		panic("app/oidc: WithStateStore requires a non-nil store")
	}
	return func(c *config) { c.states = store }
}

// WithInMemoryStoresForTesting explicitly permits memory-backed OIDC state
// and sessions. It exists for hermetic tests only; production browser/BFF
// services must use a durable store such as auth/oauth2/redis so login can
// survive a replica change or restart.
func WithInMemoryStoresForTesting() Option {
	return func(c *config) { c.allowMemory = true }
}

// WithPrincipalProfile maps verified ID-token claims into the canonical
// identity.Principal made available to non-OAuth routes. The zero profile maps
// only the verified OIDC subject.
func WithPrincipalProfile(profile identity.MappingProfile) Option {
	return func(c *config) { c.profile = profile }
}

// WithClientOption appends a safe low-level OAuth client option, such as a
// cookie-name or session TTL override. Session/state stores are owned by this
// module and therefore must be passed through WithSessionStore/WithStateStore.
func WithClientOption(opt kitoauth.Option) Option {
	if opt == nil {
		panic("app/oidc: WithClientOption requires a non-nil option")
	}
	return func(c *config) { c.clientOpts = append(c.clientOpts, opt) }
}

// Module builds an opt-in browser/BFF OIDC composition. It intercepts only
// /oauth/login, /oauth/callback, and /oauth/logout; all other routes continue
// through the service router and gain a canonical principal when a valid
// session cookie is present.
func Module(oidcConfig kitoauth.Config, opts ...Option) app.Module {
	cfg := config{}
	for _, opt := range opts {
		if opt == nil {
			panic("app/oidc: Module option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.sessions == nil {
		panic("app/oidc: WithSessionStore is required")
	}
	if cfg.states == nil {
		panic("app/oidc: WithStateStore is required")
	}
	if !cfg.allowMemory && (isMemorySessionStore(cfg.sessions) || isMemoryStateStore(cfg.states)) {
		panic("app/oidc: memory session/state stores require WithInMemoryStoresForTesting; use auth/oauth2/redis in production")
	}
	return &module{oidcConfig: oidcConfig, cfg: cfg}
}

func isMemorySessionStore(store kitoauth.SessionStore) bool {
	_, ok := store.(*kitoauth.MemorySessionStore)
	return ok
}

func isMemoryStateStore(store kitoauth.StateStore) bool {
	_, ok := store.(*kitoauth.MemoryStateStore)
	return ok
}

type module struct {
	oidcConfig kitoauth.Config
	cfg        config
	client     *kitoauth.Client
	logger     *slog.Logger
}

func (m *module) Name() string { return ModuleName }

func (m *module) Init(ctx context.Context, mc app.ModuleContext) error {
	if m == nil {
		return errors.New("app/oidc: nil module")
	}
	httpClientModule, ok := mc.LookupModule(app.HTTPClientModuleName).(app.HTTPClientProvider)
	if !ok {
		return errors.New("app/oidc: httpclient module not registered or unexpected type")
	}
	opts := make([]kitoauth.Option, 0, len(m.cfg.clientOpts)+4)
	opts = append(opts,
		kitoauth.WithSessionStore(m.cfg.sessions),
		kitoauth.WithStateStore(m.cfg.states),
		kitoauth.WithHTTPClient(httpClientModule.Client()),
		kitoauth.WithLogger(mc.Logger),
	)
	opts = append(opts, m.cfg.clientOpts...)
	client, err := kitoauth.NewClient(ctx, m.oidcConfig, opts...)
	if err != nil {
		return err
	}
	m.client = client
	m.logger = mc.Logger
	return nil
}

func (m *module) Populate(infra *app.Infrastructure) {
	if m != nil && m.client != nil {
		infra.SetResource(ResourceClientKey, m.client)
	}
}

func (m *module) Stop(context.Context) error             { return nil }
func (m *module) HealthChecks() []health.DependencyCheck { return nil }

// PublicMiddleware owns the fixed OIDC route set and projects a valid session
// for downstream routes. It is empty before Init, so a manually-driven module
// cannot accidentally expose an uninitialized login surface.
func (m *module) PublicMiddleware() []app.PhasedMiddleware {
	if m == nil || m.client == nil {
		return nil
	}
	return []app.PhasedMiddleware{{Phase: app.PhaseAuth, Func: m.middleware}}
}

func (m *module) middleware(next http.Handler) http.Handler {
	handlers := m.client.Handlers()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/login", "/oauth/callback", "/oauth/logout":
			handlers.ServeHTTP(w, r)
			return
		}

		session, err := m.client.SessionFromRequest(r.Context(), r)
		if err != nil {
			if errors.Is(err, kitoauth.ErrSessionNotFound) {
				next.ServeHTTP(w, r)
				return
			}
			m.logger.WarnContext(r.Context(), "app/oidc: session store unavailable")
			http.Error(w, "authentication temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		principal, err := m.cfg.profile.Project(session.UserID, session.Claims)
		if err != nil {
			m.logger.WarnContext(r.Context(), "app/oidc: session principal mapping rejected")
			http.Error(w, "invalid authenticated session", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), principal)))
	})
}

// Client retrieves the initialized OIDC client from app infrastructure.
func Client(infra app.Infrastructure) *kitoauth.Client {
	v, ok := infra.Resource(ResourceClientKey)
	if !ok {
		return nil
	}
	client, _ := v.(*kitoauth.Client)
	return client
}
