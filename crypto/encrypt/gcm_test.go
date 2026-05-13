package encrypt

import (
	"errors"
	"testing"
)

func TestEncryptDecryptBytes_RoundTrip(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	plaintext := []byte("hello world, this is secret data")

	sealed, err := EncryptBytes(gcm, plaintext)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}

	if string(sealed) == string(plaintext) {
		t.Fatal("sealed should differ from plaintext")
	}

	opened, err := DecryptBytes(gcm, sealed)
	if err != nil {
		t.Fatalf("DecryptBytes: %v", err)
	}

	if string(opened) != string(plaintext) {
		t.Fatalf("expected %q, got %q", plaintext, opened)
	}
}

func TestEncryptBytes_UniqueNonces(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	data := []byte("same data")
	s1, _ := EncryptBytes(gcm, data)
	s2, _ := EncryptBytes(gcm, data)

	if string(s1) == string(s2) {
		t.Fatal("two seals of the same data should differ (unique nonces)")
	}
}

func TestDecryptBytes_TooShort(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	_, err = DecryptBytes(gcm, []byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestNewGCM_InvalidKeySize(t *testing.T) {
	_, err := NewGCM([]byte("short"))
	if err == nil {
		t.Fatal("expected error for invalid key size")
	}
}

func TestEncryptDecryptBytes_EmptyPlaintext(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	sealed, err := EncryptBytes(gcm, []byte{})
	if err != nil {
		t.Fatalf("EncryptBytes empty: %v", err)
	}

	opened, err := DecryptBytes(gcm, sealed)
	if err != nil {
		t.Fatalf("DecryptBytes empty: %v", err)
	}

	if len(opened) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(opened))
	}
}

func TestEncryptDecryptBytes_NilAEADReturnsError(t *testing.T) {
	if _, err := EncryptBytes(nil, []byte("secret")); !errors.Is(err, ErrInvalidAEAD) {
		t.Fatalf("EncryptBytes nil AEAD error = %v, want ErrInvalidAEAD", err)
	}
	if _, err := DecryptBytes(nil, []byte("ciphertext")); !errors.Is(err, ErrInvalidAEAD) {
		t.Fatalf("DecryptBytes nil AEAD error = %v, want ErrInvalidAEAD", err)
	}
}
