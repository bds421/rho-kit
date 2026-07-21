package awskms

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

var _ slog.LogValuer = Config{}

func TestConfigLogValueRedactsEncryptionContextValues(t *testing.T) {
	cfg := Config{
		KeyID: "alias/prod-key",
		EncryptionContext: map[string]string{
			"tenant": "tenant-secret",
			"table":  "billing-secret",
		},
	}

	rendered := cfg.LogValue().String()

	for _, secret := range []string{"alias/prod-key", "tenant-secret", "billing-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	for _, expected := range []string{"key_id_configured=true", "tenant", "[REDACTED]"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestNewCopiesEncryptionContext(t *testing.T) {
	ctx := map[string]string{"tenant": "acme"}
	k, err := NewKEK(kms.New(kms.Options{}), Config{
		KeyID:             "alias/test",
		EncryptionContext: ctx,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx["tenant"] = "changed"
	ctx["added"] = "value"

	if got := k.context["tenant"]; got != "acme" {
		t.Fatalf("tenant context = %q, want original value", got)
	}
	if _, ok := k.context["added"]; ok {
		t.Fatal("constructor retained caller-owned context map")
	}
}

func TestKEKInvalidStateReturnsErrors(t *testing.T) {
	var nilKEK *KEK
	if got := nilKEK.KeyID(); got != "" {
		t.Fatalf("nil KeyID = %q, want empty", got)
	}
	if _, _, err := nilKEK.Wrap(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("nil Wrap expected error")
	}
	if _, err := nilKEK.Unwrap(context.Background(), "key", []byte("wrapped")); err == nil {
		t.Fatal("nil Unwrap expected error")
	}

	zero := &KEK{}
	if _, _, err := zero.Wrap(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("zero Wrap expected error")
	}
	if _, err := zero.Unwrap(context.Background(), "key", []byte("wrapped")); err == nil {
		t.Fatal("zero Unwrap expected error")
	}
}

func TestKEKRejectsNilContextAndEmptyUnwrapKey(t *testing.T) {
	k, err := NewKEK(kms.New(kms.Options{}), Config{KeyID: "alias/test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := nilContextForTest()
	if _, _, err := k.Wrap(ctx, make([]byte, 32)); err == nil {
		t.Fatal("Wrap nil context expected error")
	}
	if _, err := k.Unwrap(ctx, "key", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap nil context expected error")
	}
	if _, err := k.Unwrap(context.Background(), "", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap empty keyID expected error")
	}
}

func TestUnwrapRejectsMismatchedKeyID(t *testing.T) {
	k, err := NewKEK(kms.New(kms.Options{}), Config{KeyID: "alias/configured"})
	if err != nil {
		t.Fatalf("NewKEK: %v", err)
	}
	_, err = k.Unwrap(context.Background(), "alias/some-other-key", []byte("wrapped"))
	if err == nil {
		t.Fatal("Unwrap with mismatched keyID expected error")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Unwrap error = %v, want match-failure message", err)
	}
}

func TestDecryptKeyIDForConstrainsAliasBackedEnvelopeARN(t *testing.T) {
	envelopeKeyARN := "arn:aws:kms:us-east-1:111122223333:key/12345678-1234-1234-1234-123456789abc"

	tests := []struct {
		name       string
		configured string
		envelope   string
		want       string
		wantErr    bool
	}{
		{
			name:       "bare alias forwards envelope key ARN for rotation safety",
			configured: "alias/configured",
			envelope:   envelopeKeyARN,
			want:       envelopeKeyARN,
		},
		{
			name:       "bare alias rejects foreign alias ARN",
			configured: "alias/configured",
			envelope:   "arn:aws:kms:us-east-1:999900001111:alias/configured",
			wantErr:    true,
		},
		{
			name:       "alias ARN forwards scoped envelope key ARN for rotation safety",
			configured: "arn:aws:kms:us-east-1:111122223333:alias/configured",
			envelope:   envelopeKeyARN,
			want:       envelopeKeyARN,
		},
		{
			name:       "alias ARN rejects different account",
			configured: "arn:aws:kms:us-east-1:111122223333:alias/configured",
			envelope:   "arn:aws:kms:us-east-1:999900001111:key/12345678-1234-1234-1234-123456789abc",
			wantErr:    true,
		},
		{
			name:       "bare key ID rejects ARN without region pin context",
			configured: "12345678-1234-1234-1234-123456789abc",
			envelope:   envelopeKeyARN,
			wantErr:    true,
		},
		{
			name:       "bare key ID rejects nested suffix craft",
			configured: "12345678-1234-1234-1234-123456789abc",
			envelope:   "arn:aws:kms:us-east-1:999900001111:key/arn:aws:kms:us-east-1:111122223333:key/12345678-1234-1234-1234-123456789abc",
			wantErr:    true,
		},
		{
			name:       "non-alias key rejects unrelated key ARN",
			configured: "87654321-4321-4321-4321-cba987654321",
			envelope:   envelopeKeyARN,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := &KEK{keyID: tc.configured}
			got, err := k.decryptKeyIDFor(tc.envelope)
			if tc.wantErr {
				if err == nil {
					t.Fatal("decryptKeyIDFor expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("decryptKeyIDFor: %v", err)
			}
			if got != tc.want {
				t.Fatalf("decryptKeyIDFor = %q, want %q", got, tc.want)
			}
		})
	}
}

func nilContextForTest() context.Context { return nil }

func TestDecryptKeyIDForRejectsBareKeyIDFromDifferentRegion(t *testing.T) {
	k := &KEK{
		keyID:  "12345678-1234-1234-1234-123456789abc",
		region: "us-east-1",
	}

	got, err := k.decryptKeyIDFor("arn:aws:kms:us-east-1:111122223333:key/12345678-1234-1234-1234-123456789abc")
	if err != nil {
		t.Fatalf("decryptKeyIDFor same-region: %v", err)
	}
	wantSame := "arn:aws:kms:us-east-1:111122223333:key/12345678-1234-1234-1234-123456789abc"
	if got != wantSame {
		t.Fatalf("decryptKeyIDFor same-region = %q, want %q", got, wantSame)
	}

	if _, err := k.decryptKeyIDFor("arn:aws:kms:eu-west-1:111122223333:key/12345678-1234-1234-1234-123456789abc"); err == nil {
		t.Fatal("decryptKeyIDFor cross-region expected error")
	}
}

func TestDecryptKeyIDForBareKeyIDWithoutRegionContextRejectsARN(t *testing.T) {
	// When the KEK has no region context, fail closed on ARN-form envelopes
	// rather than forwarding an attacker-controlled ARN to Decrypt.
	k := &KEK{keyID: "12345678-1234-1234-1234-123456789abc"}
	if _, err := k.decryptKeyIDFor("arn:aws:kms:eu-west-1:111122223333:key/12345678-1234-1234-1234-123456789abc"); err == nil {
		t.Fatal("decryptKeyIDFor expected error when client region is unknown")
	}
}

func TestNewKEKCapturesClientRegion(t *testing.T) {
	c := kms.New(kms.Options{Region: "us-west-2"})
	k, err := NewKEK(c, Config{KeyID: "12345678-1234-1234-1234-123456789abc"})
	if err != nil {
		t.Fatalf("NewKEK: %v", err)
	}
	if k.region != "us-west-2" {
		t.Fatalf("region = %q, want %q", k.region, "us-west-2")
	}
}
