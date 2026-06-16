package awskms

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/smithy-go"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// fakeKMS is a fake kmsAPI that returns canned outputs/errors so the full
// Wrap/Unwrap code path (including error wrapping) can be exercised without a
// live *kms.Client.
type fakeKMS struct {
	encryptOut *kms.EncryptOutput
	encryptErr error
	decryptOut *kms.DecryptOutput
	decryptErr error
}

func (f *fakeKMS) Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	return f.encryptOut, f.encryptErr
}

func (f *fakeKMS) Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	return f.decryptOut, f.decryptErr
}

func kekWithFake(t *testing.T, f *fakeKMS) *KEK {
	t.Helper()
	return &KEK{c: f, keyID: "alias/test", metrics: packageDefaultMetrics()}
}

// TestWrapClassifiedErrorCarriesPrefixAndClassification asserts that a
// classified KMS failure surfaced through Wrap is both wrapped with the
// "awskms:" package prefix (consistent with the azure/gcp adapters) AND
// still matches apperror classification through %w.
func TestWrapClassifiedErrorCarriesPrefixAndClassification(t *testing.T) {
	k := kekWithFake(t, &fakeKMS{encryptErr: newAPIErr("ThrottlingException", "rate exceeded")})

	_, _, err := k.Wrap(context.Background(), make([]byte, 32))
	if err == nil {
		t.Fatal("Wrap with KMS error expected non-nil error")
	}
	if !strings.HasPrefix(err.Error(), "awskms: encrypt: ") {
		t.Fatalf("Wrap error = %q, want 'awskms: encrypt: ' prefix", err.Error())
	}
	if !apperror.IsUnavailable(err) {
		t.Fatalf("Wrap error %v should remain UnavailableError through %%w", err)
	}
}

// TestWrapUnclassifiedErrorCarriesPrefixAndRawCause asserts that an
// unclassified (non-smithy) KMS failure is still wrapped with the package
// prefix and preserves the raw cause for errors.Is.
func TestWrapUnclassifiedErrorCarriesPrefixAndRawCause(t *testing.T) {
	raw := errors.New("dial tcp: connection refused")
	k := kekWithFake(t, &fakeKMS{encryptErr: raw})

	_, _, err := k.Wrap(context.Background(), make([]byte, 32))
	if err == nil {
		t.Fatal("Wrap with KMS error expected non-nil error")
	}
	if !strings.HasPrefix(err.Error(), "awskms: encrypt: ") {
		t.Fatalf("Wrap error = %q, want 'awskms: encrypt: ' prefix", err.Error())
	}
	if !errors.Is(err, raw) {
		t.Fatalf("Wrap error %v should wrap the raw cause", err)
	}
	if apperror.IsUnavailable(err) || apperror.IsPermanent(err) {
		t.Fatalf("unclassified error %v should not become an apperror", err)
	}
}

// TestUnwrapClassifiedErrorCarriesPrefixAndClassification mirrors the Wrap
// case for the Decrypt path: prefix present and apperror preserved.
func TestUnwrapClassifiedErrorCarriesPrefixAndClassification(t *testing.T) {
	k := kekWithFake(t, &fakeKMS{decryptErr: newAPIErr("KeyUnavailableException", "key not ready")})

	_, err := k.Unwrap(context.Background(), k.keyID, []byte("wrapped"))
	if err == nil {
		t.Fatal("Unwrap with KMS error expected non-nil error")
	}
	if !strings.HasPrefix(err.Error(), "awskms: decrypt: ") {
		t.Fatalf("Unwrap error = %q, want 'awskms: decrypt: ' prefix", err.Error())
	}
	if !apperror.IsPermanent(err) {
		t.Fatalf("Unwrap error %v should remain PermanentError through %%w", err)
	}
	// Raw cause must still be inspectable for operators.
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("Unwrap error must still wrap the smithy.APIError cause")
	}
	if apiErr.ErrorCode() != "KeyUnavailableException" {
		t.Fatalf("ErrorCode = %q, want KeyUnavailableException", apiErr.ErrorCode())
	}
}

// TestWrapSuccessReturnsKeyIDAndCiphertext confirms the happy path returns the
// KMS-reported KeyId and ciphertext unchanged.
func TestWrapSuccessReturnsKeyIDAndCiphertext(t *testing.T) {
	wantKeyID := "arn:aws:kms:us-east-1:111122223333:key/abc"
	wantBlob := []byte("ciphertext")
	keyID := wantKeyID
	k := kekWithFake(t, &fakeKMS{encryptOut: &kms.EncryptOutput{KeyId: &keyID, CiphertextBlob: wantBlob}})

	gotKeyID, gotBlob, err := k.Wrap(context.Background(), make([]byte, 32))
	if err != nil {
		t.Fatalf("Wrap unexpected error: %v", err)
	}
	if gotKeyID != wantKeyID {
		t.Fatalf("KeyId = %q, want %q", gotKeyID, wantKeyID)
	}
	if string(gotBlob) != string(wantBlob) {
		t.Fatalf("CiphertextBlob = %q, want %q", gotBlob, wantBlob)
	}
}

// TestWrapMissingKeyIDIsRejected guards the response-validation branch.
func TestWrapMissingKeyIDIsRejected(t *testing.T) {
	k := kekWithFake(t, &fakeKMS{encryptOut: &kms.EncryptOutput{CiphertextBlob: []byte("x")}})

	_, _, err := k.Wrap(context.Background(), make([]byte, 32))
	if err == nil {
		t.Fatal("Wrap expected error when response KeyId is nil")
	}
	if !strings.Contains(err.Error(), "missing KeyId") {
		t.Fatalf("Wrap error = %q, want 'missing KeyId'", err.Error())
	}
}

// TestUnwrapSuccessReturnsPlaintext confirms the happy path returns the
// decrypted plaintext.
func TestUnwrapSuccessReturnsPlaintext(t *testing.T) {
	want := []byte("plaintext-dek")
	k := kekWithFake(t, &fakeKMS{decryptOut: &kms.DecryptOutput{Plaintext: want}})

	got, err := k.Unwrap(context.Background(), k.keyID, []byte("wrapped"))
	if err != nil {
		t.Fatalf("Unwrap unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Plaintext = %q, want %q", got, want)
	}
}
