package gcpkms

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// fakeKMSClient is a narrow stand-in for *kms.KeyManagementClient that
// records the requests it receives and returns scriptable responses, so
// Wrap/Unwrap request construction and CRC32C verification are exercised
// without a live KMS backend.
type fakeKMSClient struct {
	// recorded requests
	encReq *kmspb.EncryptRequest
	decReq *kmspb.DecryptRequest

	// scripted errors
	encErr error
	decErr error

	// response mutators: applied after the adapter-correct response is
	// built so a test can corrupt exactly one field.
	mutateEnc func(*kmspb.EncryptResponse)
	mutateDec func(*kmspb.DecryptResponse)

	// versionName is the resource name Encrypt echoes back.
	versionName string
}

func (f *fakeKMSClient) Encrypt(_ context.Context, req *kmspb.EncryptRequest, _ ...gax.CallOption) (*kmspb.EncryptResponse, error) {
	f.encReq = req
	if f.encErr != nil {
		return nil, f.encErr
	}
	// A correct backend echoes verification flags and returns a ciphertext
	// with a matching CRC32C. Use a deterministic transform so the round-trip
	// can recover the plaintext on Decrypt.
	ciphertext := append([]byte("ct:"), req.GetPlaintext()...)
	resp := &kmspb.EncryptResponse{
		Name:                    f.versionName,
		Ciphertext:              ciphertext,
		CiphertextCrc32C:        wrapperspb.Int64(int64(crc32.Checksum(ciphertext, crc32cTable))),
		VerifiedPlaintextCrc32C: true,
		// Mirror the contract: only report AAD verification when AAD was sent.
		VerifiedAdditionalAuthenticatedDataCrc32C: len(req.GetAdditionalAuthenticatedData()) > 0,
	}
	if f.mutateEnc != nil {
		f.mutateEnc(resp)
	}
	return resp, nil
}

func (f *fakeKMSClient) Decrypt(_ context.Context, req *kmspb.DecryptRequest, _ ...gax.CallOption) (*kmspb.DecryptResponse, error) {
	f.decReq = req
	if f.decErr != nil {
		return nil, f.decErr
	}
	plaintext := bytes.TrimPrefix(req.GetCiphertext(), []byte("ct:"))
	resp := &kmspb.DecryptResponse{
		Plaintext:       plaintext,
		PlaintextCrc32C: wrapperspb.Int64(int64(crc32.Checksum(plaintext, crc32cTable))),
	}
	if f.mutateDec != nil {
		f.mutateDec(resp)
	}
	return resp, nil
}

const testParent = "projects/p/locations/l/keyRings/r/cryptoKeys/k"

func newFakeKEK(t *testing.T, fake *fakeKMSClient, cfg Config) *KEK {
	t.Helper()
	k, err := newKEK(fake, cfg)
	if err != nil {
		t.Fatalf("newKEK: %v", err)
	}
	return k
}

func TestWrapBuildsEncryptRequestAndReturnsVersionName(t *testing.T) {
	versioned := testParent + "/cryptoKeyVersions/7"
	fake := &fakeKMSClient{versionName: versioned}
	k := newFakeKEK(t, fake, Config{
		KeyResource:                 testParent,
		AdditionalAuthenticatedData: []byte("tenant=acme"),
	})

	dek := []byte("0123456789abcdef0123456789abcdef")
	keyID, wrapped, err := k.Wrap(context.Background(), dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Wrap returns the version-qualified resource name verbatim.
	if keyID != versioned {
		t.Fatalf("Wrap keyID = %q, want %q", keyID, versioned)
	}
	if !bytes.Equal(wrapped, append([]byte("ct:"), dek...)) {
		t.Fatalf("Wrap ciphertext = %q, want the backend ciphertext", wrapped)
	}

	// Request fields: Name is the parent (unversioned) resource, payload and
	// CRC32C wrappers are populated for both plaintext and AAD.
	req := fake.encReq
	if got := req.GetName(); got != testParent {
		t.Fatalf("EncryptRequest.Name = %q, want parent %q", got, testParent)
	}
	if !bytes.Equal(req.GetPlaintext(), dek) {
		t.Fatalf("EncryptRequest.Plaintext = %q, want DEK", req.GetPlaintext())
	}
	if got, want := req.GetPlaintextCrc32C().GetValue(), int64(crc32.Checksum(dek, crc32cTable)); got != want {
		t.Fatalf("EncryptRequest.PlaintextCrc32C = %d, want %d", got, want)
	}
	if string(req.GetAdditionalAuthenticatedData()) != "tenant=acme" {
		t.Fatalf("EncryptRequest.AAD = %q, want tenant=acme", req.GetAdditionalAuthenticatedData())
	}
	if got, want := req.GetAdditionalAuthenticatedDataCrc32C().GetValue(), int64(crc32.Checksum([]byte("tenant=acme"), crc32cTable)); got != want {
		t.Fatalf("EncryptRequest.AADCrc32C = %d, want %d", got, want)
	}
}

func TestWrapUnwrapRoundTrip(t *testing.T) {
	versioned := testParent + "/cryptoKeyVersions/3"
	fake := &fakeKMSClient{versionName: versioned}
	k := newFakeKEK(t, fake, Config{
		KeyResource:                 testParent,
		AdditionalAuthenticatedData: []byte("tenant=acme"),
	})

	dek := []byte("0123456789abcdef0123456789abcdef")
	keyID, wrapped, err := k.Wrap(context.Background(), dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	got, err := k.Unwrap(context.Background(), keyID, wrapped)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("Unwrap = %q, want original DEK", got)
	}

	// Per the documented contract, symmetric Decrypt targets the parent
	// CryptoKey resource (not the version-qualified keyID, which GCP KMS
	// rejects with INVALID_ARGUMENT); the version is selected from the
	// ciphertext metadata. The keyID is retained purely as the per-blob
	// authorization gate. Decrypt forwards the ciphertext + AAD with their
	// CRC32C wrappers.
	req := fake.decReq
	if req.GetName() != testParent {
		t.Fatalf("DecryptRequest.Name = %q, want parent CryptoKey %q", req.GetName(), testParent)
	}
	if !bytes.Equal(req.GetCiphertext(), wrapped) {
		t.Fatalf("DecryptRequest.Ciphertext = %q, want wrapped DEK", req.GetCiphertext())
	}
	if got, want := req.GetCiphertextCrc32C().GetValue(), int64(crc32.Checksum(wrapped, crc32cTable)); got != want {
		t.Fatalf("DecryptRequest.CiphertextCrc32C = %d, want %d", got, want)
	}
	if string(req.GetAdditionalAuthenticatedData()) != "tenant=acme" {
		t.Fatalf("DecryptRequest.AAD = %q, want tenant=acme", req.GetAdditionalAuthenticatedData())
	}
}

func TestWrapNoAADOmitsCrc32CWrappers(t *testing.T) {
	fake := &fakeKMSClient{versionName: testParent + "/cryptoKeyVersions/1"}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent}) // no AAD

	if _, _, err := k.Wrap(context.Background(), []byte("dek")); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	// crc32c() returns nil for empty input so KMS treats the field as unset;
	// the empty-AAD wrap must still succeed even though the backend reports
	// VerifiedAdditionalAuthenticatedDataCrc32C=false (the len(k.aad)>0 gate).
	if fake.encReq.GetAdditionalAuthenticatedDataCrc32C() != nil {
		t.Fatal("EncryptRequest.AADCrc32C should be nil when no AAD is configured")
	}
	if fake.encReq.GetPlaintextCrc32C() == nil {
		t.Fatal("EncryptRequest.PlaintextCrc32C should be set for a non-empty DEK")
	}
}

func TestWrapRejectsMissingResponseName(t *testing.T) {
	fake := &fakeKMSClient{
		versionName: testParent + "/cryptoKeyVersions/1",
		mutateEnc:   func(r *kmspb.EncryptResponse) { r.Name = "" },
	}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent})

	if _, _, err := k.Wrap(context.Background(), []byte("dek")); err == nil {
		t.Fatal("Wrap with missing response Name expected error")
	}
}

func TestWrapRejectsUnverifiedPlaintextChecksum(t *testing.T) {
	fake := &fakeKMSClient{
		versionName: testParent + "/cryptoKeyVersions/1",
		mutateEnc:   func(r *kmspb.EncryptResponse) { r.VerifiedPlaintextCrc32C = false },
	}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent})

	_, _, err := k.Wrap(context.Background(), []byte("dek"))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Wrap error = %v, want ErrChecksumMismatch", err)
	}
}

func TestWrapRejectsUnverifiedAADChecksumOnlyWhenAADPresent(t *testing.T) {
	// With AAD present, a backend that does not verify AAD CRC32C must fail.
	withAAD := &fakeKMSClient{
		versionName: testParent + "/cryptoKeyVersions/1",
		mutateEnc:   func(r *kmspb.EncryptResponse) { r.VerifiedAdditionalAuthenticatedDataCrc32C = false },
	}
	k := newFakeKEK(t, withAAD, Config{KeyResource: testParent, AdditionalAuthenticatedData: []byte("aad")})
	if _, _, err := k.Wrap(context.Background(), []byte("dek")); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Wrap with AAD and unverified AAD CRC error = %v, want ErrChecksumMismatch", err)
	}

	// Without AAD, the same unverified flag must NOT fail the wrap (the
	// len(k.aad)>0 gate). The fake already reports false when no AAD is sent.
	noAAD := &fakeKMSClient{versionName: testParent + "/cryptoKeyVersions/1"}
	k2 := newFakeKEK(t, noAAD, Config{KeyResource: testParent})
	if _, _, err := k2.Wrap(context.Background(), []byte("dek")); err != nil {
		t.Fatalf("Wrap without AAD should ignore AAD verification flag: %v", err)
	}
}

func TestWrapRejectsCorruptedCiphertextChecksum(t *testing.T) {
	fake := &fakeKMSClient{
		versionName: testParent + "/cryptoKeyVersions/1",
		mutateEnc: func(r *kmspb.EncryptResponse) {
			// Leave CiphertextCrc32C pointing at the original value but flip a
			// byte in the ciphertext so the adapter's recompute disagrees.
			r.Ciphertext = append([]byte(nil), r.Ciphertext...)
			r.Ciphertext[0] ^= 0xFF
		},
	}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent})

	_, _, err := k.Wrap(context.Background(), []byte("dek"))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Wrap error = %v, want ErrChecksumMismatch", err)
	}
}

func TestUnwrapRejectsCorruptedPlaintextChecksum(t *testing.T) {
	versioned := testParent + "/cryptoKeyVersions/2"
	fake := &fakeKMSClient{
		versionName: versioned,
		mutateDec: func(r *kmspb.DecryptResponse) {
			r.Plaintext = append([]byte(nil), r.Plaintext...)
			r.Plaintext[0] ^= 0xFF
		},
	}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent})

	keyID, wrapped, err := k.Wrap(context.Background(), []byte("dek"))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	_, err = k.Unwrap(context.Background(), keyID, wrapped)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Unwrap error = %v, want ErrChecksumMismatch", err)
	}
}

func TestWrapClassifiesProviderError(t *testing.T) {
	fake := &fakeKMSClient{encErr: status.Error(codes.Unavailable, "backend down")}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent})

	_, _, err := k.Wrap(context.Background(), []byte("dek"))
	if err == nil {
		t.Fatal("Wrap expected error")
	}
	if !apperror.IsUnavailable(err) {
		t.Fatalf("Wrap error = %v, want apperror.IsUnavailable", err)
	}
}

func TestUnwrapClassifiesProviderError(t *testing.T) {
	versioned := testParent + "/cryptoKeyVersions/1"
	fake := &fakeKMSClient{
		versionName: versioned,
		decErr:      status.Error(codes.PermissionDenied, "no access"),
	}
	k := newFakeKEK(t, fake, Config{KeyResource: testParent})

	_, err := k.Unwrap(context.Background(), versioned, []byte("ct:dek"))
	if err == nil {
		t.Fatal("Unwrap expected error")
	}
	if !apperror.IsPermanent(err) {
		t.Fatalf("Unwrap error = %v, want apperror.IsPermanent", err)
	}
}
