package app

import (
	"net/http"
	"os"

	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// resolvedHTTPConfig is the kit's effective HTTP configuration
// for one Builder.Run invocation. The Builder iterates user-
// registered modules at startup, captures the first
// [HTTPConfigProvider] it finds, and threads its values through
// the server-build code paths below. When no HTTPConfigProvider
// module is registered, the zero value matches the kit's
// production-shape defaults (TLS required, default stack on,
// internal ops loopback-only, no server options).
type resolvedHTTPConfig struct {
	allowPlaintext           bool
	optionalClientCerts      bool
	allowInternalNonLoopback bool
	reloadingTLSActive       bool
	reloadingTLSOpts         []netutil.FilesCertificateSourceOption
	tlsReloadSignals         []os.Signal
	disableDefaultStack      bool
	stackOpts                []stack.Option
	serverOpts               []httpx.ServerOption
	customReadiness          http.Handler
}

// resolveHTTPConfig walks the module list looking for the single
// [HTTPConfigProvider] and returns its resolved configuration.
// Falls back to the zero value (kit defaults: TLS required, default
// stack on, internal ops loopback-only) when no HTTPConfigProvider
// module is registered.
//
// Exactly one provider is allowed. Registering two modules that both
// implement HTTPConfigProvider (e.g. app/http.Module alongside a
// custom module that also implements the interface) panics at
// startup: the Builder threads HTTP config through several code paths
// that scan the module list in different orders (Run reorders modules,
// Validate and serverTLSOptions scan registration order), so a
// "first wins" rule would silently resolve to different providers in
// different paths. This mirrors the fail-fast duplicate-module-name
// panic in [Builder.With].
//
// Services that need to override the defaults register
// app/http.Module(opts...) on the Builder.
func resolveHTTPConfig(modules []Module) resolvedHTTPConfig {
	var provider HTTPConfigProvider
	for _, m := range modules {
		p, ok := m.(HTTPConfigProvider)
		if !ok {
			continue
		}
		if provider != nil {
			panic("app: more than one registered module implements HTTPConfigProvider (register at most one app/http.Module)")
		}
		provider = p
	}
	if provider == nil {
		return resolvedHTTPConfig{}
	}
	reloadOpts, reloadActive := provider.ReloadingTLSOptions()
	return resolvedHTTPConfig{
		allowPlaintext:           provider.AllowPlaintext(),
		optionalClientCerts:      provider.OptionalClientCerts(),
		allowInternalNonLoopback: provider.AllowInternalNonLoopback(),
		reloadingTLSActive:       reloadActive,
		reloadingTLSOpts:         reloadOpts,
		tlsReloadSignals:         provider.TLSReloadSignals(),
		disableDefaultStack:      provider.DisableDefaultStack(),
		stackOpts:                provider.StackOptions(),
		serverOpts:               provider.ServerOptions(),
		customReadiness:          provider.CustomReadiness(),
	}
}
