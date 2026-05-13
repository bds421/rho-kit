// Package transportdefaults centralizes the kit-wide outbound HTTP transport
// defaults — connection-pool tuning, TLS-floor enforcement, and the
// process-default-transport clone strategy — so the httpx Client and the
// middleware that compose their own transports apply the same hardening
// without duplicating code.
//
// The package lives under httpx/internal/ because callers must not assemble
// their own RoundTripper out of these primitives: the public surface is
// [httpx.NewClient] and the per-middleware clients that wrap it. Exporting
// these helpers would invite divergent transports that bypass the kit's
// TLS-floor and InsecureSkipVerify guards.
package transportdefaults
