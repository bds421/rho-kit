package netutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Option configures SSRF validation behavior.
type Option func(*options)

type options struct {
	allowPrivateIPs bool
	requestTimeout  time.Duration
	minTLSVersion   uint16
}

func collectOptions(opts []Option) options {
	o := options{
		// FR-016 [MED]: default whole-request budget so a hostile
		// upstream cannot stream bytes forever to callers that
		// forgot to set their own deadline. 30s is generous for any
		// legitimate small-payload outbound request; raise via
		// [WithRequestTimeout].
		requestTimeout: 30 * time.Second,
		// FR-017 [LOW]: TLS 1.2 is the realistic floor for arbitrary
		// public HTTPS endpoints. Internal mTLS still pins TLS 1.3
		// in its own profiles. Callers that want a strict TLS-1.3
		// SSRF profile can set [WithMinTLSVersion](tls.VersionTLS13).
		minTLSVersion: tls.VersionTLS12,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("ssrf: option must not be nil")
		}
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

// WithRequestTimeout overrides the whole-request deadline applied to
// SSRF-safe clients (audit FR-016). The duration must be positive; use
// [WithoutRequestTimeout] for the rare streaming call that must opt out.
// The default is 30s.
//
// The dial / handshake / response-header timeouts on the transport
// already cap connection setup, but a hostile upstream can still
// stream bytes forever during body read. The whole-request timeout
// closes that gap for callers that forgot to set their own context
// deadline.
func WithRequestTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("ssrf: WithRequestTimeout requires a positive duration")
	}
	return func(o *options) { o.requestTimeout = d }
}

// WithoutRequestTimeout disables the whole-request deadline applied to
// SSRF-safe clients. Use only for callers that impose their own context
// deadline around the full response-body read.
func WithoutRequestTimeout() Option {
	return func(o *options) { o.requestTimeout = 0 }
}

// WithMinTLSVersion overrides the minimum TLS version on the SSRF
// transport (audit FR-017). Pass [tls.VersionTLS13] for strict
// kit-internal-style outbound calls; pass [tls.VersionTLS12] (the
// default) for compatibility with public TLS-1.2-only upstreams.
func WithMinTLSVersion(v uint16) Option {
	if v < tls.VersionTLS12 {
		panic("ssrf: WithMinTLSVersion requires TLS 1.2 or newer")
	}
	return func(o *options) { o.minTLSVersion = v }
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
	if err := validateResolveHost(host); err != nil {
		return "", err
	}

	if ip := net.ParseIP(host); ip != nil {
		if cfg.allowPrivateIPs {
			slog.Warn("ssrf: private IP filtering disabled — do not use in production", redact.String("host", host))
			return ip.String(), nil
		}
		if IsPrivateIP(ip) {
			return "", fmt.Errorf("ssrf: host is private/reserved")
		}
		return ip.String(), nil
	}

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("ssrf: dns resolution failed")
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("ssrf: dns resolution returned no addresses")
	}

	if cfg.allowPrivateIPs {
		slog.Warn("ssrf: private IP filtering disabled — do not use in production", redact.String("host", host))
		return ips[0].IP.String(), nil
	}

	for _, ip := range ips {
		if IsPrivateIP(ip.IP) {
			continue
		}
		return ip.IP.String(), nil
	}
	return "", fmt.Errorf("ssrf: all resolved IPs are private/reserved")
}

func validateResolveHost(host string) error {
	if host == "" {
		return fmt.Errorf("ssrf: host must not be empty")
	}
	for _, r := range host {
		if r == 0 || unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("ssrf: host contains whitespace or control characters")
		}
	}
	if strings.ContainsAny(host, `/\`) {
		return fmt.Errorf("ssrf: host must not contain path separators")
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		return fmt.Errorf("ssrf: host must not include URL brackets")
	}
	if strings.ContainsRune(host, '%') {
		return fmt.Errorf("ssrf: host must not contain percent-encoding or zone identifiers")
	}
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return fmt.Errorf("ssrf: host must not include a port")
	}
	return nil
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
		"64:ff9b::/96",   // NAT64 well-known prefix (RFC 6052)
		"64:ff9b:1::/48", // Local-use IPv4/IPv6 translation (RFC 8215)
		"100::/64",       // Discard-only (RFC 6666)
		"100:0:0:1::/64", // Dummy IPv6 prefix (RFC 9780)
		"2001::/32",      // Teredo tunneling — embeds arbitrary IPv4 addresses
		"2001:2::/48",    // Benchmarking (RFC 5180)
		"2001:10::/28",   // Deprecated ORCHID (RFC 4843)
		"2001:db8::/32",  // Documentation (RFC 3849)
		"2002::/16",      // 6to4 tunneling — embeds arbitrary IPv4 addresses
		"3fff::/20",      // Documentation (RFC 9637)
		"5f00::/16",      // SRv6 SIDs, not globally reachable (RFC 9602)
		"::ffff:0:0/96",  // IPv4-mapped — To4() converts these to IPv4 first, so the IPv4 ranges catch them; this entry is defense-in-depth for implementations that skip To4()
	}
	nets := make([]net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("netutil: invalid CIDR in privateIPv6Ranges")
		}
		nets = append(nets, *n)
	}
	return nets
}()

// IsPrivateIP reports whether ip is in a private, reserved, or otherwise
// non-routable address range.
func IsPrivateIP(ip net.IP) bool {
	if ip == nil || ip.To16() == nil {
		return true
	}
	// Unspecified (0.0.0.0 and ::) must always be rejected — dialing them
	// targets local-host wildcard semantics regardless of address family,
	// which trivially defeats the SSRF boundary. The IPv4 0.0.0.0/8 entry
	// in privateIPv4Ranges already catches the v4 case, but checking
	// IsUnspecified() up front documents the intent and removes any
	// reliance on ordering of the v4 range loop.
	if ip.IsUnspecified() {
		return true
	}
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
	cfg := collectOptions(opts)
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialHost, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf: invalid dial address")
			}
			if !sameDialHost(dialHost, host) {
				return nil, fmt.Errorf("ssrf: transport pinned to host cannot dial different host")
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
		TLSClientConfig: &tls.Config{
			ServerName: host,
			MinVersion: cfg.minTLSVersion,
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
	}, ip, nil
}

func sameDialHost(a, b string) bool {
	return strings.EqualFold(a, b)
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
	cfg := collectOptions(opts)
	return &http.Client{
		Transport: transport,
		Timeout:   cfg.requestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, ip, nil
}

// SSRFSafeTransportFromURL is the URL-input variant of [SSRFSafeTransport].
// Most callers receive a full URL from upstream (config, request body, user
// input) rather than a bare hostname; parsing it themselves to extract the
// host string before calling [SSRFSafeTransport] is repetitive boilerplate
// that's easy to get wrong (e.g. Hostname() vs Host, port handling, IDN).
//
// Returns the parsed *url.URL so callers can build http.Request from it
// without re-parsing.
//
// Rejects schemes other than "http" and "https" — this constructor is
// specifically for HTTP transports; data:, file:, gopher: etc. are
// trivially-bad SSRF vectors that should never reach the dialer.
func SSRFSafeTransportFromURL(ctx context.Context, rawURL string, resolver DNSResolver, opts ...Option) (*http.Transport, *url.URL, string, error) {
	u, err := parseSSRFURL(rawURL)
	if err != nil {
		return nil, nil, "", err
	}
	t, ip, err := SSRFSafeTransport(ctx, u.Hostname(), resolver, opts...)
	if err != nil {
		return nil, nil, "", err
	}
	return t, u, ip, nil
}

// SSRFSafeClientFromURL is the URL-input variant of [SSRFSafeClient]. See
// [SSRFSafeTransportFromURL] for the rationale.
func SSRFSafeClientFromURL(ctx context.Context, rawURL string, resolver DNSResolver, opts ...Option) (*http.Client, *url.URL, string, error) {
	u, err := parseSSRFURL(rawURL)
	if err != nil {
		return nil, nil, "", err
	}
	c, ip, err := SSRFSafeClient(ctx, u.Hostname(), resolver, opts...)
	if err != nil {
		return nil, nil, "", err
	}
	return c, u, ip, nil
}

// parseSSRFURL parses rawURL and rejects unsafe schemes / empty hosts.
//
// Empty host is the corner case where url.Parse accepts something like
// "http://" without complaint: a downstream Resolve on "" silently succeeds
// and the connection ends up dialing the local machine. Reject up front.
func parseSSRFURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		if strings.Contains(err.Error(), "invalid port") {
			return nil, fmt.Errorf("ssrf: URL port is invalid")
		}
		return nil, fmt.Errorf("ssrf: invalid URL syntax")
	}
	if err := validateSSRFURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

func validateSSRFURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("ssrf: URL must not be nil")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ssrf: scheme is not allowed (only http/https)")
	}
	if u.User != nil {
		return fmt.Errorf("ssrf: URL userinfo is not allowed")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("ssrf: URL has empty host")
	}
	if err := validateURLPort(u); err != nil {
		return err
	}
	if err := validateResolveHost(u.Hostname()); err != nil {
		return err
	}
	return nil
}

func validateURLPort(u *url.URL) error {
	host := u.Host
	if host == "" || !strings.ContainsRune(host, ':') {
		return nil
	}

	if strings.HasPrefix(host, "[") {
		end := strings.LastIndex(host, "]")
		if end < 0 {
			return fmt.Errorf("ssrf: URL host is invalid")
		}
		rest := host[end+1:]
		if rest == "" {
			return nil
		}
		if !strings.HasPrefix(rest, ":") {
			return fmt.Errorf("ssrf: URL host has invalid bracketed IPv6 syntax")
		}
		return validatePortValue(rest[1:])
	}

	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return fmt.Errorf("ssrf: URL host has invalid port syntax")
	}
	return validatePortValue(port)
}

func validatePortValue(port string) error {
	if port == "" {
		return fmt.Errorf("ssrf: URL port must not be empty")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return fmt.Errorf("ssrf: URL port is invalid")
	}
	return nil
}

// SSRFSafeDynamicTransport returns an *http.Transport whose DialContext
// re-resolves AND re-validates the destination host on every dial.
//
// Use this in two scenarios where the IP-pinned [SSRFSafeTransport] is
// unsafe:
//
//   - Long-lived clients (the cached IP would grow stale as the upstream's
//     DNS rotates).
//   - Following redirects to potentially-different hosts (the per-dial
//     resolver re-validates the redirect target before connecting, so a
//     302 → http://169.254.169.254/ cannot escape the SSRF guard).
//
// Cost: one extra DNS lookup per dial. Acceptable for outbound requests to
// user-supplied URLs, which are rarely on a hot path.
//
// Pass [WithAllowPrivateIPs] to permit localhost and private IPs (local
// development only).
func SSRFSafeDynamicTransport(resolver DNSResolver, opts ...Option) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf: invalid dial address")
			}
			ip, err := ResolveAndValidate(ctx, host, resolver, opts...)
			if err != nil {
				return nil, fmt.Errorf("ssrf: resolve target: %w", err)
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
		},
		TLSClientConfig: &tls.Config{
			MinVersion: collectOptions(opts).minTLSVersion,
			// ServerName intentionally left empty — Go's http.Transport
			// derives it from the request URL, which is correct for
			// dynamic / redirect-following clients.
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// SSRFSafeClientFollowRedirects returns an *http.Client that follows
// redirects up to maxHops, re-validating each hop's host through the
// SSRF guard. Built on [SSRFSafeDynamicTransport] so the per-dial DNS
// lookup blocks any redirect that would land on an internal IP — the
// classic SSRF-via-redirect bypass.
//
// maxHops <= 0 panics; the recommended value is 5–10. Returns an error
// from the request when the redirect chain exceeds maxHops.
//
// Pass [WithAllowPrivateIPs] to permit localhost and private IPs (local
// development only).
func SSRFSafeClientFollowRedirects(maxHops int, resolver DNSResolver, opts ...Option) *http.Client {
	if maxHops <= 0 {
		panic("ssrf: SSRFSafeClientFollowRedirects requires maxHops > 0")
	}
	cfg := collectOptions(opts)
	return &http.Client{
		Transport: ssrfValidatingTransport{next: SSRFSafeDynamicTransport(resolver, opts...)},
		Timeout:   cfg.requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := validateSSRFURL(req.URL); err != nil {
				return err
			}
			if len(via) > maxHops {
				return fmt.Errorf("ssrf: redirect chain exceeded %d hops", maxHops)
			}
			return nil
		},
	}
}

type ssrfValidatingTransport struct {
	next http.RoundTripper
}

func (t ssrfValidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("ssrf: request must not be nil")
	}
	if err := validateSSRFURL(req.URL); err != nil {
		return nil, err
	}
	next := t.next
	if next == nil {
		return nil, fmt.Errorf("ssrf: transport is not initialized")
	}
	return next.RoundTrip(req)
}
