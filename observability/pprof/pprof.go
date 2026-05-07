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
	"net/http"
	"net/http/pprof"
	"runtime"
	"strings"
)

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
func Handler() http.Handler {
	mux := http.NewServeMux()
	Mount(mux)
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
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
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
