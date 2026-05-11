// Package httpx provides HTTP helpers and safe server defaults.
//
// It exists to standardize timeouts, JSON responses, and client TLS/tracing
// across services. Use NewHTTPClient for internal mTLS calls, or
// NewTracingHTTPClient when you want OpenTelemetry spans on outbound requests.
// Kit-created clients block redirects unless callers opt into a bounded chain.
package httpx
