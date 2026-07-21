package gcpkms

import (
	"context"
	"errors"
	"hash/crc32"
	"log/slog"
	"strings"
	"testing"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/protobuf/types/known/wrapperspb"
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
	k, err := NewKEK(&kms.KeyManagementClient{}, Config{
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
	k, err := NewKEK(&kms.KeyManagementClient{}, Config{
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

func TestUnwrapRejectsMismatchedKeyID(t *testing.T) {
	parent := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	k, err := NewKEK(&kms.KeyManagementClient{}, Config{KeyResource: parent})
	if err != nil {
		t.Fatalf("NewKEK: %v", err)
	}

	cases := []struct{ name, keyID string }{
		{"different parent", "projects/p/locations/l/keyRings/r/cryptoKeys/other"},
		{"non-numeric version", parent + "/cryptoKeyVersions/abc"},
		{"empty version", parent + "/cryptoKeyVersions/"},
		{"wrong suffix", parent + "X"},
	}
	for _, tc := range cases {
		_, err := k.Unwrap(context.Background(), tc.keyID, []byte("wrapped"))
		if err == nil {
			t.Fatalf("%s: Unwrap expected error", tc.name)
		}
		if !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("%s: error = %v, want match-failure message", tc.name, err)
		}
	}
}

func TestAllowsKeyIDAcceptsVersionedSuffix(t *testing.T) {
	parent := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	if !allowsKeyID(parent, parent) {
		t.Fatal("expected exact match to pass")
	}
	if !allowsKeyID(parent, parent+"/cryptoKeyVersions/3") {
		t.Fatal("expected version-qualified suffix to pass")
	}
}

func TestUnwrapDecryptRequestTargetsParentCryptoKey(t *testing.T) {
	parent := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	k, err := NewKEK(&kms.KeyManagementClient{}, Config{
		KeyResource:                 parent,
		AdditionalAuthenticatedData: []byte("tenant=acme"),
	})
	if err != nil {
		t.Fatalf("NewKEK: %v", err)
	}

	// Wrap returns the version-qualified CryptoKeyVersion name, which is
	// stored as the envelope keyID. Decrypt's Name field, however, must be
	// the parent CryptoKey resource — GCP KMS rejects a version-qualified
	// name on (symmetric) Decrypt with INVALID_ARGUMENT and selects the
	// version from the ciphertext itself.
	keyID := parent + "/cryptoKeyVersions/3"
	wrapped := []byte("ciphertext")

	req := k.decryptRequest(keyID, wrapped)

	if got := req.GetName(); got != parent {
		t.Fatalf("DecryptRequest.Name = %q, want parent CryptoKey %q", got, parent)
	}
	if got := req.GetCiphertext(); string(got) != string(wrapped) {
		t.Fatalf("DecryptRequest.Ciphertext = %q, want %q", got, wrapped)
	}
	if req.GetCiphertextCrc32C().GetValue() != int64(crc32.Checksum(wrapped, crc32cTable)) {
		t.Fatalf("DecryptRequest.CiphertextCrc32C mismatch")
	}
	if string(req.GetAdditionalAuthenticatedData()) != "tenant=acme" {
		t.Fatalf("DecryptRequest.AdditionalAuthenticatedData = %q, want AAD", req.GetAdditionalAuthenticatedData())
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

func TestNewKEK_RejectsVersionQualifiedKeyResource(t *testing.T) {
	_, err := NewKEK(&kms.KeyManagementClient{}, Config{
		KeyResource: "projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/3",
	})
	if err == nil {
		t.Fatal("expected error for version-qualified KeyResource")
	}
	if !strings.Contains(err.Error(), "parent CryptoKey") {
		t.Fatalf("error = %v, want parent CryptoKey message", err)
	}
}

// fakeKMS exercises Wrap/Unwrap CRC paths without a live client.
type fakeKMS struct {
	encryptOut *kmspb.EncryptResponse
	encryptErr error
	decryptOut *kmspb.DecryptResponse
	decryptErr error
	lastEnc    *kmspb.EncryptRequest
	lastDec    *kmspb.DecryptRequest
}

func (f *fakeKMS) Encrypt(ctx context.Context, req *kmspb.EncryptRequest, _ ...gax.CallOption) (*kmspb.EncryptResponse, error) {
	f.lastEnc = req
	return f.encryptOut, f.encryptErr
}

func (f *fakeKMS) Decrypt(ctx context.Context, req *kmspb.DecryptRequest, _ ...gax.CallOption) (*kmspb.DecryptResponse, error) {
	f.lastDec = req
	return f.decryptOut, f.decryptErr
}

func TestWrap_CRCSuccessAndMismatch(t *testing.T) {
	key := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	versioned := key + "/cryptoKeyVersions/1"
	dek := []byte("0123456789abcdef0123456789abcdef")
	ct := []byte("ciphertext-bytes")

	t.Run("success", func(t *testing.T) {
		f := &fakeKMS{encryptOut: &kmspb.EncryptResponse{
			Name:                    versioned,
			Ciphertext:              ct,
			CiphertextCrc32C:        wrapperspb.Int64(int64(crc32.Checksum(ct, crc32cTable))),
			VerifiedPlaintextCrc32C: true,
		}}
		k := &KEK{c: f, key: key}
		gotID, gotCT, err := k.Wrap(context.Background(), dek)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		if gotID != versioned {
			t.Fatalf("keyID = %q, want %q", gotID, versioned)
		}
		if string(gotCT) != string(ct) {
			t.Fatalf("ciphertext mismatch")
		}
	})

	t.Run("unverified plaintext CRC", func(t *testing.T) {
		f := &fakeKMS{encryptOut: &kmspb.EncryptResponse{
			Name:                    versioned,
			Ciphertext:              ct,
			CiphertextCrc32C:        wrapperspb.Int64(int64(crc32.Checksum(ct, crc32cTable))),
			VerifiedPlaintextCrc32C: false,
		}}
		k := &KEK{c: f, key: key}
		if _, _, err := k.Wrap(context.Background(), dek); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("err = %v, want ErrChecksumMismatch", err)
		}
	})

	t.Run("response ciphertext CRC mismatch", func(t *testing.T) {
		f := &fakeKMS{encryptOut: &kmspb.EncryptResponse{
			Name:                    versioned,
			Ciphertext:              ct,
			CiphertextCrc32C:        wrapperspb.Int64(1), // wrong
			VerifiedPlaintextCrc32C: true,
		}}
		k := &KEK{c: f, key: key}
		if _, _, err := k.Wrap(context.Background(), dek); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("err = %v, want ErrChecksumMismatch", err)
		}
	})
}

func TestUnwrap_CRCSuccessAndMismatch(t *testing.T) {
	key := "projects/p/locations/l/keyRings/r/cryptoKeys/k"
	versioned := key + "/cryptoKeyVersions/1"
	pt := []byte("plain-dek-32-bytes-pad-pad-pad!!")
	ct := []byte("wrapped")

	t.Run("success", func(t *testing.T) {
		f := &fakeKMS{decryptOut: &kmspb.DecryptResponse{
			Plaintext:       pt,
			PlaintextCrc32C: wrapperspb.Int64(int64(crc32.Checksum(pt, crc32cTable))),
		}}
		k := &KEK{c: f, key: key}
		got, err := k.Unwrap(context.Background(), versioned, ct)
		if err != nil {
			t.Fatalf("Unwrap: %v", err)
		}
		if string(got) != string(pt) {
			t.Fatalf("plaintext mismatch")
		}
		if f.lastDec == nil || f.lastDec.Name != key {
			t.Fatalf("Decrypt Name = %v, want parent key", f.lastDec)
		}
	})

	t.Run("plaintext CRC mismatch", func(t *testing.T) {
		f := &fakeKMS{decryptOut: &kmspb.DecryptResponse{
			Plaintext:       pt,
			PlaintextCrc32C: wrapperspb.Int64(1),
		}}
		k := &KEK{c: f, key: key}
		if _, err := k.Unwrap(context.Background(), versioned, ct); !errors.Is(err, ErrChecksumMismatch) {
			t.Fatalf("err = %v, want ErrChecksumMismatch", err)
		}
	})
}
