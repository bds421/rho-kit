package encrypt

import (
	"crypto/rand"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestFieldEncryptor_RoundTrip(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	original := "my-secret-password"
	ciphertext, err := enc.Encrypt(original)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if ciphertext == original {
		t.Fatal("ciphertext should differ from plaintext")
	}
	if ciphertext[:8] != "\x00enc:v2:" {
		t.Fatalf("expected v2 prefix, got %q", ciphertext[:8])
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != original {
		t.Fatalf("expected %q, got %q", original, decrypted)
	}
}

func TestFieldEncryptor_EmptyString(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	ciphertext, err := enc.Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if ciphertext != "" {
		t.Fatalf("expected empty, got %q", ciphertext)
	}

	decrypted, err := enc.Decrypt("")
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if decrypted != "" {
		t.Fatalf("expected empty, got %q", decrypted)
	}
}

func TestFieldEncryptor_PlaintextPassthrough(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	plaintext := "old-plaintext-password"
	decrypted, err := enc.Decrypt(plaintext)
	if err != nil {
		t.Fatalf("decrypt plaintext: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestFieldEncryptor_IdempotentEncrypt(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	original := "password123"
	first, err := enc.Encrypt(original)
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}

	second, err := enc.Encrypt(first)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}
	if second != first {
		t.Fatal("double encryption should be idempotent")
	}
}

func TestFieldEncryptor_UniqueNonces(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	c1, _ := enc.Encrypt("same-password")
	c2, _ := enc.Encrypt("same-password")
	if c1 == c2 {
		t.Fatal("two encryptions of the same plaintext should differ (unique nonces)")
	}
}

func TestNewFieldEncryptor_InvalidKeySize(t *testing.T) {
	_, err := NewFieldEncryptor([]byte("too-short"))
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestFieldEncryptor_Decrypt_InvalidBase64(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = enc.Decrypt("\x00enc:v2:not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestFieldEncryptor_Decrypt_TooShort(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = enc.Decrypt("\x00enc:v2:AQID")
	if err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestEncryptOptional_NilEncryptor(t *testing.T) {
	got, err := EncryptOptional(nil, "plaintext")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plaintext" {
		t.Fatalf("expected plaintext unchanged, got %q", got)
	}
}

func TestEncryptOptional_EmptyValue(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	got, err := EncryptOptional(enc, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestEncryptOptional_WithEncryptor(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	got, err := EncryptOptional(enc, "secret-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "secret-value" {
		t.Fatal("expected encrypted value to differ from plaintext")
	}
	if len(got) < 7 || got[:8] != "\x00enc:v2:" {
		t.Fatalf("expected v2 prefix, got %q", got)
	}
}
