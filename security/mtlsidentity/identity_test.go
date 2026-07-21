package mtlsidentity

import (
	"errors"
	"testing"
)

func TestNormalizeSAN(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKind  SANKind
		wantValue string
		wantOK    bool
		wantErr   error
	}{
		{name: "empty", input: "  ", wantOK: false},
		{name: "dns", input: "SVC-A.Internal", wantKind: SANDNS, wantValue: "svc-a.internal", wantOK: true},
		{name: "uri", input: "spiffe://example.org/svc-a", wantKind: SANURI, wantValue: "spiffe://example.org/svc-a", wantOK: true},
		{name: "uri case normalize", input: "SPIFFE://Example.ORG/svc-A", wantKind: SANURI, wantValue: "spiffe://example.org/svc-A", wantOK: true},
		{name: "unsafe", input: "svc\nname", wantErr: ErrInvalidSAN},
		{name: "invalid dns", input: "svc_name.internal", wantErr: ErrInvalidDNSSAN},
		{name: "invalid uri", input: "spiffe://example.org/%zz?token=secret-token", wantErr: ErrInvalidURISAN},
		{name: "uri query", input: "spiffe://example.org/svc-a?token=secret-token", wantErr: ErrInvalidURISAN},
		{name: "uri userinfo", input: "spiffe://user@example.org/svc-a", wantErr: ErrInvalidURISAN},
		{name: "uri fragment", input: "spiffe://example.org/svc-a#frag", wantErr: ErrInvalidURISAN},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := NormalizeSAN(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeSAN err = %v, want %v", err, tt.wantErr)
			}
			if ok != tt.wantOK {
				t.Fatalf("NormalizeSAN ok = %v, want %v", ok, tt.wantOK)
			}
			if got.Kind != tt.wantKind || got.Value != tt.wantValue {
				t.Fatalf("NormalizeSAN = %#v, want kind=%v value=%q", got, tt.wantKind, tt.wantValue)
			}
			if err != nil {
				if msg := err.Error(); containsAny(msg, "secret-token", "token=", "%zz") {
					t.Fatalf("NormalizeSAN error leaked input: %q", msg)
				}
			}
		})
	}
}

func TestNormalizeCN(t *testing.T) {
	got, ok, err := NormalizeCN("  svc-a  ")
	if err != nil || !ok || got != "svc-a" {
		t.Fatalf("NormalizeCN = %q, %v, %v; want svc-a, true, nil", got, ok, err)
	}
	_, ok, err = NormalizeCN(" ")
	if err != nil || ok {
		t.Fatalf("NormalizeCN empty = ok %v err %v, want false nil", ok, err)
	}
	_, _, err = NormalizeCN("svc\nsecret-token")
	if !errors.Is(err, ErrInvalidCN) {
		t.Fatalf("NormalizeCN err = %v, want ErrInvalidCN", err)
	}
	if containsAny(err.Error(), "secret-token", "svc") {
		t.Fatalf("NormalizeCN error leaked input: %q", err.Error())
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && len(s) >= len(needle) {
			for i := 0; i <= len(s)-len(needle); i++ {
				if s[i:i+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
