package netutil

import (
	"context"
	"net"
	"net/netip"
	"testing"
)

type fixedResolver []string

func (f fixedResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	out := make([]net.IPAddr, len(f))
	for i, s := range f {
		out[i] = net.IPAddr{IP: net.ParseIP(s)}
	}
	return out, nil
}

func TestAllowedNetworksOpenOnlyTheirOwnRange(t *testing.T) {
	siem := WithAllowedNetworks(netip.MustParsePrefix("10.20.0.0/16"))
	cases := []struct {
		host string
		ok   bool
	}{
		{"10.20.4.5", true},
		{"10.21.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"127.0.0.1", false},
		{"0.0.0.0", false},
		{"::ffff:10.20.4.5", true},
		{"93.184.216.34", true},
	}
	for _, tc := range cases {
		if _, err := ResolveAndValidate(context.Background(), tc.host, nil, siem); (err == nil) != tc.ok {
			t.Errorf("%s: %v, want ok=%v", tc.host, err, tc.ok)
		}
	}
	ip, err := ResolveAndValidate(context.Background(), "siem.internal", fixedResolver{"192.168.1.9", "10.20.0.7"}, siem)
	if err != nil || ip != "10.20.0.7" {
		t.Errorf("resolved %q, %v; want the allowed address", ip, err)
	}
}

func TestAllowedNetworksRejectsWidePrefixes(t *testing.T) {
	for _, p := range []string{
		"0.0.0.0/0", "10.0.0.0/7", "::/0", "fc00::/7", "fd00::/16",
		"2002:a9fe::/48", "64:ff9b::a9fe:a9fe/128", "::ffff:169.254.169.254/128", "2001:0:a9fe::/48",
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s accepted", p)
				}
			}()
			WithAllowedNetworks(netip.MustParsePrefix(p))
		}()
	}
}

func TestAllowedIPv6NetworkStaysNarrow(t *testing.T) {
	if _, err := ResolveAndValidate(context.Background(), "::1", nil, WithAllowedNetworks(netip.MustParsePrefix("::1/128"))); err != nil {
		t.Errorf("IPv6 loopback when allowed: %v", err)
	}
	opt := WithAllowedNetworks(netip.MustParsePrefix("fd12:3456:789a::/48"))
	for host, ok := range map[string]bool{
		"fd12:3456:789a::5":      true,
		"fd12:3456:789b::5":      false,
		"2002:a9fe:a9fe::1":      false,
		"::ffff:169.254.169.254": false,
	} {
		if _, err := ResolveAndValidate(context.Background(), host, nil, opt); (err == nil) != ok {
			t.Errorf("%s: %v, want ok=%v", host, err, ok)
		}
	}
}
