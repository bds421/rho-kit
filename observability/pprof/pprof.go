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
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// MountOption configures [MountWith].
type MountOption func(*mountConfig)

type mountConfig struct {
	requireLoopback bool
	authFn          func(*http.Request) bool
	allowPublic     bool
}

// WithUnsafePublicMount opts a caller out of the FR-086 default
// loopback/auth requirement. Use only for environments where the
// service truly is reachable only through a trusted internal
// network. Do not use on public-facing servers — pprof endpoints
// expose heap, goroutine, and CPU profile data and can be used as
// DoS amplifiers.
func WithUnsafePublicMount() MountOption {
	return func(c *mountConfig) { c.allowPublic = true }
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
	if fn == nil {
		panic("pprof: WithAuth requires a non-nil function")
	}
	return func(c *mountConfig) { c.authFn = fn }
}

// Handler returns an http.Handler that serves /debug/pprof/* routes.
//
// With no options, Handler uses the same safe default as [Mount]:
// requests must originate from loopback. Mount it on an internal-only
// port (the kit's :9090 by default).
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
	if len(opts) == 0 {
		opts = []MountOption{WithRequireLoopback()}
	}
	MountWith(mux, opts...)
	return mux
}

// Mount installs the pprof routes onto mux at /debug/pprof/.
//
// FR-086 [MED]: Mount is now an alias for
// MountWith(mux, WithRequireLoopback()) — pre-fix it admitted
// arbitrary remote callers. Switch to MountWith with explicit
// gating for non-loopback deployments.
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
	MountWith(mux, WithRequireLoopback())
}

// MountWith installs pprof on mux with optional gating. See
// [WithRequireLoopback] and [WithAuth].
//
// FR-086 [MED]: by default the mount requires loopback OR auth.
// Pre-fix MountWith(mux) on a public mux silently exposed
// goroutine/heap/CPU profile endpoints. The constructor now panics
// when neither WithRequireLoopback nor WithAuth is supplied; opt
// out with [WithUnsafePublicMount] for the genuine "everyone is on
// the internal network" scenario.
func MountWith(mux *http.ServeMux, opts ...MountOption) {
	cfg := &mountConfig{}
	for _, o := range opts {
		if o == nil {
			panic("pprof: Mount option must not be nil")
		}
		o(cfg)
	}
	if !cfg.requireLoopback && cfg.authFn == nil && !cfg.allowPublic {
		panic("pprof: MountWith requires WithRequireLoopback or WithAuth — pass WithUnsafePublicMount to opt out (FR-086)")
	}
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if cfg.requireLoopback && !isLoopback(r.RemoteAddr) {
				http.NotFound(w, r)
				return
			}
			if cfg.authFn != nil && !allowPprofRequest(cfg.authFn, r) {
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

func allowPprofRequest(fn func(*http.Request) bool, r *http.Request) (allowed bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("pprof: auth callback panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			allowed = false
		}
	}()
	return fn(r)
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
