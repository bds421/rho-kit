package clientip

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// maxForwardedForBytes / maxForwardedForParts cap the X-Forwarded-For
// parsing surface. Any joined value past these limits causes the
// handler to fall back to RemoteAddr — the assumption is that anything
// past a 32-hop chain of full IPv6 addresses is a misconfigured proxy
// or malicious input, not legitimate traffic.
const (
	maxForwardedForBytes = 8 * 1024
	maxForwardedForParts = 32
)

// defaultTrustedProxyCIDRs lists ONLY loopback ranges. Internal RFC1918 /
// ULA ranges are no longer trusted by default — they let any caller reaching
// the service from inside the VPC, pod network, or Docker network spoof
// `X-Real-IP` / `X-Forwarded-For` and bypass IP-based rate limits, audit
// logging, and abuse detection.
//
// Production deployments running behind a TLS-terminating ingress MUST
// supply the ingress's CIDRs via [ParseTrustedProxiesStrict] and pass the
// result to [ClientIPWithTrustedProxies]. The same list should drive
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
// X-Real-IP and X-Forwarded-For are only trusted when RemoteAddr itself
// comes from a configured trusted proxy CIDR. The default proxy set is
// loopback-only — RFC1918/ULA ranges are NOT trusted unless the caller
// supplies them via [ClientIPWithTrustedProxies]. This is a fail-closed
// default: an unconfigured ingress chain returns the direct peer rather
// than a forwarded header an upstream tenant could have set.
func ClientIP(r *http.Request) string {
	return ClientIPWithTrustedProxies(r, nil)
}

// ClientIPWithTrustedProxies behaves like ClientIP but allows a custom list of
// trusted proxy CIDRs. If trusted is nil or empty, the default proxy ranges
// are used.
func ClientIPWithTrustedProxies(r *http.Request, trusted []*net.IPNet) string {
	if r == nil {
		return ""
	}
	if len(trusted) == 0 {
		trusted = defaultTrustedProxyCIDRs
	}
	if !isTrustedAddr(r.RemoteAddr, trusted) {
		return remoteIPString(r.RemoteAddr)
	}

	if realIP, ok := singletonHeaderValue(r.Header, "X-Real-IP"); ok && realIP != "" {
		if ip := net.ParseIP(realIP); ip != nil {
			return ip.String()
		}
	}

	if forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ","); forwarded != "" {
		// Cap the joined XFF length and parts count so a misconfigured
		// upstream proxy that forwards client XFFs verbatim cannot turn
		// this into an unbounded-parse vector. The cap is generous: even
		// a chain of 32 hops with full IPv6 addresses fits comfortably
		// in 8 KiB.
		if len(forwarded) > maxForwardedForBytes {
			return remoteIPString(r.RemoteAddr)
		}
		parts := strings.Split(forwarded, ",")
		if len(parts) > maxForwardedForParts {
			return remoteIPString(r.RemoteAddr)
		}
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if candidate == "" {
				continue
			}
			candidateIP := parseForwardedForIP(candidate)
			if candidateIP == nil {
				// An entry that is neither a bare IP nor a valid host:port
				// (hostnames, truncated values, garbage) means the chain can
				// no longer be trusted: walking further left would return an
				// earlier, fully client-controlled hop. Fail closed to
				// RemoteAddr rather than continuing into untrusted territory.
				return remoteIPString(r.RemoteAddr)
			}
			if !isIPInTrustedCIDRs(candidateIP, trusted) {
				return candidateIP.String()
			}
		}
	}

	return remoteIPString(r.RemoteAddr)
}

// parseForwardedForIP parses a single X-Forwarded-For entry into an IP. It
// accepts both a bare IP and the "ip:port" / "[ipv6]:port" forms that some
// proxies (Azure Application Gateway, IIS/ARR) append. It returns nil when
// the entry is not a valid IP in any of those forms.
func parseForwardedForIP(entry string) net.IP {
	if ip := net.ParseIP(entry); ip != nil {
		return ip
	}
	if host, _, err := net.SplitHostPort(entry); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip
		}
	}
	return nil
}

func singletonHeaderValue(h http.Header, name string) (string, bool) {
	values := h.Values(name)
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

// ParseTrustedProxies parses a list of CIDR strings or single IP addresses
// into net.IPNet values. Empty input returns the default loopback-only trusted
// proxy ranges. Invalid entries panic; use [ParseTrustedProxiesStrict] when
// callers need an error-returning API.
func ParseTrustedProxies(cidrs []string) []*net.IPNet {
	if len(cidrs) == 0 {
		return cloneIPNets(defaultTrustedProxyCIDRs)
	}
	nets, err := ParseTrustedProxiesStrict(cidrs)
	if err != nil {
		panic("clientip: ParseTrustedProxies trusted proxy configuration is invalid")
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
					return nil, fmt.Errorf("clientip: trusted proxy entry is not a valid CIDR or IP")
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

func remoteIPString(addr string) string {
	host := stripPort(addr)
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// stripPort removes the port portion from a host:port address.
func stripPort(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func cloneIPNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		if n == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &net.IPNet{
			IP:   append(net.IP(nil), n.IP...),
			Mask: append(net.IPMask(nil), n.Mask...),
		})
	}
	return out
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
		if cidr == nil {
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
