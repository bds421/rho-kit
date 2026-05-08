// Package pprof exposes the standard net/http/pprof handlers as a
// kit-curated http.Handler so the internal :9090 port can mount them
// alongside /metrics and /ready.
//
// Direct use of net/http/pprof has the unfortunate side effect of
// registering routes on http.DefaultServeMux at import time. Most
// services then import pprof but never realise the public router has
// /debug/pprof/* exposed — which leaks heap profiles, allocation
// stats, and goroutine dumps to anyone who finds the route. This
// package gives an explicit, opt-in handler so the install is visible
// in every service's wiring code.
package pprof

import (
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strings"
)

// MountOption configures [MountWith].
type MountOption func(*mountConfig)

type mountConfig struct {
	requireLoopback bool
	authFn          func(*http.Request) bool
}

// WithRequireLoopback restricts pprof routes to requests originating
// from a loopback IP (127.0.0.0/8 or ::1). Non-loopback requests get a
// 404. Use this when pprof is mounted on a port that might be reachable
// from a non-loopback CIDR by accident.
func WithRequireLoopback() MountOption {
	return func(c *mountConfig) { c.requireLoopback = true }
}

// WithAuth gates every pprof request through fn. fn returns true to
// admit the request. A typical wiring is to consult auth-middleware
// context populated upstream, so pprof inherits whatever auth the rest
// of the service uses.
func WithAuth(fn func(*http.Request) bool) MountOption {
	return func(c *mountConfig) { c.authFn = fn }
}

// Handler returns an http.Handler that serves /debug/pprof/* routes.
//
// Mount it on an internal-only port (the kit's :9090 by default).
// Mounting on the public port without auth exposes:
//
//   - /debug/pprof/heap — heap allocations
//   - /debug/pprof/goroutine — every running goroutine and its stack
//   - /debug/pprof/profile — 30s CPU profile (DoS amplifier)
//   - /debug/pprof/trace — execution trace
//
// Production deployments should also gate this behind an internal
// network policy or a fleet-wide auth proxy.
func Handler(opts ...MountOption) http.Handler {
	mux := http.NewServeMux()
	MountWith(mux, opts...)
	return mux
}

// Mount installs the pprof routes onto mux at /debug/pprof/. Used when
// the caller already owns a mux and wants to attach pprof to it.
//
// Routes installed:
//
//	/debug/pprof/                — index
//	/debug/pprof/cmdline         — process command line
//	/debug/pprof/profile         — CPU profile (default 30s)
//	/debug/pprof/symbol          — symbol resolution
//	/debug/pprof/trace           — execution trace
//	/debug/pprof/{name}          — heap, goroutine, allocs, block,
//	                                mutex, threadcreate (named profiles)
func Mount(mux *http.ServeMux) {
	MountWith(mux)
}

// MountWith installs pprof on mux with optional gating. See
// [WithRequireLoopback] and [WithAuth].
func MountWith(mux *http.ServeMux, opts ...MountOption) {
	cfg := &mountConfig{}
	for _, o := range opts {
		o(cfg)
	}
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if cfg.requireLoopback && !isLoopback(r.RemoteAddr) {
				http.NotFound(w, r)
				return
			}
			if cfg.authFn != nil && !cfg.authFn(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/debug/pprof/", wrap(pprof.Index))
	mux.HandleFunc("/debug/pprof/cmdline", wrap(pprof.Cmdline))
	mux.HandleFunc("/debug/pprof/profile", wrap(pprof.Profile))
	mux.HandleFunc("/debug/pprof/symbol", wrap(pprof.Symbol))
	mux.HandleFunc("/debug/pprof/trace", wrap(pprof.Trace))
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// EnableMutexBlockProfiling turns on the runtime mutex and block
// profilers at the given fractions. Call once during boot before
// mounting pprof — the named profiles are empty until enabled.
//
//	mutexFraction: 1 in N contention events recorded; 0 disables.
//	  Production: leave at 0 unless investigating a regression.
//	blockRate: report any block exceeding the given duration in
//	  nanoseconds; 0 disables. 10000000 (10ms) is a reasonable
//	  starting point for incident investigations.
func EnableMutexBlockProfiling(mutexFraction int, blockRateNs int) {
	runtime.SetMutexProfileFraction(mutexFraction)
	runtime.SetBlockProfileRate(blockRateNs)
}

// IsPprofPath reports whether p falls under the /debug/pprof/ tree.
// Useful in middleware that wants to skip auth for these paths on
// internal-only mounts.
//
// The match is exact ("/debug/pprof") or prefixed by "/debug/pprof/" so
// look-alike paths such as "/debug/pprofevil" cannot piggy-back on a
// pprof-bypass rule.
func IsPprofPath(p string) bool {
	return p == "/debug/pprof" || strings.HasPrefix(p, "/debug/pprof/")
}
