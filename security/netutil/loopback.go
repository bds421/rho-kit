package netutil

import (
	"context"
	"net"
	"strings"
)

// IsLoopbackHost reports whether host is a loopback bind/target.
//
// Behavior:
//   - Empty host is treated as loopback (kit listeners default empty to 127.0.0.1).
//   - Bracket-only IPv6 forms ("[]", "[", "]") are non-loopback: net.Listen treats
//     them as the IPv6 wildcard.
//   - IP literals use net.ParseIP / IP.IsLoopback (no DNS).
//   - The name "localhost" (any case) is always loopback without DNS.
//   - Other hostnames are resolved via LookupIPAddr; every returned address must
//     be loopback, otherwise the result is false (fail-closed on mixed records
//     or resolution failure).
//
// Prefer this helper over package-local copies so app validators and transport
// safety checks agree on what "loopback" means.
func IsLoopbackHost(host string) bool {
	if host == "" {
		return true
	}
	stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if stripped == "" {
		// Bracket-only forms strip to empty but bind as IPv6 wildcard.
		return false
	}
	if strings.EqualFold(stripped, "localhost") {
		return true
	}
	if ip := net.ParseIP(stripped); ip != nil {
		return ip.IsLoopback()
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), stripped)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, a := range ips {
		if a.IP == nil || !a.IP.IsLoopback() {
			return false
		}
	}
	return true
}

// IsLoopbackHostLiteral reports whether host is loopback without DNS.
// Accepts empty host, "localhost" (case-insensitive), and loopback IP literals.
// Unknown hostnames return false. Use when construction-time checks must not
// depend on DNS (or when host:port has already been split to a host).
func IsLoopbackHostLiteral(host string) bool {
	if host == "" {
		return true
	}
	stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if stripped == "" {
		return false
	}
	if strings.EqualFold(stripped, "localhost") {
		return true
	}
	ip := net.ParseIP(stripped)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// IsLoopbackAddr reports whether addr is a loopback host or host:port.
// Empty addresses are treated as loopback so SDK defaults remain dev-safe.
// Host resolution uses [IsLoopbackHostLiteral] (no DNS).
func IsLoopbackAddr(addr string) bool {
	if addr == "" {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return IsLoopbackHostLiteral(host)
}
