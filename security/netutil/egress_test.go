package netutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEgressCheck_RefusesPrivateAndReservedAddresses(t *testing.T) {
	t.Parallel()
	c := collectEgress(nil)
	for address, want := range map[string]bool{
		"8.8.8.8:443":                true,
		"[2001:4860:4860::8888]:443": true,
		"127.0.0.1:80":               false,
		"10.1.2.3:80":                false,
		"172.30.1.10:8443":           false,
		"192.168.1.1:80":             false,
		"100.64.0.1:80":              false,
		"169.254.169.254:80":         false,
		"0.0.0.0:80":                 false,
		"[::1]:80":                   false,
		"[fe80::1]:80":               false,
		"[fc00::1]:80":               false,
		"[::ffff:10.0.0.1]:80":       false,
		"[fd00:ec2::254]:80":         false,
		"[64:ff9b::a00:1]:80":        false, // NAT64 of 10.0.0.1
		"[2002:a00:1::1]:80":         false, // 6to4 of 10.0.0.1
	} {
		err := c.check(address, false)
		assert.Equal(t, want, err == nil, "%s: %v", address, err)
		if err != nil {
			assert.ErrorIs(t, err, ErrEgressRefused, address)
		}
	}
}

// A listed host reaches the internal ranges only; link-local, metadata,
// multicast, broadcast and the IPv6 ranges that carry IPv4 stay refused.
func TestEgressCheck_AListedHostReachesInternalRangesButNeverMetadata(t *testing.T) {
	t.Parallel()
	c := collectEgress(nil)
	for _, address := range []string{"127.0.0.1:80", "10.1.2.3:80", "172.16.0.5:80", "192.168.1.1:80", "[fd12::1]:80", "100.64.0.1:80", "[::1]:80", "8.8.8.8:443"} {
		assert.NoError(t, c.check(address, true), address)
	}
	for _, address := range []string{
		"169.254.169.254:80", // AWS, GCP, Azure metadata
		"169.254.170.2:80",   // ECS task credentials
		"[fd00:ec2::254]:80", // AWS IMDS over IPv6
		"[fd00:ec2::23]:80",  // EKS Pod Identity
		"100.100.100.200:80", // Alibaba metadata
		"[fe80::1]:80", "224.0.0.1:80", "255.255.255.255:80", "0.0.0.0:80", "[::]:80",
		"[64:ff9b::a00:1]:80", "[2002:a00:1::1]:80", "[2001::1]:80", "[::a00:1]:80",
	} {
		assert.ErrorIs(t, c.check(address, true), ErrEgressRefused, address)
	}
	narrow := collectEgress([]EgressOption{WithEgressInternalRanges(netip.MustParsePrefix("10.1.0.0/16"))})
	assert.NoError(t, narrow.check("10.1.2.3:80", true))
	assert.ErrorIs(t, narrow.check("10.2.0.1:80", true), ErrEgressRefused)
}

func TestEgressRanges_RefuseMetadataAndLinkLocal(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"169.254.0.0/16", "100.100.0.0/16", "fd00::/8", "fe80::/10"} {
		assert.Panics(t, func() { WithEgressInternalRanges(netip.MustParsePrefix(p)) }, p)
		assert.Panics(t, func() { WithEgressNetworks(netip.MustParsePrefix(p)) }, p)
	}
}

// allowHosts lists exactly the given host:port entries.
func allowHosts(entries ...string) EgressOption {
	return WithEgressHosts(func(_ context.Context, host string, port int) (bool, error) {
		return slices.Contains(entries, net.JoinHostPort(host, strconv.Itoa(port))), nil
	})
}

// loopback lets a test server on loopback stand for a public host.
var loopback = WithEgressNetworks(netip.MustParsePrefix("127.0.0.0/8"))

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Host
}

func call(t *testing.T, c *http.Client, target string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("X-Example-Signature", "sha256=x")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func serve(t *testing.T, tls bool, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func answer(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, text) }
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}
}

func rootsOf(srv *httptest.Server) EgressOption {
	return WithEgressRootCAs(srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs)
}

func TestEgressClient_OnlyAListedHostAndPortReachInside(t *testing.T) {
	t.Parallel()
	srv := serve(t, false, answer("ok"))
	_, err := call(t, NewEgressClient(allowHosts()), srv.URL)
	require.ErrorIs(t, err, ErrEgressRefused, "not listed")
	_, err = call(t, NewEgressClient(), srv.URL)
	require.ErrorIs(t, err, ErrEgressRefused, "no allowlist")
	body, err := call(t, NewEgressClient(allowHosts(hostOf(t, srv.URL))), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "ok", body)
	u, _ := url.Parse(srv.URL)
	_, err = call(t, NewEgressClient(allowHosts(net.JoinHostPort(u.Hostname(), "1"))), srv.URL)
	require.ErrorIs(t, err, ErrEgressRefused, "another port of a listed host")
}

func TestEgressClient_PlainHTTPGoesOnlyToAListedHost(t *testing.T) {
	t.Parallel()
	srv := serve(t, false, answer("ok"))
	_, err := call(t, NewEgressClient(loopback), srv.URL)
	require.ErrorIs(t, err, ErrEgressRefused)
}

// A name is judged by the address it resolves to at the connection:
// "localhost" passes no URL check but is refused, as would be any name
// rebound to a private address.
func TestEgressClient_ANameIsJudgedByItsAddress(t *testing.T) {
	t.Parallel()
	srv := serve(t, false, answer("ok"))
	u, _ := url.Parse(srv.URL)
	u.Host = "localhost:" + u.Port()
	_, err := call(t, NewEgressClient(allowHosts(hostOf(t, srv.URL))), u.String())
	require.ErrorIs(t, err, ErrEgressRefused)
}

func TestEgressClient_ARedirectPassesTheGuardAgainAndLosesTheSignature(t *testing.T) {
	t.Parallel()
	var signature string
	target := serve(t, false, func(_ http.ResponseWriter, r *http.Request) { signature = r.Header.Get("X-Example-Signature") })
	u, _ := url.Parse(target.URL)
	u.Host = "localhost:" + u.Port()
	redirect := serve(t, false, redirectTo(u.String()))
	_, err := call(t, NewEgressClient(allowHosts(hostOf(t, redirect.URL))), redirect.URL)
	require.ErrorIs(t, err, ErrEgressRefused, "a redirect to a host not listed")
	both := NewEgressClient(allowHosts(hostOf(t, redirect.URL), u.Host), WithEgressStripOnRedirect("x-example-"))
	_, err = call(t, both, redirect.URL)
	require.NoError(t, err)
	assert.Empty(t, signature, "another host does not get the signature")
}

func TestEgressClient_ARedirectFromAHostNotListedIntoAListedOneIsRefused(t *testing.T) {
	t.Parallel()
	inside := serve(t, true, answer("inside"))
	redirect := serve(t, true, redirectTo(inside.URL))
	c := NewEgressClient(allowHosts(hostOf(t, inside.URL)), loopback, rootsOf(inside))
	body, err := call(t, c, inside.URL)
	require.NoError(t, err)
	assert.Equal(t, "inside", body)
	_, err = call(t, c, redirect.URL)
	require.ErrorIs(t, err, ErrEgressRefused)
	assert.Contains(t, err.Error(), "into a listed host")
}

// A listed host may send a call on to a public one, which cannot send it
// back inside: each hop is judged by the one before it.
func TestEgressClient_APublicHopBetweenListedHostsCannotLeadInside(t *testing.T) {
	t.Parallel()
	inside := serve(t, true, answer("inside"))
	public := serve(t, true, redirectTo(inside.URL))
	listed := serve(t, true, redirectTo(public.URL))
	c := NewEgressClient(allowHosts(hostOf(t, listed.URL), hostOf(t, inside.URL)), loopback, rootsOf(inside))
	_, err := call(t, c, listed.URL)
	require.ErrorIs(t, err, ErrEgressRefused)
	assert.Contains(t, err.Error(), "into a listed host")
}

func TestEgressClient_ARedirectNeverLowersTheScheme(t *testing.T) {
	t.Parallel()
	plain := serve(t, false, answer("ok"))
	secure := serve(t, true, redirectTo(plain.URL))
	c := NewEgressClient(allowHosts(hostOf(t, secure.URL), hostOf(t, plain.URL)), rootsOf(secure))
	_, err := call(t, c, secure.URL)
	require.ErrorIs(t, err, ErrEgressRefused)
}

func TestEgressClient_RedirectsAreBounded(t *testing.T) {
	t.Parallel()
	var hops int
	var srv *httptest.Server
	srv = serve(t, true, func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, srv.URL+"/"+strconv.Itoa(hops), http.StatusTemporaryRedirect)
	})
	_, err := call(t, NewEgressClient(loopback, rootsOf(srv), WithEgressMaxRedirects(2)), srv.URL)
	require.ErrorIs(t, err, ErrEgressRefused)
	assert.Equal(t, 3, hops, "the first call and two redirects")
}

func TestEgressClient_AResponseIsReadUpToItsBound(t *testing.T) {
	t.Parallel()
	srv := serve(t, true, answer(strings.Repeat("x", 100)))
	_, err := call(t, NewEgressClient(loopback, rootsOf(srv), WithEgressMaxResponse(99)), srv.URL)
	require.ErrorIs(t, err, ErrEgressResponseTooLarge)
	body, err := call(t, NewEgressClient(loopback, rootsOf(srv), WithEgressMaxResponse(100)), srv.URL)
	require.NoError(t, err)
	assert.Len(t, body, 100)
}

func TestEgressClient_SetsTheUserAgent(t *testing.T) {
	t.Parallel()
	var ua string
	srv := serve(t, true, func(_ http.ResponseWriter, r *http.Request) { ua = r.UserAgent() })
	_, err := call(t, NewEgressClient(loopback, rootsOf(srv), WithEgressUserAgent("example/1")), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "example/1", ua)
}

func TestEgressClient_AnAllowlistErrorFailsTheCall(t *testing.T) {
	t.Parallel()
	down := errors.New("allowlist down")
	c := NewEgressClient(WithEgressHosts(func(context.Context, string, int) (bool, error) { return false, down }))
	_, err := call(t, c, "https://hooks.example.invalid/x")
	require.ErrorIs(t, err, down)
}

func TestLimitedBody_EndsAtTheBound(t *testing.T) {
	t.Parallel()
	b := &limitedBody{ReadCloser: io.NopCloser(bytes.NewReader([]byte("abcd"))), left: 4}
	got, err := io.ReadAll(b)
	require.NoError(t, err)
	assert.Equal(t, "abcd", string(got))
	b = &limitedBody{ReadCloser: io.NopCloser(bytes.NewReader([]byte("abcde"))), left: 4}
	got, err = io.ReadAll(b)
	require.ErrorIs(t, err, ErrEgressResponseTooLarge)
	assert.Equal(t, "abcd", string(got))
}

func TestEgressOptions_PanicOnInvalidValues(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { WithEgressHosts(nil) })
	assert.Panics(t, func() { WithEgressMaxRedirects(-1) })
	assert.Panics(t, func() { WithEgressTimeout(0) })
	assert.Panics(t, func() { WithEgressDialTimeout(0) })
	assert.Panics(t, func() { WithEgressMaxResponse(0) })
	assert.Panics(t, func() { NewEgressClient(nil) })
}

func TestEgressDialer_JudgesEveryAddress(t *testing.T) {
	t.Parallel()
	srv := serve(t, false, answer("ok"))
	_, err := EgressDialer()(t.Context(), "tcp", hostOf(t, srv.URL))
	require.ErrorIs(t, err, ErrEgressRefused)
	conn, err := EgressDialer(allowHosts(hostOf(t, srv.URL)))(t.Context(), "tcp", hostOf(t, srv.URL))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}
