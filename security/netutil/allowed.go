package netutil

import (
	"net"
	"net/netip"
)

const (
	minAllowedBitsV4 = 8
	minAllowedBitsV6 = 16
)

// WithAllowedNetworks lets requests reach private or reserved addresses that
// lie inside the given networks, while every other private range stays
// blocked. It is the narrow alternative to [WithAllowPrivateIPs] for
// deployments whose legitimate targets live on internal networks, such as
// an on-premise SIEM in a known subnet.
//
// Prefixes must be valid and no wider than /8 (IPv4) or /16 (IPv6), so a
// typo cannot reopen the whole address space; unspecified addresses
// (0.0.0.0, ::) are never allowed.
func WithAllowedNetworks(prefixes ...netip.Prefix) Option {
	for _, p := range prefixes {
		minBits := minAllowedBitsV6
		if p.Addr().Is4() {
			minBits = minAllowedBitsV4
		}
		if !p.IsValid() || p.Bits() < minBits {
			panic("ssrf: WithAllowedNetworks requires valid prefixes no wider than /8 (IPv4) or /16 (IPv6)")
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
