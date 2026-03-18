package clientip

import (
	"net"
	"net/http"
	"strings"
)

// defaultTrustedProxyCIDRs are private/loopback ranges that typically host reverse proxies.
var defaultTrustedProxyCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"::1/128",
		"fc00::/7",
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

// ParseTrustedProxies parses a list of CIDR strings or single IP addresses into
// net.IPNet values. Invalid entries are skipped. If the resulting list is empty,
// the default trusted proxy ranges are returned.
func ParseTrustedProxies(cidrs []string) []*net.IPNet {
	if len(cidrs) == 0 {
		return defaultTrustedProxyCIDRs
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			ip := net.ParseIP(c)
			if ip == nil {
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
	if len(nets) == 0 {
		return defaultTrustedProxyCIDRs
	}
	return nets
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
