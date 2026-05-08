package encrypt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
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
	if ciphertext[:7] != "enc:v3:" {
		t.Fatalf("expected v2 prefix, got %q", ciphertext[:7])
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

func TestFieldEncryptor_PlaintextRejectedByDefault(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	if _, err := enc.Decrypt("old-plaintext-password"); err == nil {
		t.Fatal("expected ErrPlaintextNotAllowed; got nil")
	} else if !errors.Is(err, ErrPlaintextNotAllowed) {
		t.Fatalf("expected ErrPlaintextNotAllowed, got %v", err)
	}
}

func TestFieldEncryptor_DoubleEncryptYieldsDifferentCiphertext(t *testing.T) {
	// Encrypt always produces fresh ciphertext, even when the input
	// already looks like a previous Encrypt output. The safe
	// idempotent path is EncryptIfPlain, which AEAD-verifies before
	// passing through.
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
	// The attack we're defending against: an attacker who can submit
	// values into an encrypted field crafts a string that begins with
	// the v2 prefix in hopes of being passed through verbatim.
	// EncryptIfPlain must AEAD-verify before passing through, so a
	// bogus prefix gets re-encrypted (and the original attacker value
	// never reaches storage in plaintext).
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	attackerInput := "enc:v3:" + base64.StdEncoding.EncodeToString([]byte("not-real-ciphertext"))
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

	_, err = enc.Decrypt("enc:v2:not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestFieldEncryptor_Decrypt_TooShort(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = enc.Decrypt("enc:v2:AQID")
	if err == nil {
		t.Fatal("expected error for too-short ciphertext")
	}
}

func TestFieldEncryptor_DecryptsLegacyV2Format(t *testing.T) {
	// v3 format dropped the leading "\x00" of v2 because Postgres
	// TEXT columns reject NUL bytes. The body (base64 of nonce ‖ ct ‖
	// tag) is identical between v2 and v3, so legacy v2 ciphertext
	// must continue to decrypt unchanged after the prefix swap.
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	original := "alice@example.com"
	v3Ciphertext, err := enc.Encrypt(original)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Reframe the v3 ciphertext as legacy v2 by swapping the prefix.
	// The payload bytes are unchanged — only the framing differs.
	body := v3Ciphertext[len("enc:v3:"):]
	legacyV2Ciphertext := "\x00enc:v2:" + body

	got, err := enc.Decrypt(legacyV2Ciphertext)
	if err != nil {
		t.Fatalf("decrypt legacy v2: %v", err)
	}
	if got != original {
		t.Fatalf("legacy v2 decrypt: got %q, want %q", got, original)
	}
}

func TestFieldEncryptor_DecryptRejectsNoNullV2Format(t *testing.T) {
	// A "v2 without the leading null byte" prefix is the format we
	// briefly considered but rejected because it overlaps with the
	// deleted enc:v1: namespace and the same-shape-as-legacy-v2 is
	// confusing. Decrypt must reject it so operators don't accidentally
	// roll a hand-rolled migration that mints rows in a third format.
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	v3Ciphertext, err := enc.Encrypt("payload")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	body := v3Ciphertext[len("enc:v3:"):]
	bogus := "enc:v2:" + body
	if _, err := enc.Decrypt(bogus); err == nil {
		t.Fatal("Decrypt must reject the no-NUL enc:v2: shape (only enc:v3: and \\x00enc:v2: are recognised)")
	}
}

func TestFieldEncryptor_CiphertextIsPostgresTextSafe(t *testing.T) {
	// Regression: the prior format used a leading "\x00" defence-in-depth
	// byte that broke Postgres TEXT/VARCHAR inserts ("invalid byte
	// sequence for encoding UTF8: 0x00"). The output must stay within
	// the printable-ASCII range that TEXT columns accept without any
	// per-byte escaping. Run against a variety of plaintext shapes so
	// we catch any byte that leaks through outside the base64 body.
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	inputs := []string{
		"",
		"a",
		"alice@example.com",
		"line one\nline two",
		"emoji \xf0\x9f\x94\x92 inside",
		string(make([]byte, 1024)), // 1 KiB of plaintext NULs — the encryptor input may contain NULs even when its output must not
	}
	for _, in := range inputs {
		out, err := enc.Encrypt(in)
		if err != nil {
			t.Fatalf("encrypt %q: %v", in, err)
		}
		for i := 0; i < len(out); i++ {
			b := out[i]
			if b == 0x00 {
				t.Fatalf("ciphertext[%d] = 0x00; Postgres TEXT/VARCHAR rejects null bytes", i)
			}
			// printable ASCII (space..tilde) plus base64's '+/=' is
			// the maximum range we expect — anything else suggests
			// the prefix or base64 alphabet drifted.
			if b < 0x20 || b > 0x7E {
				t.Fatalf("ciphertext[%d] = 0x%02x; expected printable ASCII for safe text-column storage", i, b)
			}
		}
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
	if len(got) < 7 || got[:7] != "enc:v3:" {
		t.Fatalf("expected v2 prefix, got %q", got)
	}
}
