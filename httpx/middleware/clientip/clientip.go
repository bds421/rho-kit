package clientip

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// defaultTrustedProxyCIDRs lists ONLY loopback ranges. Internal RFC1918 /
// ULA ranges are no longer trusted by default — they let any caller reaching
// the service from inside the VPC, pod network, or Docker network spoof
// `X-Real-IP` / `X-Forwarded-For` and bypass IP-based rate limits, audit
// logging, and abuse detection.
//
// Production deployments running behind a TLS-terminating ingress MUST
// supply the ingress's CIDRs via [ParseTrustedProxiesStrict] (or the
// permissive [ParseTrustedProxies]) and pass the result to
// [ClientIPWithTrustedProxies]. The same list should drive
// [ratelimit.WithTrustedProxies] so log/ratelimit attribution agrees.
var defaultTrustedProxyCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"::1/128",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		nets = append(nets, n)
	}
	return nets
}()

// ClientIP extracts the real client IP from a request using the most
// trustworthy source available:
//
//  1. X-Real-IP — single-value header set by the edge proxy (nginx).
//  2. X-Forwarded-For — walked right-to-left, skipping trusted proxy IPs.
//  3. RemoteAddr — direct connection address (final fallback).
//
// Both X-Real-IP and X-Forwarded-For are only trusted when RemoteAddr
// itself comes from a trusted proxy (private/loopback ranges).
func ClientIP(r *http.Request) string {
	return ClientIPWithTrustedProxies(r, nil)
}

// ClientIPWithTrustedProxies behaves like ClientIP but allows a custom list of
// trusted proxy CIDRs. If trusted is nil or empty, the default proxy ranges
// are used.
func ClientIPWithTrustedProxies(r *http.Request, trusted []*net.IPNet) string {
	if len(trusted) == 0 {
		trusted = defaultTrustedProxyCIDRs
	}
	if !isTrustedAddr(r.RemoteAddr, trusted) {
		return stripPort(r.RemoteAddr)
	}

	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if net.ParseIP(realIP) != nil {
			return realIP
		}
		// X-Real-IP is not a valid IP address — fall through to X-Forwarded-For.
	}

	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if candidate == "" {
				continue
			}
			candidateIP := net.ParseIP(candidate)
			if candidateIP == nil {
				// Skip unparseable entries (hostnames, garbage) rather than
				// returning them as a "client IP" — downstream consumers
				// expect a valid IP address.
				continue
			}
			if !isIPInTrustedCIDRs(candidateIP, trusted) {
				return candidate
			}
		}
	}

	return stripPort(r.RemoteAddr)
}

// ParseTrustedProxies parses a list of CIDR strings or single IP addresses
// into net.IPNet values. Invalid entries are silently skipped. If the
// resulting list is empty, the default (loopback-only) trusted proxy ranges
// are returned.
//
// Prefer [ParseTrustedProxiesStrict] — silently skipping invalid entries
// hides operator typos, and a typo in the trusted-proxies list directly
// translates to either client-IP spoofing (entry silently skipped) or
// dropped trust (entry skipped where it shouldn't be).
func ParseTrustedProxies(cidrs []string) []*net.IPNet {
	if len(cidrs) == 0 {
		return defaultTrustedProxyCIDRs
	}
	nets, _ := parseProxies(cidrs, false)
	if len(nets) == 0 {
		return defaultTrustedProxyCIDRs
	}
	return nets
}

// ParseTrustedProxiesStrict parses a list of CIDR strings or single IP
// addresses into net.IPNet values, returning an error on the first invalid
// entry. Use this in startup paths where a typo should fail-loud rather
// than silently degrade trust.
//
// An empty or nil input returns (nil, nil) — the caller must decide whether
// to fall back to the default loopback list or trust no proxies at all.
func ParseTrustedProxiesStrict(cidrs []string) ([]*net.IPNet, error) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	return parseProxies(cidrs, true)
}

// parseProxies is the shared implementation. When strict is true, the first
// unparseable entry returns an error; when false, unparseable entries are
// dropped.
func parseProxies(cidrs []string, strict bool) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			ip := net.ParseIP(c)
			if ip == nil {
				if strict {
					return nil, fmt.Errorf("clientip: %q is not a valid CIDR or IP", c)
				}
				continue
			}
			mask := net.CIDRMask(128, 128)
			if ip.To4() != nil {
				mask = net.CIDRMask(32, 32)
			}
			n = &net.IPNet{IP: ip, Mask: mask}
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// stripPort removes the port portion from a host:port address.
func stripPort(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// isTrustedAddr returns true if remoteAddr (host:port) falls within a trusted CIDR.
func isTrustedAddr(remoteAddr string, trusted []*net.IPNet) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return isIPInTrustedCIDRs(ip, trusted)
}

// isIPInTrustedCIDRs returns true if ip is contained in any of the trusted CIDRs.
func isIPInTrustedCIDRs(ip net.IP, trusted []*net.IPNet) bool {
	for _, cidr := range trusted {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
