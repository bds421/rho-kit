// Package signedrequest is the lazy app-module wrapper for
// [github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest].
//
// Services that authenticate inter-service traffic via HMAC-
// signed requests (X-Signature + timestamp + nonce) pass
// [signedrequest.Module] to [app.Builder.With]. Services that
// don't, do not import this package.
//
// The module contributes its middleware at
// [app.PhaseSignedRequest] so unsigned or malformed requests are
// rejected before any deeper middleware (auth, tenant, budget)
// runs.
package signedrequest

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ModuleName is the registered Module.Name() value.
const ModuleName = "signedrequest"

// Module returns an [app.Module] that contributes the signed-
// request middleware to the public mux at
// [app.PhaseSignedRequest].
//
// resolver looks up the HMAC secret for a given key ID; store
// caches recently-seen nonces to defeat replay. Both are
// required — passing nil for either panics at construction.
//
// opts flow through to [signedrequest.Middleware] (clock skew,
// required headers, body cap).
func Module(resolver signedrequest.KeyResolver, store signedrequest.NonceStore, opts ...signedrequest.Option) app.Module {
	if resolver == nil {
		panic("app/signedrequest: Module requires a non-nil KeyResolver")
	}
	if store == nil {
		panic("app/signedrequest: Module requires a non-nil NonceStore (no-store means trivially-replayable signatures)")
	}
	for _, opt := range opts {
		if opt == nil {
			panic("app/signedrequest: Module option must not be nil")
		}
	}
	return &signedRequestModule{
		resolver: resolver,
		store:    store,
		opts:     append([]signedrequest.Option(nil), opts...),
	}
}

type signedRequestModule struct {
	resolver signedrequest.KeyResolver
	store    signedrequest.NonceStore
	opts     []signedrequest.Option

	middleware func(http.Handler) http.Handler
}

func (m *signedRequestModule) Name() string { return ModuleName }

func (m *signedRequestModule) Init(_ context.Context, _ app.ModuleContext) error {
	m.middleware = signedrequest.Middleware(m.resolver, m.store, m.opts...)
	return nil
}

func (m *signedRequestModule) Populate(_ *app.Infrastructure) {}

func (m *signedRequestModule) Stop(_ context.Context) error { return nil }

func (m *signedRequestModule) HealthChecks() []health.DependencyCheck { return nil }

func (m *signedRequestModule) PublicMiddleware() []app.PhasedMiddleware {
	if m.middleware == nil {
		return nil
	}
	return []app.PhasedMiddleware{{
		Phase: app.PhaseSignedRequest,
		Func:  m.middleware,
	}}
}
