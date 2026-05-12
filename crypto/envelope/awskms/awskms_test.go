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

func nilContextForTest() context.Context { return nil }
