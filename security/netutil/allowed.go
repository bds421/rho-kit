package netutil

import (
	"net"
	"net/netip"
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
// Prefixes must be valid and no wider than /8 (IPv4) or /48 (IPv6), so a
// typo cannot reopen the whole address space. IPv6 ranges that carry IPv4
// addresses (IPv4-mapped and -compatible, NAT64, Teredo, 6to4) are refused,
// because they would reach arbitrary IPv4 hosts; list the IPv4 network
// instead. Unspecified addresses (0.0.0.0, ::) are never allowed.
func WithAllowedNetworks(prefixes ...netip.Prefix) Option {
	for _, p := range prefixes {
		minBits := minAllowedBitsV6
		if p.Addr().Is4() {
			minBits = minAllowedBitsV4
		}
		if !p.IsValid() || p.Bits() < minBits {
			panic("ssrf: WithAllowedNetworks requires valid prefixes no wider than /8 (IPv4) or /48 (IPv6)")
		}
		if embedsIPv4(p) {
			panic("ssrf: WithAllowedNetworks refuses IPv6 ranges that embed IPv4 addresses; list the IPv4 network instead")
		}
	}
	cloned := append([]netip.Prefix(nil), prefixes...)
	return func(o *options) { o.allowedNetworks = append(o.allowedNetworks, cloned...) }
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

var ipv4CarryingRanges = []netip.Prefix{
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func embedsIPv4(p netip.Prefix) bool {
	if p.Addr().Is4() {
		return false
	}
	for _, r := range ipv4CarryingRanges {
		if r.Overlaps(p) {
			return true
		}
	}
	return false
}
