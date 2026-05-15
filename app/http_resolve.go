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

// resolveHTTPConfig walks the module list looking for the first
// [HTTPConfigProvider] and returns its resolved configuration.
// Falls back to the zero value (kit defaults: TLS required, default
// stack on, internal ops loopback-only) when no HTTPConfigProvider
// module is registered.
//
// Services that need to override these defaults register
// app/http.Module(opts...) on the Builder.
func resolveHTTPConfig(modules []Module) resolvedHTTPConfig {
	for _, m := range modules {
		p, ok := m.(HTTPConfigProvider)
		if !ok {
			continue
		}
		reloadOpts, reloadActive := p.ReloadingTLSOptions()
		return resolvedHTTPConfig{
			allowPlaintext:           p.AllowPlaintext(),
			optionalClientCerts:      p.OptionalClientCerts(),
			allowInternalNonLoopback: p.AllowInternalNonLoopback(),
			reloadingTLSActive:       reloadActive,
			reloadingTLSOpts:         reloadOpts,
			tlsReloadSignals:         p.TLSReloadSignals(),
			disableDefaultStack:      p.DisableDefaultStack(),
			stackOpts:                p.StackOptions(),
			serverOpts:               p.ServerOptions(),
			customReadiness:          p.CustomReadiness(),
		}
	}
	return resolvedHTTPConfig{}
}
