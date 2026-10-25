package netutil

import (
	"context"
	"net/netip"
	"testing"
)

// The examples of RFC 6052 section 2.4, all for 192.0.2.33.
var rfc6052Examples = []struct {
	prefix, nat64 string
}{
	{"2001:db8::/32", "2001:db8:c000:221::"},
	{"2001:db8:100::/40", "2001:db8:1c0:2:21::"},
	{"2001:db8:122::/48", "2001:db8:122:c000:2:2100::"},
	{"2001:db8:122:300::/56", "2001:db8:122:3c0:0:221::"},
	{"2001:db8:122:344::/64", "2001:db8:122:344:c0:2:2100:0"},
	{"2001:db8:122:344::/96", "2001:db8:122:344::c000:221"},
	{"64:ff9b::/96", "64:ff9b::c000:221"},
}

func TestNAT64EmbedsAndExtractsAtEveryPrefixLength(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.33")
	for _, tc := range rfc6052Examples {
		t.Run(tc.prefix, func(t *testing.T) {
			n, err := ParseNAT64Prefix(tc.prefix)
			if err != nil {
				t.Fatal(err)
			}
			want := netip.MustParseAddr(tc.nat64)
			if got, ok := n.Embed(v4); !ok || got != want {
				t.Errorf("Embed: %s %v, want %s", got, ok, want)
			}
			if got, ok := n.Extract(want); !ok || got != v4 {
				t.Errorf("Extract: %s %v, want %s", got, ok, v4)
			}
			if got, ok := n.Embed(netip.MustParseAddr("::ffff:192.0.2.33")); !ok || got != want {
				t.Errorf("Embed of the mapped form: %s %v", got, ok)
			}
		})
	}
}

func TestNAT64RefusesWhatNoGatewaySynthesizes(t *testing.T) {
	n, err := ParseNAT64Prefix("2001:db8:122::/48")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"2001:db8:122:c000:2ff:2100::", "2001:db8:123:c000:2:2100::", "192.0.2.33", "::ffff:192.0.2.33"} {
		if v4, ok := n.Extract(netip.MustParseAddr(a)); ok {
			t.Errorf("%s extracted as %s", a, v4)
		}
	}
	if !n.Contains(netip.MustParseAddr("2001:db8:122:c000:2ff:2100::")) {
		t.Error("an address with a set u octet is still in the prefix")
	}
	if _, ok := n.Embed(netip.MustParseAddr("2001:db8::1")); ok {
		t.Error("an IPv6 address embedded")
	}
	if _, ok := (NAT64Prefix{}).Embed(netip.MustParseAddr("192.0.2.33")); ok {
		t.Error("embedded without a prefix")
	}
}

func TestParseNAT64Prefix(t *testing.T) {
	if n, err := ParseNAT64Prefix("  "); err != nil || n.IsValid() {
		t.Errorf("empty: %v %v", n, err)
	}
	if n, err := ParseNAT64Prefix("64:ff9b::1:2/96"); err != nil || n.String() != "64:ff9b::/96" {
		t.Errorf("host bits are masked: %v %v", n, err)
	}
	for _, bad := range []string{
		"64:ff9b::", "10.0.0.0/8", "::ffff:0:0/96", "64:ff9b::/80", "2001:db8::/36", "2001:db8:0:0:100::/96", "x",
	} {
		if _, err := ParseNAT64Prefix(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestNAT64AddressesAreCheckedAsTheIPv4TheyCarry(t *testing.T) {
	custom, err := ParseNAT64Prefix("2001:db8:64::/48")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		opts  []Option
		host  string
		allow bool
	}{
		{"well-known, public", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::5db8:d822", true},
		{"well-known, private", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::a00:1", false},
		{"well-known, metadata", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::a9fe:a9fe", false},
		{"well-known, loopback", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::7f00:1", false},
		{"well-known, CGNAT", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::6440:1", false},
		{"well-known, documentation", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::c000:221", false},
		{"well-known, multicast", []Option{WithNAT64(WellKnownNAT64)}, "64:ff9b::e000:1", false},
		{"well-known without the option", nil, "64:ff9b::5db8:d822", false},
		{"custom /48, public", []Option{WithNAT64(custom)}, "2001:db8:64:5db8:d8:2200::", true},
		{"custom /48, private", []Option{WithNAT64(custom)}, "2001:db8:64:a00:0:100::", false},
		{"custom /48, u octet set", []Option{WithNAT64(custom)}, "2001:db8:64:5db8:ffd8:2200::", false},
		{"allowed network through NAT64", []Option{WithNAT64(WellKnownNAT64), WithAllowedNetworks(netip.MustParsePrefix("10.20.0.0/16"))}, "64:ff9b::a14:405", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveAndValidate(context.Background(), tc.host, nil, tc.opts...)
			if (err == nil) != tc.allow {
				t.Errorf("%s: %v, want allowed=%v", tc.host, err, tc.allow)
			}
		})
	}
}

func TestDNS64AnswersAreCheckedAsTheIPv4TheyCarry(t *testing.T) {
	opt := WithNAT64(WellKnownNAT64)
	ip, err := ResolveAndValidate(context.Background(), "ipv4only.example", fixedResolver{"64:ff9b::a00:1", "64:ff9b::5db8:d822"}, opt)
	if err != nil || ip != "64:ff9b::5db8:d822" {
		t.Errorf("got %q %v, want the synthesized address of the public IPv4", ip, err)
	}
	if _, err := ResolveAndValidate(context.Background(), "internal.example", fixedResolver{"64:ff9b::a9fe:a9fe"}, opt); err == nil {
		t.Error("the synthesized address of the metadata service allowed")
	}
}
