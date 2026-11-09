package netutil

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

const (
	dialTimeout    = 10 * time.Second
	minDialTimeout = 3 * time.Second
)

// permitted resolves host and keeps every address the guard accepts, IPv6
// and IPv4 alternating (RFC 8305 section 4), starting with the family of
// the first answer, or with IPv6 when a NAT64 prefix is set. With a NAT64
// prefix each permitted IPv4 address is also offered as its NAT64 address,
// so an IPv6-only host reaches an IPv4-only name without DNS64.
func permitted(ctx context.Context, host string, resolver DNSResolver, cfg options) ([]netip.Addr, error) {
	if err := validateResolveHost(host); err != nil {
		return nil, err
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if kept := cfg.keep([]netip.Addr{literal.Unmap()}, host); len(kept) > 0 {
			return kept, nil
		}
		return nil, fmt.Errorf("ssrf: host is private/reserved")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf: dns resolution failed")
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("ssrf: dns resolution returned no addresses")
	}
	addrs := make([]netip.Addr, 0, len(resolved))
	for _, r := range resolved {
		if a, ok := netip.AddrFromSlice(r.IP); ok {
			addrs = append(addrs, a.Unmap())
		}
	}
	if kept := cfg.keep(addrs, host); len(kept) > 0 {
		return kept, nil
	}
	return nil, fmt.Errorf("ssrf: all resolved IPs are private/reserved")
}

func (o options) keep(addrs []netip.Addr, host string) []netip.Addr {
	if o.allowPrivateIPs {
		slog.Warn("ssrf: private IP filtering disabled — do not use in production", redact.String("host", host))
	}
	var v6, v4 []netip.Addr
	for _, a := range addrs {
		if !o.allowPrivateIPs && o.refused(a.AsSlice()) {
			continue
		}
		if !a.Is4() {
			v6 = append(v6, a)
			continue
		}
		v4 = append(v4, a)
		if translated, ok := o.nat64.Embed(a); ok {
			v6 = append(v6, translated)
		}
	}
	if len(addrs) > 0 && addrs[0].Is4() && !o.nat64.IsValid() {
		return interleave(v4, v6)
	}
	return interleave(v6, v4)
}

func interleave(first, second []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(first)+len(second))
	for i := 0; i < len(first) || i < len(second); i++ {
		if i < len(first) {
			out = append(out, first[i])
		}
		if i < len(second) {
			out = append(out, second[i])
		}
	}
	return out
}

// dialFirst connects to the first of addrs that answers, in their order,
// each attempt with a share of the dial timeout, so a family whose path is
// broken falls back to the other.
func dialFirst(ctx context.Context, network string, addrs []netip.Addr, port string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: max(dialTimeout/time.Duration(len(addrs)), minDialTimeout)}
	var lastErr error
	for _, a := range addrs {
		if !familyMatches(a, network) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		return nil, fmt.Errorf("ssrf: no permitted address of the %s family", network)
	}
	return nil, lastErr
}

func familyMatches(a netip.Addr, network string) bool {
	switch network {
	case "tcp4", "udp4":
		return a.Is4()
	case "tcp6", "udp6":
		return a.Is6()
	}
	return true
}
