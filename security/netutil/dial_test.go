package netutil

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func addrsOf(raw ...string) []net.IPAddr {
	out := make([]net.IPAddr, len(raw))
	for i, r := range raw {
		out[i] = net.IPAddr{IP: net.ParseIP(r)}
	}
	return out
}

func strs(addrs []netip.Addr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

func TestPermitted_AlternatesFamiliesFromTheFirstAnswer(t *testing.T) {
	for name, tt := range map[string]struct {
		answer []string
		want   []string
	}{
		"IPv4 first": {
			answer: []string{"93.184.216.34", "93.184.216.35", "2606:2800:220:1::1", "10.0.0.1"},
			want:   []string{"93.184.216.34", "2606:2800:220:1::1", "93.184.216.35"},
		},
		"IPv6 first": {
			answer: []string{"2606:2800:220:1::1", "fd00::1", "93.184.216.34", "2606:2800:220:1::2"},
			want:   []string{"2606:2800:220:1::1", "93.184.216.34", "2606:2800:220:1::2"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := permitted(context.Background(), "example.com", &mockDNSResolver{ips: addrsOf(tt.answer...)}, collectOptions(nil))
			require.NoError(t, err)
			assert.Equal(t, tt.want, strs(got))
		})
	}
}

func TestPermitted_SynthesizedNAT64OffersTheIPv4AddressThroughThePrefixFirst(t *testing.T) {
	resolver := &mockDNSResolver{ips: addrsOf("93.184.216.34", "10.0.0.1")}
	got, err := permitted(context.Background(), "example.com", resolver, collectOptions([]Option{WithNAT64(WellKnownNAT64), WithSynthesizedNAT64()}))
	require.NoError(t, err)
	assert.Equal(t, []string{"64:ff9b::5db8:d822", "93.184.216.34"}, strs(got), "the private address stays out, also through the prefix")

	got, err = permitted(context.Background(), "example.com", resolver, collectOptions([]Option{WithNAT64(WellKnownNAT64)}))
	require.NoError(t, err)
	assert.Equal(t, []string{"93.184.216.34"}, strs(got), "the prefix alone accepts DNS64 answers and synthesizes none")

	got, err = permitted(context.Background(), "64:ff9b::5db8:d822", nil, collectOptions([]Option{WithNAT64(WellKnownNAT64)}))
	require.NoError(t, err)
	assert.Equal(t, []string{"64:ff9b::5db8:d822"}, strs(got), "a DNS64 answer is dialed as it is")

	_, err = permitted(context.Background(), "64:ff9b::a00:1", nil, collectOptions([]Option{WithNAT64(WellKnownNAT64)}))
	assert.ErrorContains(t, err, "private/reserved")
}

func TestSSRFSafeTransport_FallsBackToTheOtherFamily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	// The server listens on 127.0.0.1 only, so its port on ::1 refuses:
	// the first, IPv6 address fails and the IPv4 one answers.
	resolver := &mockDNSResolver{ips: addrsOf("::1", "127.0.0.1")}
	client, first, err := SSRFSafeClient(context.Background(), "receiver.example.com", resolver, WithAllowPrivateIPs())
	require.NoError(t, err)
	assert.Equal(t, "::1", first)
	resp, err := client.Get("http://receiver.example.com:" + u.Port() + "/")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestSSRFSafeTransport_DeliversToAnIPv6Receiver(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback on this host")
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	client := SSRFSafeClientFollowRedirects(1, &mockDNSResolver{ips: addrsOf("::1")}, WithAllowPrivateIPs())
	resp, err := client.Get("http://receiver.example.com:" + port + "/")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestDialFirst_KeepsToTheRequestedFamily(t *testing.T) {
	_, err := dialFirst(context.Background(), "tcp4", []netip.Addr{netip.MustParseAddr("::1")}, "80")
	assert.ErrorContains(t, err, "no permitted address of the tcp4 family")
}

func TestPermitted_TheNAT64PrefixAloneKeepsAnIPv4AnswerFirst(t *testing.T) {
	resolver := &mockDNSResolver{ips: addrsOf("93.184.216.34", "2606:2800:220:1::1")}
	got, err := permitted(context.Background(), "example.com", resolver, collectOptions([]Option{WithNAT64(WellKnownNAT64)}))
	require.NoError(t, err)
	assert.Equal(t, []string{"93.184.216.34", "2606:2800:220:1::1"}, strs(got))
}

// hanging records the addresses dialed and answers none before its
// attempt's deadline.
type hanging struct {
	mu     sync.Mutex
	dialed []string
}

func (h *hanging) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	h.mu.Lock()
	h.dialed = append(h.dialed, addr)
	h.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDialWithin_ManyAddressesThatHangEndWithinTheBudget(t *testing.T) {
	var addrs []netip.Addr
	for i := range 20 {
		addrs = append(addrs, netip.AddrFrom4([4]byte{93, 184, 216, byte(i + 1)}))
	}
	h := &hanging{}
	const budget = 120 * time.Millisecond
	start := time.Now()
	_, err := dialWithin(context.Background(), "tcp", addrs, "443", budget, h.dial)
	require.Error(t, err)
	assert.Less(t, time.Since(start), budget+100*time.Millisecond)
	assert.Len(t, h.dialed, maxDialAttempts, "only the first addresses are tried")
}

func TestDialWithin_SynthesisOnlyWhereTheHostHasNoIPv4Route(t *testing.T) {
	resolver := &mockDNSResolver{ips: addrsOf("93.184.216.34")}
	for name, tt := range map[string]struct {
		opts  []Option
		first string
	}{
		"IPv6-only host":              {[]Option{WithNAT64(WellKnownNAT64), WithSynthesizedNAT64()}, "[64:ff9b::5db8:d822]:443"},
		"dual-stack host, DNS64 only": {[]Option{WithNAT64(WellKnownNAT64)}, "93.184.216.34:443"},
		"dual-stack host":             {nil, "93.184.216.34:443"},
	} {
		t.Run(name, func(t *testing.T) {
			addrs, err := permitted(context.Background(), "example.com", resolver, collectOptions(tt.opts))
			require.NoError(t, err)
			h := &hanging{}
			_, _ = dialWithin(context.Background(), "tcp", addrs, "443", 20*time.Millisecond, h.dial)
			require.NotEmpty(t, h.dialed)
			assert.Equal(t, tt.first, h.dialed[0])
			if len(tt.opts) < 2 {
				for _, a := range h.dialed {
					assert.NotContains(t, a, "64:ff9b::", "no address through the prefix")
				}
			}
		})
	}
}

func TestSSRFSafeDialer_NamesWhyItDidNotConnect(t *testing.T) {
	ctx := context.Background()
	blocked := SSRFSafeDialer(&mockDNSResolver{ips: addrsOf("10.0.0.1", "127.0.0.1")})
	_, err := blocked(ctx, "tcp", "internal.example.com:443")
	assert.ErrorIs(t, err, ErrBlocked)
	_, err = blocked(ctx, "tcp", "169.254.169.254:80")
	assert.ErrorIs(t, err, ErrBlocked)
	_, err = blocked(ctx, "tcp", "no-port.example.com")
	assert.ErrorIs(t, err, ErrBlocked)
	_, err = blocked(ctx, "tcp", "bad host:443")
	assert.ErrorIs(t, err, ErrBlocked)
	_, err = SSRFSafeDialer(&mockDNSResolver{ips: addrsOf("::1")}, WithAllowPrivateIPs())(ctx, "tcp4", "v6only.example.com:80")
	assert.ErrorIs(t, err, ErrBlocked)
	_, err = SSRFSafeDialer(&mockDNSResolver{err: &net.DNSError{Err: "no such host", IsNotFound: true}})(ctx, "tcp", "gone.example.com:443")
	assert.ErrorIs(t, err, ErrUnresolved)
	_, err = SSRFSafeDialer(&mockDNSResolver{})(ctx, "tcp", "empty.example.com:443")
	assert.ErrorIs(t, err, ErrUnresolved)
}

func TestSSRFSafeDialer_ConnectsToAPermittedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	conn, err := SSRFSafeDialer(&mockDNSResolver{ips: addrsOf("127.0.0.1")}, WithAllowPrivateIPs())(context.Background(), "tcp", "receiver.example.com:"+port)
	require.NoError(t, err)
	_ = conn.Close()
}
