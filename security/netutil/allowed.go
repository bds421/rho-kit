package netutil

import (
	"net"
	"net/netip"

	"github.com/bds421/rho-kit/core/v2/redact"
)

const (
	minAllowedBitsV4 = 8
	minAllowedBitsV6 = 48
)

// WithAllowedNetworks lets requests reach private or reserved addresses that
// lie inside the given networks, while every other private range stays
// blocked. It is the narrow alternative to [WithAllowPrivateIPs] for
// deployments whose legitimate targets live on internal networks, such as
// an on-premise SIEM in a known subnet.
//
// Every prefix must pass [ValidateAllowedNetwork]; WithAllowedNetworks
// panics otherwise, so validate configured networks with it first.
// Unspecified addresses (0.0.0.0, ::) are never allowed.
func WithAllowedNetworks(prefixes ...netip.Prefix) Option {
	for _, p := range prefixes {
		if err := ValidateAllowedNetwork(p); err != nil {
			panic("ssrf: WithAllowedNetworks: " + err.Error())
		}
	}
	cloned := append([]netip.Prefix(nil), prefixes...)
	return func(o *options) { o.allowedNetworks = append(o.allowedNetworks, cloned...) }
}

// ValidateAllowedNetwork reports whether p may be passed to
// [WithAllowedNetworks]: a valid prefix no wider than /8 (IPv4) or /48
// (IPv6), so a typo cannot reopen the whole address space, and not an IPv6
// range that carries IPv4 addresses (IPv4-mapped and -compatible, NAT64,
// Teredo, 6to4), which would reach arbitrary IPv4 hosts. The error says
// what is accepted and is safe to log.
func ValidateAllowedNetwork(p netip.Prefix) error {
	if !p.IsValid() {
		return redact.Safe("not a network; expected an IPv4 or IPv6 network in CIDR notation such as 10.20.0.0/16 or fd12:3456:789a::/48")
	}
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		v4 := netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked()
		return redact.Safef("%s is an IPv4 network written as IPv6; list it as %s", p, v4)
	}
	minBits := minAllowedBitsV6
	if p.Addr().Is4() {
		minBits = minAllowedBitsV4
	}
	if p.Bits() < minBits {
		return redact.Safef("%s is wider than /8 (IPv4) or /48 (IPv6)", p)
	}
	if embedsIPv4(p) {
		return redact.Safef("%s is an IPv6 range that carries IPv4 addresses (IPv4-mapped or -compatible, NAT64, Teredo or 6to4); list the IPv4 network instead", p)
	}
	return nil
}

// WithNAT64 checks an address in the NAT64 prefix as the IPv4 address it
// carries (RFC 6052), so an IPv6-only deployment reaches public IPv4 hosts
// through its gateway while private ones stay blocked. An address in the
// prefix that is no well-formed NAT64 address is refused. Without this
// option the well-known prefix 64:ff9b::/96 is refused as a whole.
func WithNAT64(prefix NAT64Prefix) Option {
	return func(o *options) { o.nat64 = prefix }
}

// refused reports whether the guard keeps ip out of reach.
func (o options) refused(ip net.IP) bool {
	if addr, ok := netip.AddrFromSlice(ip); ok && o.nat64.Contains(addr) {
		v4, ok := o.nat64.Extract(addr)
		if !ok {
			return true
		}
		ip = v4.AsSlice()
	}
	return IsPrivateIP(ip) && !o.allowed(ip)
}

func (o options) allowed(ip net.IP) bool {
	if ip.IsUnspecified() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, p := range o.allowedNetworks {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

var ipv6Loopback = netip.MustParsePrefix("::1/128")

var ipv4CarryingRanges = []netip.Prefix{
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func embedsIPv4(p netip.Prefix) bool {
	if p.Addr().Is4() || p == ipv6Loopback {
		return false
	}
	for _, r := range ipv4CarryingRanges {
		if r.Overlaps(p) {
			return true
		}
	}
	return false
}
