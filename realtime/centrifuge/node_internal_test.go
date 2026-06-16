package centrifuge

import (
	"errors"
	"testing"

	cfg "github.com/centrifugal/centrifuge"
)

func TestDisconnectReason_Classifies(t *testing.T) {
	cases := []struct {
		name string
		d    cfg.Disconnect
		want string
	}{
		{
			name: "client-initiated close is clean",
			d:    cfg.DisconnectConnectionClosed,
			want: disconnectReasonClean,
		},
		{
			name: "server shutdown is stale",
			d:    cfg.DisconnectShutdown,
			want: disconnectReasonStale,
		},
		{
			name: "server invalid-token disconnect is stale",
			d:    cfg.DisconnectInvalidToken,
			want: disconnectReasonStale,
		},
		{
			name: "custom application disconnect code is stale",
			d:    cfg.Disconnect{Code: 4001, Reason: "kicked"},
			want: disconnectReasonStale,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := disconnectReason(tc.d)
			if got != tc.want {
				t.Fatalf("disconnectReason(%+v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestValueToString_RendersScalars(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"error", errors.New("boom"), "boom"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", 42, "42"},
		{"int64", int64(-7), "-7"},
		{"uint64", uint64(9000), "9000"},
		{"float64", 3.5, "3.5"},
		{"nil", nil, ""},
		{"fallback struct", struct{ A int }{A: 1}, "{1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := valueToString(tc.in)
			if got != tc.want {
				t.Fatalf("valueToString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
