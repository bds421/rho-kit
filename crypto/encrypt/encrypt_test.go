package encrypt

import (
	"crypto/rand"
	"encoding/base64"
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

func TestFieldEncryptor_DoubleEncryptYieldsDifferentCiphertext(t *testing.T) {
	// Renamed + flipped: the previous "idempotent re-encrypt" behaviour was a
	// security footgun (any user value starting with "enc:v1:" or
	// "\x00enc:v2:" was stored verbatim). Encrypt now always encrypts; the
	// safe idempotent path is EncryptIfPlain, which AEAD-verifies.
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	first, err := enc.Encrypt("password123")
	if err != nil {
		t.Fatalf("first encrypt: %v", err)
	}
	second, err := enc.Encrypt(first)
	if err != nil {
		t.Fatalf("second encrypt: %v", err)
	}
	if second == first {
		t.Fatal("Encrypt must always produce fresh ciphertext (no prefix shortcut); use EncryptIfPlain for idempotent re-encrypt")
	}
}

func TestFieldEncryptor_EncryptIfPlain_PassesThroughValidCiphertext(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	first, err := enc.Encrypt("hello")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	again, err := enc.EncryptIfPlain(first)
	if err != nil {
		t.Fatalf("encrypt-if-plain: %v", err)
	}
	if again != first {
		t.Fatal("EncryptIfPlain must pass through verifiable ciphertext unchanged")
	}
}

func TestFieldEncryptor_EncryptIfPlain_RejectsAttackerControlledPrefix(t *testing.T) {
	// The attack we're defending against: previously, an input like
	// "enc:v1:not-real" was treated as already-encrypted and stored verbatim.
	// EncryptIfPlain must AEAD-verify before passing through, so a bogus
	// prefix gets re-encrypted (and the original attacker value never reaches
	// storage in plaintext).
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	attackerInput := "enc:v1:" + base64.StdEncoding.EncodeToString([]byte("not-real-ciphertext"))
	out, err := enc.EncryptIfPlain(attackerInput)
	if err != nil {
		t.Fatalf("encrypt-if-plain: %v", err)
	}
	if out == attackerInput {
		t.Fatal("EncryptIfPlain must NOT pass through forged-prefix inputs that fail AEAD verification")
	}
	// Round-trips: decrypting the output gives back the original input.
	got, err := enc.Decrypt(out)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != attackerInput {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, attackerInput)
	}
}

func TestFieldEncryptor_EncryptWithContext_AADBindsCiphertextToRow(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	rowAAAD := []byte("users:42:email")
	rowBAAD := []byte("users:99:email")

	cipherForRowA, err := enc.EncryptWithContext("alice@example.com", rowAAAD)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Same AAD round-trips.
	got, err := enc.DecryptWithContext(cipherForRowA, rowAAAD)
	if err != nil {
		t.Fatalf("decrypt with matching AAD: %v", err)
	}
	if got != "alice@example.com" {
		t.Fatalf("round-trip: got %q, want %q", got, "alice@example.com")
	}

	// Swapping the ciphertext into a different row (different AAD) MUST fail.
	if _, err := enc.DecryptWithContext(cipherForRowA, rowBAAD); err == nil {
		t.Fatal("DecryptWithContext must reject ciphertext copied across rows (AAD mismatch)")
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
