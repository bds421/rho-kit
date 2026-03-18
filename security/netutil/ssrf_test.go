package netutil

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		private bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v4 other", "127.0.0.2", true},
		{"10.x private", "10.0.0.1", true},
		{"172.16 private", "172.16.0.1", true},
		{"172.31 private", "172.31.255.255", true},
		{"192.168 private", "192.168.1.1", true},
		{"link-local", "169.254.1.1", true},
		{"zero network", "0.0.0.1", true},
		{"CGNAT", "100.64.0.1", true},
		{"CGNAT upper", "100.127.255.255", true},
		{"reserved 240", "240.0.0.1", true},
		{"IETF protocol", "192.0.0.1", true},
		{"TEST-NET-1", "192.0.2.1", true},
		{"benchmarking", "198.18.0.1", true},
		{"benchmarking upper", "198.19.255.255", true},
		{"TEST-NET-2", "198.51.100.1", true},
		{"TEST-NET-3", "203.0.113.1", true},
		{"6to4 relay", "192.88.99.1", true},
		{"public IP", "8.8.8.8", false},
		{"public IP 2", "1.1.1.1", false},
		{"loopback v6", "::1", true},
		{"link-local v6", "fe80::1", true},
		{"multicast v6", "ff02::1", true},
		{"IPv4-mapped v6 private", "::ffff:192.168.1.1", true},
		{"IPv4-mapped v6 public", "::ffff:8.8.8.8", false},
		{"ipv6 ULA fd00::", "fd00::1", true},
		{"ipv6 ULA fc00::", "fc00::1", true},
		{"ipv6-mapped 127.0.0.1", "::ffff:127.0.0.1", true},
		{"ipv6-mapped 10.0.0.1", "::ffff:10.0.0.1", true},
		{"Teredo", "2001:0000:4136:e378:8000:63bf:3fff:fdd2", true},
		{"6to4", "2002:c0a8:0101::1", true},
		{"NAT64", "64:ff9b::192.168.1.1", true},
		{"discard-only", "100::1", true},
		{"public IPv6", "2607:f8b0:4004:800::200e", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "failed to parse IP %s", tt.ip)
			assert.Equal(t, tt.private, IsPrivateIP(ip), "IsPrivateIP(%s)", tt.ip)
		})
	}
}

func TestResolveAndValidate_Loopback(t *testing.T) {
	_, err := ResolveAndValidate(context.Background(), "localhost", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestResolveAndValidate_NonexistentDomain(t *testing.T) {
	_, err := ResolveAndValidate(context.Background(), "this-domain-does-not-exist-9999.invalid", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dns resolution failed")
}

// mockDNSResolver returns predefined IPs for testing SSRF validation.
type mockDNSResolver struct {
	ips []net.IPAddr
	err error
}

func (m *mockDNSResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return m.ips, m.err
}

func TestResolveAndValidate_WithMockResolver(t *testing.T) {
	tests := []struct {
		name      string
		ips       []net.IPAddr
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "public IP passes",
			ips:     []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
			wantErr: false,
		},
		{
			name:      "private IP rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name:      "IPv6 loopback rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("::1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name:      "IPv6 ULA rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("fd00::1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name:      "IPv4-mapped private rejected",
			ips:       []net.IPAddr{{IP: net.ParseIP("::ffff:10.0.0.1")}},
			wantErr:   true,
			errSubstr: "private/reserved",
		},
		{
			name: "mixed IPs returns first public",
			ips: []net.IPAddr{
				{IP: net.ParseIP("10.0.0.1")},
				{IP: net.ParseIP("8.8.8.8")},
			},
			wantErr: false,
		},
		{
			name:      "empty result",
			ips:       []net.IPAddr{},
			wantErr:   true,
			errSubstr: "no addresses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &mockDNSResolver{ips: tt.ips}
			ip, err := ResolveAndValidate(context.Background(), "example.com", resolver)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, ip)
			}
		})
	}
}

func TestResolveAndValidate_DNSError(t *testing.T) {
	resolver := &mockDNSResolver{err: fmt.Errorf("no such host")}
	_, err := ResolveAndValidate(context.Background(), "bad.host", resolver)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dns resolution failed")
}
