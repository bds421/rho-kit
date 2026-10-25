package netutil

import (
	"net/netip"
	"strings"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// NAT64Prefix is the IPv6 prefix through which a NAT64 gateway reaches IPv4
// hosts, with the IPv4 address embedded as RFC 6052 section 2.2 lays out
// for each prefix length: /32, /40, /48, /56, /64 or /96. Bits 64 to 71
// (the "u" octet) are always zero. The zero value is no prefix.
type NAT64Prefix struct {
	prefix netip.Prefix
}

// WellKnownNAT64 is 64:ff9b::/96 (RFC 6052 section 2.1), the prefix most
// NAT64 gateways and DNS64 resolvers use.
var WellKnownNAT64 = NAT64Prefix{prefix: netip.MustParsePrefix("64:ff9b::/96")}

// ipv4Octets are the byte positions of the embedded IPv4 address for each
// RFC 6052 prefix length; byte 8 is the u octet.
var ipv4Octets = map[int][4]int{
	32: {4, 5, 6, 7},
	40: {5, 6, 7, 9},
	48: {6, 7, 9, 10},
	56: {7, 9, 10, 11},
	64: {9, 10, 11, 12},
	96: {12, 13, 14, 15},
}

const uOctet = 8

// ParseNAT64Prefix reads a NAT64 prefix such as 64:ff9b::/96 or
// 2001:db8:64::/48; empty means none. The error says what is accepted and
// is safe to log.
func ParseNAT64Prefix(raw string) (NAT64Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return NAT64Prefix{}, nil
	}
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		return NAT64Prefix{}, redact.Safef("%q is not an IPv6 prefix; a NAT64 prefix is IPv6 with length /32, /40, /48, /56, /64 or /96, such as 64:ff9b::/96", raw)
	}
	return NewNAT64Prefix(p)
}

// NewNAT64Prefix checks p against RFC 6052: IPv6, not IPv4-mapped, one of
// the six lengths, and a zero u octet when the prefix covers it (/96).
func NewNAT64Prefix(p netip.Prefix) (NAT64Prefix, error) {
	if !p.IsValid() || !p.Addr().Is6() || p.Addr().Is4In6() {
		return NAT64Prefix{}, redact.Safef("%s is not an IPv6 prefix; a NAT64 prefix is IPv6 with length /32, /40, /48, /56, /64 or /96, such as 64:ff9b::/96", p)
	}
	if _, ok := ipv4Octets[p.Bits()]; !ok {
		return NAT64Prefix{}, redact.Safef("%s has length /%d; a NAT64 prefix has length /32, /40, /48, /56, /64 or /96 (RFC 6052)", p, p.Bits())
	}
	p = p.Masked()
	if p.Addr().As16()[uOctet] != 0 {
		return NAT64Prefix{}, redact.Safef("%s sets bits 64 to 71, which RFC 6052 reserves as zero", p)
	}
	return NAT64Prefix{prefix: p}, nil
}

// IsValid reports whether n is a prefix rather than the zero value.
func (n NAT64Prefix) IsValid() bool { return n.prefix.IsValid() }

// Prefix is the IPv6 prefix itself.
func (n NAT64Prefix) Prefix() netip.Prefix { return n.prefix }

func (n NAT64Prefix) String() string { return n.prefix.String() }

// Contains reports whether a lies in the prefix, whether or not it is a
// well-formed NAT64 address.
func (n NAT64Prefix) Contains(a netip.Addr) bool {
	return n.IsValid() && a.Is6() && !a.Is4In6() && n.prefix.Contains(a)
}

// Extract answers the IPv4 address a NAT64 address stands for. It is false
// for an address outside the prefix and for one with a non-zero u octet,
// which no gateway synthesizes.
func (n NAT64Prefix) Extract(a netip.Addr) (netip.Addr, bool) {
	if !n.Contains(a) {
		return netip.Addr{}, false
	}
	b := a.As16()
	if b[uOctet] != 0 {
		return netip.Addr{}, false
	}
	pos := ipv4Octets[n.prefix.Bits()]
	return netip.AddrFrom4([4]byte{b[pos[0]], b[pos[1]], b[pos[2]], b[pos[3]]}), true
}

// Embed synthesizes the NAT64 address of an IPv4 address, with a zero
// suffix. It is false without a prefix or for an address that is not IPv4.
func (n NAT64Prefix) Embed(v4 netip.Addr) (netip.Addr, bool) {
	v4 = v4.Unmap()
	if !n.IsValid() || !v4.Is4() {
		return netip.Addr{}, false
	}
	b := n.prefix.Addr().As16()
	src := v4.As4()
	for i, p := range ipv4Octets[n.prefix.Bits()] {
		b[p] = src[i]
	}
	return netip.AddrFrom16(b), true
}
