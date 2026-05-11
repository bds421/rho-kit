package gcpkms

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	kms "cloud.google.com/go/kms/apiv1"
)

var _ slog.LogValuer = Config{}

func TestConfigLogValueRedactsAAD(t *testing.T) {
	cfg := Config{
		KeyResource:                 "projects/p/locations/l/keyRings/r/cryptoKeys/k",
		AdditionalAuthenticatedData: []byte("tenant=tenant-secret"),
	}

	rendered := cfg.LogValue().String()

	for _, secret := range []string{"projects/p/locations/l/keyRings/r/cryptoKeys/k", "tenant-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	for _, expected := range []string{"key_resource_configured=true", "aad_configured=true", "aad_bytes="} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestNewCopiesAAD(t *testing.T) {
	aad := []byte("tenant=acme")
	k, err := New(&kms.KeyManagementClient{}, Config{
		KeyResource:                 "projects/p/locations/l/keyRings/r/cryptoKeys/k",
		AdditionalAuthenticatedData: aad,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	aad[0] = 'T'

	if got := string(k.aad); got != "tenant=acme" {
		t.Fatalf("AAD = %q, want original value", got)
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
	k, err := New(&kms.KeyManagementClient{}, Config{
		KeyResource: "projects/p/locations/l/keyRings/r/cryptoKeys/k",
	})
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

func TestResponseChecksumMismatchErrorIsStable(t *testing.T) {
	err := responseChecksumMismatchError("plaintext")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("error does not wrap ErrChecksumMismatch: %v", err)
	}
	msg := err.Error()
	for _, leaked := range []string{"got=", "want=", "deadbeef", "01234567"} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("checksum mismatch error leaked checksum detail: %q", msg)
		}
	}
	if !strings.Contains(msg, "response plaintext CRC32C mismatch") {
		t.Fatalf("checksum mismatch error missing stable kind: %q", msg)
	}
}

func nilContextForTest() context.Context { return nil }
