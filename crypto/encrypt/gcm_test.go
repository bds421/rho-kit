package encrypt

import (
	"testing"
)

func TestSealOpenBytes_RoundTrip(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	plaintext := []byte("hello world, this is secret data")

	sealed, err := SealBytes(gcm, plaintext)
	if err != nil {
		t.Fatalf("SealBytes: %v", err)
	}

	if string(sealed) == string(plaintext) {
		t.Fatal("sealed should differ from plaintext")
	}

	opened, err := OpenBytes(gcm, sealed)
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}

	if string(opened) != string(plaintext) {
		t.Fatalf("expected %q, got %q", plaintext, opened)
	}
}

func TestSealBytes_UniqueNonces(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	data := []byte("same data")
	s1, _ := SealBytes(gcm, data)
	s2, _ := SealBytes(gcm, data)

	if string(s1) == string(s2) {
		t.Fatal("two seals of the same data should differ (unique nonces)")
	}
}

func TestOpenBytes_TooShort(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	_, err = OpenBytes(gcm, []byte{1, 2, 3})
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

func TestSealOpenBytes_EmptyPlaintext(t *testing.T) {
	gcm, err := NewGCM(testKey(t))
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}

	sealed, err := SealBytes(gcm, []byte{})
	if err != nil {
		t.Fatalf("SealBytes empty: %v", err)
	}

	opened, err := OpenBytes(gcm, sealed)
	if err != nil {
		t.Fatalf("OpenBytes empty: %v", err)
	}

	if len(opened) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(opened))
	}
}
