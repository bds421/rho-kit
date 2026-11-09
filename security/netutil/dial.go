package netutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

const (
	// dialTimeout bounds one dial over every address it tries.
	dialTimeout = 10 * time.Second
	// maxDialAttempts caps the addresses one dial tries, so a name with
	// many addresses that do not answer cannot hold it longer.
	maxDialAttempts = 6
)

var (
	// ErrBlocked is the guard refusing a target: a malformed one, one whose
	// every address is private or reserved, or one with no address of the
	// dialed family.
	ErrBlocked = errors.New("ssrf: blocked")
	// ErrUnresolved is a target name that resolved to no address, which may
	// pass.
	ErrUnresolved = errors.New("ssrf: dns resolution failed")
)

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// permitted resolves host and keeps every address the guard accepts, IPv6
// and IPv4 alternating (RFC 8305 section 4), starting with the family of
// the first answer. With [WithSynthesizedNAT64] each permitted IPv4 address
// is also offered as its NAT64 address, IPv6 first, so an IPv6-only host
// reaches an IPv4-only name without DNS64.
func permitted(ctx context.Context, host string, resolver DNSResolver, cfg options) ([]netip.Addr, error) {
	if err := validateResolveHost(host); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlocked, err)
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if kept := cfg.keep([]netip.Addr{literal.Unmap()}, host); len(kept) > 0 {
			return kept, nil
		}
		return nil, fmt.Errorf("%w: private/reserved address", ErrBlocked)
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	resolved, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, ErrUnresolved
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("%w: no addresses", ErrUnresolved)
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
	return nil, fmt.Errorf("%w: every resolved address is private/reserved", ErrBlocked)
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
		if translated, ok := o.nat64.Embed(a); ok && o.synthesizeNAT64 {
			v6 = append(v6, translated)
		}
	}
	synthesized := o.synthesizeNAT64 && o.nat64.IsValid()
	if len(addrs) > 0 && addrs[0].Is4() && !synthesized {
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

// dialFirst connects to the first of addrs that answers, in their order, so
// a family whose path is broken falls back to the other. It tries at most
// maxDialAttempts addresses of the network's family within one budget,
// each attempt with an equal share of it.
func dialFirst(ctx context.Context, network string, addrs []netip.Addr, port string) (net.Conn, error) {
	return dialWithin(ctx, network, addrs, port, dialTimeout, (&net.Dialer{}).DialContext)
}

func dialWithin(ctx context.Context, network string, addrs []netip.Addr, port string, budget time.Duration, dial dialFunc) (net.Conn, error) {
	var tried []netip.Addr
	for _, a := range addrs {
		if familyMatches(a, network) && len(tried) < maxDialAttempts {
			tried = append(tried, a)
		}
	}
	if len(tried) == 0 {
		return nil, fmt.Errorf("%w: no permitted address of the %s family", ErrBlocked, network)
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	share := budget / time.Duration(len(tried))
	var lastErr error
	for _, a := range tried {
		attempt, cancelAttempt := context.WithTimeout(ctx, share)
		conn, err := dial(attempt, network, net.JoinHostPort(a.String(), port))
		cancelAttempt()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
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

// SSRFSafeDialer dials a host:port through the guard on every call: it
// resolves the host, keeps the permitted addresses and connects to the
// first that answers (see [SSRFSafeDynamicTransport]), so DNS cannot be
// rebound to an internal address between the check and the connection. A
// refused target fails with [ErrBlocked], an unresolved one with
// [ErrUnresolved]; a failed connection returns the dial's own error.
func SSRFSafeDialer(resolver DNSResolver, opts ...Option) func(ctx context.Context, network, addr string) (net.Conn, error) {
	cfg := collectOptions(opts)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid dial address", ErrBlocked)
		}
		addrs, err := permitted(ctx, host, resolver, cfg)
		if err != nil {
			return nil, fmt.Errorf("ssrf: resolve target: %w", err)
		}
		return dialFirst(ctx, network, addrs, port)
	}
}
