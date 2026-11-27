package netutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// ErrEgressRefused marks a call the egress guard refused: a scheme, an
// address, a redirect or an allowlist answer. A refused dial reaches the
// caller wrapped in url.Error and net.OpError; errors.Is finds it.
var ErrEgressRefused = errors.New("netutil: egress refused")

// ErrEgressResponseTooLarge is returned by a response body read past the
// bound of [WithEgressMaxResponse].
var ErrEgressResponseTooLarge = errors.New("netutil: egress response too large")

// HostAllowed answers whether a host (as the URL names it, without
// brackets) and port are listed: such a host may be called over http
// and may reach the internal ranges ([WithEgressInternalRanges]).
type HostAllowed func(ctx context.Context, host string, port int) (bool, error)

// DefaultInternalRanges are what a listed host may reach besides public
// addresses: loopback, RFC 1918, unique-local and carrier-grade NAT
// (RFC 6598).
var DefaultInternalRanges = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("100.64.0.0/10"),
}

// neverReachable are refused for every host, listed or not: link-local
// (169.254.169.254, ECS 169.254.170.2) and the cloud metadata services
// inside the internal ranges (AWS IMDS and EKS Pod Identity on IPv6,
// Alibaba). They hand out the machine's credentials.
var neverReachable = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
}

const (
	defaultEgressTimeout      = 10 * time.Second
	defaultEgressDialTimeout  = 5 * time.Second
	defaultEgressMaxRedirects = 3
)

type egressConfig struct {
	hosts        HostAllowed
	internal     []netip.Prefix
	networks     []netip.Prefix
	maxRedirects int
	stripHeaders []string
	timeout      time.Duration
	dialTimeout  time.Duration
	roots        *x509.CertPool
	maxResponse  int64
	userAgent    string
}

// EgressOption configures [NewEgressClient] and [EgressDialer].
type EgressOption func(*egressConfig)

// WithEgressHosts lists the hosts and ports that may use http and reach
// the internal ranges. Without it no host is listed: public addresses
// over https only. hosts is asked for every request, redirect and dial.
func WithEgressHosts(hosts HostAllowed) EgressOption {
	if hosts == nil {
		panic("netutil: WithEgressHosts requires a function")
	}
	return func(c *egressConfig) { c.hosts = hosts }
}

// WithEgressInternalRanges replaces [DefaultInternalRanges] as what a
// listed host may reach. A prefix that overlaps link-local or a metadata
// range, or fails [ValidateAllowedNetwork], panics.
func WithEgressInternalRanges(prefixes ...netip.Prefix) EgressOption {
	checked := validEgressRanges("WithEgressInternalRanges", prefixes)
	return func(c *egressConfig) { c.internal = checked }
}

// WithEgressNetworks lets every host, listed or not, reach the given
// private networks (a known gateway, a test server on loopback); the
// scheme rules stay. The prefixes are checked as for
// [WithEgressInternalRanges].
func WithEgressNetworks(prefixes ...netip.Prefix) EgressOption {
	checked := validEgressRanges("WithEgressNetworks", prefixes)
	return func(c *egressConfig) { c.networks = append(c.networks, checked...) }
}

func validEgressRanges(option string, prefixes []netip.Prefix) []netip.Prefix {
	for _, p := range prefixes {
		if err := ValidateAllowedNetwork(p); err != nil {
			panic("netutil: " + option + ": " + err.Error())
		}
		for _, never := range neverReachable {
			if never.Overlaps(p) {
				panic("netutil: " + option + ": a prefix overlaps link-local or a metadata service")
			}
		}
	}
	return append([]netip.Prefix(nil), prefixes...)
}

// WithEgressMaxRedirects bounds the redirects of one call (default 3;
// zero follows none).
func WithEgressMaxRedirects(n int) EgressOption {
	if n < 0 {
		panic("netutil: WithEgressMaxRedirects requires zero or more")
	}
	return func(c *egressConfig) { c.maxRedirects = n }
}

// WithEgressStripOnRedirect drops the request headers whose names start
// with one of prefixes (case-insensitive) when a redirect leads to another
// host than the first request's: a signature is for its own host. Go
// already drops Authorization and Cookie there.
func WithEgressStripOnRedirect(prefixes ...string) EgressOption {
	cloned := append([]string(nil), prefixes...)
	return func(c *egressConfig) { c.stripHeaders = append(c.stripHeaders, cloned...) }
}

// WithEgressTimeout bounds a whole call, body included (default 10 s).
func WithEgressTimeout(d time.Duration) EgressOption {
	if d <= 0 {
		panic("netutil: WithEgressTimeout requires a positive duration")
	}
	return func(c *egressConfig) { c.timeout = d }
}

// WithEgressDialTimeout bounds one connection attempt (default 5 s).
func WithEgressDialTimeout(d time.Duration) EgressOption {
	if d <= 0 {
		panic("netutil: WithEgressDialTimeout requires a positive duration")
	}
	return func(c *egressConfig) { c.dialTimeout = d }
}

// WithEgressRootCAs trusts these CAs for https instead of the system's.
func WithEgressRootCAs(roots *x509.CertPool) EgressOption {
	return func(c *egressConfig) { c.roots = roots }
}

// WithEgressMaxResponse bounds what is read of a response body: a read
// past n bytes fails with [ErrEgressResponseTooLarge].
func WithEgressMaxResponse(n int64) EgressOption {
	if n <= 0 {
		panic("netutil: WithEgressMaxResponse requires a positive bound")
	}
	return func(c *egressConfig) { c.maxResponse = n }
}

// WithEgressUserAgent sets the User-Agent of a request that has none.
func WithEgressUserAgent(ua string) EgressOption {
	return func(c *egressConfig) { c.userAgent = ua }
}

func collectEgress(opts []EgressOption) *egressConfig {
	c := &egressConfig{
		internal: DefaultInternalRanges, maxRedirects: defaultEgressMaxRedirects,
		timeout: defaultEgressTimeout, dialTimeout: defaultEgressDialTimeout,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("netutil: EgressOption must not be nil")
		}
		opt(c)
	}
	return c
}

// NewEgressClient returns an http.Client for calls to addresses a caller
// names (webhooks, an identity provider an admin configured, a file URL).
// Every connection passes the guard at connect time, after DNS, so
// neither a redirect nor DNS rebinding reaches a private network:
// public addresses only, and for a listed host ([WithEgressHosts]) the
// internal ranges too; link-local and metadata never. https only, http to
// a listed host. No proxy from the environment, so the guard sees the
// target. Redirects are bounded, never go from https to http, never lead
// from a host not listed into a listed one, and lose the headers of
// [WithEgressStripOnRedirect] on another host.
func NewEgressClient(opts ...EgressOption) *http.Client {
	c := collectEgress(opts)
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           c.dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.roots},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   c.dialTimeout,
		ResponseHeaderTimeout: c.timeout,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       time.Minute,
	}
	return &http.Client{Transport: egressTransport{c: c, next: transport}, CheckRedirect: c.checkRedirect, Timeout: c.timeout}
}

// EgressDialer is the guarded DialContext of [NewEgressClient], for a
// transport of the caller's own. It judges addresses only; the scheme
// and redirect rules are the client's.
func EgressDialer(opts ...EgressOption) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return collectEgress(opts).dial
}

// dial resolves through the system and lets Control judge each address
// the socket is about to connect to.
func (c *egressConfig) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: dial address", ErrEgressRefused)
	}
	listed, err := c.listedAt(ctx, host, port)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: c.dialTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		return c.check(address, listed)
	}}
	return d.DialContext(ctx, network, addr)
}

// check is the guard on one address ("ip:port").
func (c *egressConfig) check(address string, listed bool) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparsable address", ErrEgressRefused)
	}
	ip := ap.Addr().Unmap()
	switch {
	case containedIn(neverReachable, ip):
		return fmt.Errorf("%w: link-local or metadata address", ErrEgressRefused)
	case !IsPrivateIP(ip.AsSlice()):
		return nil
	case containedIn(c.networks, ip), listed && containedIn(c.internal, ip):
		return nil
	}
	return fmt.Errorf("%w: private address", ErrEgressRefused)
}

func containedIn(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (c *egressConfig) listedAt(ctx context.Context, host, port string) (bool, error) {
	if c.hosts == nil {
		return false, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return false, fmt.Errorf("%w: port", ErrEgressRefused)
	}
	ok, err := c.hosts(ctx, host, n)
	if err != nil {
		return false, fmt.Errorf("netutil: egress allowlist: %w", err)
	}
	return ok, nil
}

// limitedBody fails a read past its bound instead of cutting the body
// short silently.
type limitedBody struct {
	io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	// One byte past the bound tells a body that ends there from a longer one.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.left {
		n, b.left = int(b.left), 0
		return n, ErrEgressResponseTooLarge
	}
	b.left -= int64(n)
	return n, err
}
