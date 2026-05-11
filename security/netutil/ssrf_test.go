package netutil

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAndValidate_AllowPrivateWarningRedactsHost(t *testing.T) {
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got, err := ResolveAndValidate(context.Background(), "10.0.0.1", nil, WithAllowPrivateIPs())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", got)

	out := buf.String()
	assert.Contains(t, out, `"host"`)
	assert.NotContains(t, out, "10.0.0.1")
	assert.True(t, strings.Contains(out, "<redacted"), "warning should keep redacted host shape: %q", out)
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		private bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v4 other", "127.0.0.2", true},
		{"10.x private", "10.0.0.1", true},
		{"172.16 private", "172.16.0.1", true},
		{"172.31 private", "172.31.255.255", true},
		{"192.168 private", "192.168.1.1", true},
		{"link-local", "169.254.1.1", true},
		{"zero network", "0.0.0.1", true},
		{"CGNAT", "100.64.0.1", true},
		{"CGNAT upper", "100.127.255.255", true},
		{"reserved 240", "240.0.0.1", true},
		{"IETF protocol", "192.0.0.1", true},
		{"TEST-NET-1", "192.0.2.1", true},
		{"benchmarking", "198.18.0.1", true},
		{"benchmarking upper", "198.19.255.255", true},
		{"TEST-NET-2", "198.51.100.1", true},
		{"TEST-NET-3", "203.0.113.1", true},
		{"6to4 relay", "192.88.99.1", true},
		{"public IP", "8.8.8.8", false},
		{"public IP 2", "1.1.1.1", false},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		{"loopback v6", "::1", true},
		{"link-local v6", "fe80::1", true},
		{"multicast v6", "ff02::1", true},
		{"IPv4-mapped v6 private", "::ffff:192.168.1.1", true},
		{"IPv4-mapped v6 public", "::ffff:8.8.8.8", false},
		{"ipv6 ULA fd00::", "fd00::1", true},
		{"ipv6 ULA fc00::", "fc00::1", true},
		{"ipv6-mapped 127.0.0.1", "::ffff:127.0.0.1", true},
		{"ipv6-mapped 10.0.0.1", "::ffff:10.0.0.1", true},
		{"Teredo", "2001:0000:4136:e378:8000:63bf:3fff:fdd2", true},
		{"6to4", "2002:c0a8:0101::1", true},
		{"NAT64", "64:ff9b::192.168.1.1", true},
		{"local-use IPv4/IPv6 translation", "64:ff9b:1::1", true},
		{"discard-only", "100::1", true},
		{"dummy IPv6 prefix", "100:0:0:1::1", true},
		{"IPv6 benchmarking", "2001:2::1", true},
		{"deprecated ORCHID", "2001:10::1", true},
		{"documentation 2001:db8", "2001:db8::1", true},
		{"documentation 3fff", "3fff::1", true},
		{"SRv6 SIDs", "5f00::1", true},
		{"public IPv6", "2607:f8b0:4004:800::200e", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "failed to parse IP %s", tt.ip)
			assert.Equal(t, tt.private, IsPrivateIP(ip), "IsPrivateIP(%s)", tt.ip)
		})
	}
}

func TestIsPrivateIP_InvalidIPIsUnsafe(t *testing.T) {
	assert.True(t, IsPrivateIP(nil))
	assert.True(t, IsPrivateIP(net.IP{}))
}

func TestResolveAndValidate_Loopback(t *testing.T) {
	_, err := ResolveAndValidate(context.Background(), "localhost", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestResolveAndValidate_NonexistentDomain(t *testing.T) {
	_, err := ResolveAndValidate(context.Background(), "this-domain-does-not-exist-9999.invalid", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dns resolution failed")
}

func TestResolveAndValidate_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = ResolveAndValidate(context.Background(), "example.com", &mockDNSResolver{}, nil)
	})
}

func TestResolveAndValidate_RejectsInvalidHostInput(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	for _, host := range []string{
		"",
		"example.com:443",
		" example.com",
		"example.com\nx",
		"example.com\x07x",
		"example.com/path",
		`example.com\path`,
		"[::1]",
		"fe80::1%25lo0",
		"example.com%2fmetadata",
	} {
		t.Run(host, func(t *testing.T) {
			_, err := ResolveAndValidate(context.Background(), host, resolver)
			assert.Error(t, err)
		})
	}
}

func TestResolveAndValidate_ErrorsDoNotEchoHostValues(t *testing.T) {
	resolver := &mockDNSResolver{err: fmt.Errorf("lookup failed")}

	tests := []struct {
		name string
		host string
	}{
		{name: "invalid host", host: "secret-token/path"},
		{name: "dns failure", host: "secret-token.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveAndValidate(context.Background(), tt.host, resolver)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestResolveAndValidate_IPLiteralDoesNotUseResolver(t *testing.T) {
	resolver := &recordingResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}

	_, err := ResolveAndValidate(context.Background(), "127.0.0.1", resolver)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")

	ip, err := ResolveAndValidate(context.Background(), "8.8.8.8", resolver)
	require.NoError(t, err)
	assert.Equal(t, "8.8.8.8", ip)
	assert.Zero(t, resolver.calls, "literal IP hosts must be classified without DNS lookup")
}

func TestWithMinTLSVersion_PanicsBelowTLS12(t *testing.T) {
	assert.Panics(t, func() {
		WithMinTLSVersion(0)
	})
}

// mockDNSResolver returns predefined IPs for testing SSRF validation.
type mockDNSResolver struct {
	ips []net.IPAddr
	err error
}

func (m *mockDNSResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return m.ips, m.err
}

func TestResolveAndValidate_WithMockResolver(t *testing.T) {
	tests := []struct {
		name      string
		ips       []net.IPAddr
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "public IP passes",
			ips:     []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
			wantErr: false,
		},
		{
			name:      "private IP rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name:      "IPv6 loopback rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("::1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name:      "IPv6 ULA rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("fd00::1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name:      "IPv4-mapped private rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("::ffff:10.0.0.1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name: "mixed IPs returns first public",
			ips: []net.IPAddr{
				{IP: net.ParseIP("10.0.0.1")},
				{IP: net.ParseIP("8.8.8.8")},
			},
			wantErr: false,
		},
		{
			name:      "empty result",
			ips:       []net.IPAddr{},
			wantErr:   true,
			errSubstr: "no addresses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &mockDNSResolver{ips: tt.ips}
			ip, err := ResolveAndValidate(context.Background(), "example.com", resolver)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, ip)
			}
		})
	}
}

func TestResolveAndValidate_DNSError(t *testing.T) {
	resolver := &mockDNSResolver{err: fmt.Errorf("no such host")}
	_, err := ResolveAndValidate(context.Background(), "bad.host", resolver)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dns resolution failed")
}

func TestResolveAndValidate_AllowPrivateIPs(t *testing.T) {
	tests := []struct {
		name string
		ips  []net.IPAddr
		want string
	}{
		{
			name: "loopback allowed in dev mode",
			ips:  []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}},
			want: "127.0.0.1",
		},
		{
			name: "private 10.x allowed in dev mode",
			ips:  []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}},
			want: "10.0.0.1",
		},
		{
			name: "IPv6 loopback allowed in dev mode",
			ips:  []net.IPAddr{{IP: net.ParseIP("::1")}},
			want: "::1",
		},
		{
			name: "public IP still works in dev mode",
			ips:  []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}},
			want: "8.8.8.8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &mockDNSResolver{ips: tt.ips}
			ip, err := ResolveAndValidate(context.Background(), "example.com", resolver, WithAllowPrivateIPs())
			assert.NoError(t, err)
			assert.Equal(t, tt.want, ip)
		})
	}
}

func TestResolveAndValidate_DefaultRejectsPrivate(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	_, err := ResolveAndValidate(context.Background(), "example.com", resolver)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestSSRFSafeClient_AllowPrivateIPs(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	client, ip, err := SSRFSafeClient(context.Background(), "localhost", resolver, WithAllowPrivateIPs())
	assert.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ip)
	assert.NotNil(t, client)
}

func TestSSRFSafeTransport_AllowPrivateIPs(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	transport, ip, err := SSRFSafeTransport(context.Background(), "localhost", resolver, WithAllowPrivateIPs())
	assert.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ip)
	assert.NotNil(t, transport)
}

func TestSSRFSafeTransport_RejectsDifferentDialHost(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	transport, _, err := SSRFSafeTransport(context.Background(), "example.com", resolver)
	require.NoError(t, err)

	conn, err := transport.DialContext(context.Background(), "tcp", "secret-token.example:80")
	if conn != nil {
		_ = conn.Close()
	}
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pinned to host")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSSRFSafeTransport_InvalidDialAddressDoesNotEchoValue(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	transport, _, err := SSRFSafeTransport(context.Background(), "example.com", resolver)
	require.NoError(t, err)

	conn, err := transport.DialContext(context.Background(), "tcp", "secret-token")
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSSRFSafeTransport_AllowsCaseVariantDialHost(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	transport, _, err := SSRFSafeTransport(context.Background(), "Example.COM", resolver, WithAllowPrivateIPs())
	require.NoError(t, err)

	conn, err := transport.DialContext(context.Background(), "tcp", "example.com:9")
	if conn != nil {
		_ = conn.Close()
	}
	assert.NotContains(t, fmt.Sprint(err), "pinned to host")
}

func TestWithRequestTimeout_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			assert.Panics(t, func() {
				WithRequestTimeout(d)
			})
		})
	}
}

func TestWithoutRequestTimeout_DisablesWholeRequestTimeout(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	client, _, err := SSRFSafeClient(context.Background(), "localhost", resolver,
		WithAllowPrivateIPs(),
		WithoutRequestTimeout(),
	)
	require.NoError(t, err)
	assert.Zero(t, client.Timeout)
}

// --- SSRFSafeClientFromURL / SSRFSafeTransportFromURL ---

func TestSSRFSafeClientFromURL_Happy(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	client, u, ip, err := SSRFSafeClientFromURL(context.Background(),
		"https://localhost:9443/api/v1?x=y", resolver, WithAllowPrivateIPs())
	assert.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "localhost", u.Hostname())
	assert.Equal(t, "/api/v1", u.Path)
	assert.Equal(t, "127.0.0.1", ip)
}

func TestSSRFSafeTransportFromURL_Happy(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	tr, u, ip, err := SSRFSafeTransportFromURL(context.Background(),
		"http://localhost/healthz", resolver, WithAllowPrivateIPs())
	assert.NoError(t, err)
	assert.NotNil(t, tr)
	assert.Equal(t, "localhost", u.Hostname())
	assert.Equal(t, "127.0.0.1", ip)
}

func TestSSRFSafeClientFromURL_RejectsBadScheme(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	for _, raw := range []string{
		"file:///etc/passwd",
		"data:text/plain,hi",
		"gopher://internal/",
		"ftp://example.com/",
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, _, err := SSRFSafeClientFromURL(context.Background(), raw, resolver, WithAllowPrivateIPs())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "scheme")
		})
	}
}

func TestSSRFSafeClientFromURL_RejectsEmptyHost(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	_, _, _, err := SSRFSafeClientFromURL(context.Background(), "http://", resolver, WithAllowPrivateIPs())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty host")
}

func TestSSRFSafeClientFromURL_EmptyHostErrorDoesNotEchoQuery(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	_, _, _, err := SSRFSafeClientFromURL(context.Background(), "http://?token=secret-token", resolver, WithAllowPrivateIPs())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty host")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
}

func TestSSRFSafeClientFromURL_ParseErrorDoesNotEchoQuery(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	_, _, _, err := SSRFSafeClientFromURL(context.Background(), "http://example.com/%zz?token=secret-token", resolver, WithAllowPrivateIPs())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid URL syntax")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestSSRFSafeClientFromURL_RejectsInvalidPorts(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}
	for _, raw := range []string{
		"http://example.com:bad/",
		"http://example.com:secret-token/",
		"http://example.com:/",
		"http://example.com:0/",
		"http://example.com:65536/",
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, _, err := SSRFSafeClientFromURL(context.Background(), raw, resolver)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "port")
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestSSRFSafeClientFromURL_RejectsUserinfo(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}}
	_, _, _, err := SSRFSafeClientFromURL(context.Background(),
		"https://169.254.169.254@example.com/resource", resolver)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "userinfo")
}

func TestSSRFSafeClientFromURL_RejectsMalformedURL(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	// url.Parse is permissive — passing a control character forces a parse error.
	_, _, _, err := SSRFSafeClientFromURL(context.Background(), "http://example.com/\x00", resolver, WithAllowPrivateIPs())
	assert.Error(t, err)
}

// --- SSRFSafeDynamicTransport / SSRFSafeClientFollowRedirects ---

func TestSSRFSafeDynamicTransport_RejectsPrivateOnEachDial(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}}
	transport := SSRFSafeDynamicTransport(resolver)

	// DialContext is the SSRF guard for the dynamic transport. Calling it
	// with a private-resolving host must fail without ever opening a socket.
	_, err := transport.DialContext(context.Background(), "tcp", "victim.internal:443")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestSSRFSafeDynamicTransport_AllowsPublicOnEachDial(t *testing.T) {
	resolver := &mockDNSResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	transport := SSRFSafeDynamicTransport(resolver)

	// We don't actually want to connect to 8.8.8.8 in a unit test. The DNS
	// re-resolution is the security-relevant step; the dial that follows is
	// out of scope. Verify that the transport's resolver accepts the
	// public IP via the underlying ResolveAndValidate.
	ip, err := ResolveAndValidate(context.Background(), "victim.example.com", resolver)
	assert.NoError(t, err)
	assert.Equal(t, "8.8.8.8", ip)
	assert.NotNil(t, transport)
}

func TestSSRFSafeClientFollowRedirects_PanicsOnZeroMaxHops(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on maxHops=0")
		}
	}()
	_ = SSRFSafeClientFollowRedirects(0, nil)
}

func TestSSRFSafeValidatingTransport_ZeroValueFailsClosed(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/resource", nil)
	require.NoError(t, err)

	resp, err := ssrfValidatingTransport{}.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestSSRFSafeClientFollowRedirects_RejectsUnsafeInitialURL(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "bad scheme", target: "ftp://example.com/resource", want: "scheme"},
		{name: "userinfo", target: "http://user:pass@example.com/resource", want: "userinfo"},
		{name: "invalid port", target: "http://example.com:0/resource", want: "port"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := SSRFSafeClientFollowRedirects(5, nil, WithAllowPrivateIPs())
			resp, err := client.Get(tt.target)
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestSSRFSafeClientFollowRedirects_RejectsInitialPrivateHostBeforeDial(t *testing.T) {
	client := SSRFSafeClientFollowRedirects(5, nil)

	resp, err := client.Get("http://127.0.0.1/")
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestSSRFSafeClientFollowRedirects_StopsAfterMaxHops(t *testing.T) {
	// A test server that infinitely redirects to itself.
	hops := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()

	maxHops := 3
	client := SSRFSafeClientFollowRedirects(maxHops, nil, WithAllowPrivateIPs())

	resp, err := client.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
	}
	assert.Error(t, err, "expected redirect-chain-exceeded error")
	assert.Contains(t, err.Error(), "redirect chain exceeded")
}

func TestSSRFSafeClientFollowRedirects_AllowsExactlyMaxHops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/ok":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := SSRFSafeClientFollowRedirects(1, nil, WithAllowPrivateIPs())
	resp, err := client.Get(srv.URL + "/start")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSSRFSafeClientFollowRedirects_RejectsMoreThanMaxHops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/ok":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := SSRFSafeClientFollowRedirects(1, nil, WithAllowPrivateIPs())
	resp, err := client.Get(srv.URL + "/start")
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect chain exceeded")
}

func TestSSRFSafeClientFollowRedirects_RejectsUnsafeRedirectURL(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "bad scheme", target: "file:///etc/passwd", want: "scheme"},
		{name: "userinfo", target: "http://user:pass@example.com/", want: "userinfo"},
		{name: "invalid port", target: "http://example.com:0/", want: "port"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tt.target, http.StatusFound)
			}))
			defer srv.Close()

			client := SSRFSafeClientFollowRedirects(5, nil, WithAllowPrivateIPs())
			resp, err := client.Get(srv.URL)
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestSSRFSafeDynamicTransport_ResolverFiresOnEveryDial(t *testing.T) {
	// The DialContext is the SSRF guard. The HTTP layer reuses keep-alive
	// connections for same-host redirects, but every NEW host:port forces a
	// fresh dial — and that fresh dial MUST run the resolver. Verify by
	// calling DialContext directly with two different hosts.
	rec := &recordingResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	transport := SSRFSafeDynamicTransport(rec, WithAllowPrivateIPs())

	for _, host := range []string{"a.example.com:80", "b.example.com:80"} {
		conn, err := transport.DialContext(context.Background(), "tcp", host)
		if conn != nil {
			_ = conn.Close()
		}
		// Connect attempt may fail at TCP layer (host unreachable) — what
		// matters is that the resolver was consulted before we got there.
		_ = err
	}
	assert.Equal(t, 2, rec.calls,
		"each fresh dial through the dynamic transport must consult the resolver")
}

type recordingResolver struct {
	ips   []net.IPAddr
	calls int
}

func (r *recordingResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	r.calls++
	return r.ips, nil
}
