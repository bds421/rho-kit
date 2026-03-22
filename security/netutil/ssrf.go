package netutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Option configures SSRF validation behavior.
type Option func(*options)

type options struct {
	allowPrivateIPs bool
}

func collectOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithAllowPrivateIPs disables private/reserved IP filtering.
// This is intended ONLY for local development where services communicate
// over localhost or Docker-internal networks.
//
// WARNING: Never use this option in production — it completely disables
// SSRF protection. A [slog.Warn] message is emitted every time this
// option is active so that accidental production usage is visible in logs.
func WithAllowPrivateIPs() Option {
	return func(o *options) {
		o.allowPrivateIPs = true
	}
}

// DNSResolver abstracts DNS lookups for testability.
// Use nil with ResolveAndValidate to use net.DefaultResolver.
type DNSResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// ResolveAndValidate resolves the hostname and returns the first non-private IP as a string.
// Pass nil for resolver to use net.DefaultResolver.
//
// By default all private/reserved IPs are rejected. Pass [WithAllowPrivateIPs]
// to permit them (local development only).
//
// WARNING: Callers MUST use the returned IP string (not the original hostname)
// when dialing, to prevent DNS rebinding attacks. Prefer SSRFSafeTransport
// which enforces this by construction.
func ResolveAndValidate(ctx context.Context, host string, resolver DNSResolver, opts ...Option) (string, error) {
	cfg := collectOptions(opts)

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("dns resolution failed for %s: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("dns resolution returned no addresses for %s", host)
	}

	if cfg.allowPrivateIPs {
		slog.Warn("ssrf: private IP filtering disabled — do not use in production", "host", host)
		return ips[0].IP.String(), nil
	}

	for _, ip := range ips {
		if IsPrivateIP(ip.IP) {
			continue
		}
		return ip.IP.String(), nil
	}
	return "", fmt.Errorf("all resolved IPs for %s are private/reserved", host)
}

// privateIPv4Ranges lists all IPv4 CIDR ranges considered private/reserved for SSRF protection.
var privateIPv4Ranges = []net.IPNet{
	{IP: net.IPv4(10, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)},
	{IP: net.IPv4(192, 168, 0, 0), Mask: net.CIDRMask(16, 32)},
	{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(169, 254, 0, 0), Mask: net.CIDRMask(16, 32)},
	{IP: net.IPv4(0, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)},   // CGNAT (RFC 6598)
	{IP: net.IPv4(224, 0, 0, 0), Mask: net.CIDRMask(4, 32)},     // Multicast (RFC 5771)
	{IP: net.IPv4(240, 0, 0, 0), Mask: net.CIDRMask(4, 32)},     // Reserved (RFC 1112)
	{IP: net.IPv4(192, 0, 0, 0), Mask: net.CIDRMask(24, 32)},    // IETF Protocol Assignments
	{IP: net.IPv4(192, 0, 2, 0), Mask: net.CIDRMask(24, 32)},    // TEST-NET-1 (RFC 5737)
	{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)},   // Benchmarking (RFC 2544)
	{IP: net.IPv4(198, 51, 100, 0), Mask: net.CIDRMask(24, 32)}, // TEST-NET-2 (RFC 5737)
	{IP: net.IPv4(203, 0, 113, 0), Mask: net.CIDRMask(24, 32)},  // TEST-NET-3 (RFC 5737)
	{IP: net.IPv4(192, 88, 99, 0), Mask: net.CIDRMask(24, 32)},  // 6to4 relay (deprecated)
}

// privateIPv6Ranges lists IPv6 CIDR ranges considered unsafe for SSRF protection,
// beyond what net.IP.IsPrivate/IsLoopback/IsLinkLocal already cover.
var privateIPv6Ranges = func() []net.IPNet {
	cidrs := []string{
		"2001::/32",     // Teredo tunneling — embeds arbitrary IPv4 addresses
		"2002::/16",     // 6to4 tunneling — embeds arbitrary IPv4 addresses
		"64:ff9b::/96",  // NAT64 well-known prefix (RFC 6052)
		"100::/64",      // Discard-only (RFC 6666)
		"::ffff:0:0/96", // IPv4-mapped — To4() converts these to IPv4 first, so the IPv4 ranges catch them; this entry is defense-in-depth for implementations that skip To4()
	}
	nets := make([]net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("netutil: invalid CIDR in privateIPv6Ranges: " + c)
		}
		nets = append(nets, *n)
	}
	return nets
}()

// IsPrivateIP reports whether ip is in a private, reserved, or otherwise
// non-routable address range.
func IsPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		for _, r := range privateIPv4Ranges {
			if r.Contains(ip4) {
				return true
			}
		}
		return false
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	for _, r := range privateIPv6Ranges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// SSRFSafeTransport resolves the hostname, validates the IP is not private,
// and returns an http.Transport that dials the pre-validated IP directly.
// TLS ServerName (SNI) is preserved so HTTPS works correctly.
//
// This prevents DNS rebinding attacks by design — the Go HTTP stack cannot
// re-resolve the hostname at dial time because we override DialContext.
//
// The returned transport captures the resolved IP at creation time. It is
// intended for short-lived use (one request or a small batch). Do not store
// it long-term — if the server's IP changes, the transport will dial a stale
// address. Create a new transport per request for maximum correctness.
//
// Pass [WithAllowPrivateIPs] to permit localhost and private IPs (local
// development only).
//
// WARNING: Do not use this transport with an http.Client that follows redirects.
// Redirects could target internal IPs, bypassing SSRF protection. Use
// [SSRFSafeClient] instead, which disables redirects automatically.
func SSRFSafeTransport(ctx context.Context, host string, resolver DNSResolver, opts ...Option) (*http.Transport, string, error) {
	ip, err := ResolveAndValidate(ctx, host, resolver, opts...)
	if err != nil {
		return nil, "", err
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf: invalid address %q: %w", addr, err)
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
		TLSClientConfig: &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
	}, ip, nil
}

// SSRFSafeClient returns an *http.Client that resolves and validates the
// target host, pins the resolved IP, and refuses to follow redirects.
// This is the recommended way to make outbound requests to user-supplied URLs.
// Pass [WithAllowPrivateIPs] to permit localhost and private IPs (local
// development only).
func SSRFSafeClient(ctx context.Context, host string, resolver DNSResolver, opts ...Option) (*http.Client, string, error) {
	transport, ip, err := SSRFSafeTransport(ctx, host, resolver, opts...)
	if err != nil {
		return nil, "", err
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, ip, nil
}
