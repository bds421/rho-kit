package encrypt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
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
	if ciphertext[:len(encryptedV3Prefix)] != encryptedV3Prefix {
		t.Fatalf("expected current prefix, got %q", ciphertext[:len(encryptedV3Prefix)])
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
	// the current prefix in hopes of being passed through verbatim.
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

func TestFieldEncryptor_EncryptIfPlainWithContext_PassesThroughAADBoundCiphertext(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	aad := []byte("users:42:email")
	first, err := enc.EncryptWithContext("hello", aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	again, err := enc.EncryptIfPlainWithContext(first, aad)
	if err != nil {
		t.Fatalf("encrypt-if-plain with matching AAD: %v", err)
	}
	if again != first {
		t.Fatal("EncryptIfPlainWithContext must pass through verifiable AAD-bound ciphertext unchanged")
	}

	wrongAAD := []byte("users:99:email")
	rewrappedAsPlaintext, err := enc.EncryptIfPlainWithContext(first, wrongAAD)
	if err != nil {
		t.Fatalf("encrypt-if-plain with wrong AAD: %v", err)
	}
	if rewrappedAsPlaintext == first {
		t.Fatal("EncryptIfPlainWithContext must not pass through ciphertext bound to different AAD")
	}
	got, err := enc.DecryptWithContext(rewrappedAsPlaintext, wrongAAD)
	if err != nil {
		t.Fatalf("decrypt re-encrypted value: %v", err)
	}
	if got != first {
		t.Fatalf("mismatched AAD candidate should be encrypted as plaintext: got %q, want %q", got, first)
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
	if strings.Contains(err.Error(), "9") {
		t.Fatalf("invalid key size error leaked supplied length: %v", err)
	}
}

func TestFieldEncryptor_Decrypt_InvalidBase64(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = enc.Decrypt(encryptedV3Prefix + "not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestFieldEncryptor_Decrypt_TooShort(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = enc.Decrypt(encryptedV3Prefix + "AQID")
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

func TestFieldEncryptor_DecryptsLegacyV1Format(t *testing.T) {
	// kit v1 wrote ciphertext with the printable-ASCII "enc:v1:"
	// prefix (no leading NUL). v2 added the NUL guard; v3 dropped
	// it because the NUL broke Postgres TEXT columns. The body
	// (12-byte IV ‖ ciphertext ‖ 16-byte tag) is identical across
	// all three framings, so v1 ciphertext under the current key
	// MUST decrypt unchanged — otherwise customers with v1-encrypted
	// fields lose access to their data on upgrade.
	//
	// Note: Encrypt never WRITES the v1 prefix. The legacy shortcut
	// where input starting with "enc:v1:" was returned unchanged
	// (the one-byte plaintext bypass) is gone — Encrypt always
	// emits fresh v3 ciphertext.
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	original := "alice@example.com"
	v3Ciphertext, err := enc.Encrypt(original)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	body := v3Ciphertext[len("enc:v3:"):]
	legacyV1Ciphertext := "enc:v1:" + body

	got, err := enc.Decrypt(legacyV1Ciphertext)
	if err != nil {
		t.Fatalf("decrypt legacy v1: %v", err)
	}
	if got != original {
		t.Fatalf("legacy v1 decrypt: got %q, want %q", got, original)
	}
}

func TestFieldEncryptor_DecryptRejectsNoNullV2Format(t *testing.T) {
	// `enc:v2:` (no NUL) was never a written format — v1 used
	// `enc:v1:`, v2 used `\x00enc:v2:`, v3 uses `enc:v3:`. Decrypt
	// must reject any made-up `enc:v2:` framing so operators don't
	// accidentally roll a hand-rolled migration that mints rows in
	// a fourth format. Only enc:v3:, \x00enc:v2:, and enc:v1: are
	// recognised.
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
		t.Fatal("Decrypt must reject the no-NUL enc:v2: shape (only enc:v3:, \\x00enc:v2:, and enc:v1: are recognised)")
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
	got, err := encryptOptional(nil, "plaintext")
	if !errors.Is(err, ErrInvalidEncryptor) {
		t.Fatalf("expected ErrInvalidEncryptor, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty output on error, got %q", got)
	}
}

func TestEncryptOptional_BindsAAD(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	aad := []byte("tenant:acme:users:42:ssn")
	got, err := encryptOptionalWithContext(enc, "secret-value", aad)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "secret-value" {
		t.Fatal("expected encrypted value to differ from plaintext")
	}
	if _, err := enc.DecryptWithContext(got, []byte("tenant:evil:users:42:ssn")); err == nil {
		t.Fatal("DecryptWithContext must reject optional ciphertext under different AAD")
	}
	plain, err := enc.DecryptWithContext(got, aad)
	if err != nil {
		t.Fatalf("decrypt with matching AAD: %v", err)
	}
	if plain != "secret-value" {
		t.Fatalf("got %q, want secret-value", plain)
	}
}

func TestEncryptOptional_EmptyValue(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	got, err := encryptOptional(enc, "")
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

	got, err := encryptOptional(enc, "secret-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "secret-value" {
		t.Fatal("expected encrypted value to differ from plaintext")
	}
	if len(got) < len(encryptedV3Prefix) || got[:len(encryptedV3Prefix)] != encryptedV3Prefix {
		t.Fatalf("expected current prefix, got %q", got)
	}
}

func TestFieldEncryptor_InvalidReceiverReturnsError(t *testing.T) {
	var nilEncryptor *FieldEncryptor
	cases := []struct {
		name string
		enc  *FieldEncryptor
	}{
		{name: "nil", enc: nilEncryptor},
		{name: "zero", enc: &FieldEncryptor{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.enc.Encrypt("secret"); !errors.Is(err, ErrInvalidEncryptor) {
				t.Fatalf("Encrypt error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := tc.enc.EncryptWithContext("secret", []byte("aad")); !errors.Is(err, ErrInvalidEncryptor) {
				t.Fatalf("EncryptWithContext error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := tc.enc.EncryptIfPlain("secret"); !errors.Is(err, ErrInvalidEncryptor) {
				t.Fatalf("EncryptIfPlain error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := tc.enc.EncryptIfPlainWithContext("secret", []byte("aad")); !errors.Is(err, ErrInvalidEncryptor) {
				t.Fatalf("EncryptIfPlainWithContext error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := tc.enc.Decrypt("enc:v3:AA=="); !errors.Is(err, ErrInvalidEncryptor) {
				t.Fatalf("Decrypt error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := tc.enc.DecryptWithContext("enc:v3:AA==", []byte("aad")); !errors.Is(err, ErrInvalidEncryptor) {
				t.Fatalf("DecryptWithContext error = %v, want ErrInvalidEncryptor", err)
			}
		})
	}
}

func TestFieldEncryptor_OpsCountIncrementsPerCall(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	if e, d := enc.OpsCount(); e != 0 || d != 0 {
		t.Fatalf("initial OpsCount = (%d, %d), want (0, 0)", e, d)
	}

	for range 5 {
		if _, err := enc.Encrypt("hello"); err != nil {
			t.Fatalf("encrypt: %v", err)
		}
	}
	ct, err := enc.Encrypt("hello")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	for range 3 {
		if _, err := enc.Decrypt(ct); err != nil {
			t.Fatalf("decrypt: %v", err)
		}
	}

	enc.encryptOps.Add(0) // ensure no false visibility issue when reading
	enc.decryptOps.Add(0)
	if e, d := enc.OpsCount(); e != 6 || d != 3 {
		t.Fatalf("OpsCount = (%d, %d), want (6, 3)", e, d)
	}
}

func TestFieldEncryptor_RegisterMetricsCallback(t *testing.T) {
	enc, err := NewFieldEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	var encrypts, decrypts int
	enc.RegisterMetrics(func(op Operation) {
		switch op {
		case OperationEncrypt:
			encrypts++
		case OperationDecrypt:
			decrypts++
		}
	})

	ct, err := enc.Encrypt("payload")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := enc.Decrypt(ct); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if encrypts != 1 || decrypts != 1 {
		t.Fatalf("encrypts=%d decrypts=%d, want both 1", encrypts, decrypts)
	}

	// Nil clears the callback.
	enc.RegisterMetrics(nil)
	if _, err := enc.Encrypt("payload"); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if encrypts != 1 {
		t.Fatalf("encrypts after nil callback = %d, want 1 (callback should not fire)", encrypts)
	}
}

func TestOperation_String(t *testing.T) {
	if got := OperationEncrypt.String(); got != "encrypt" {
		t.Fatalf("OperationEncrypt = %q, want encrypt", got)
	}
	if got := OperationDecrypt.String(); got != "decrypt" {
		t.Fatalf("OperationDecrypt = %q, want decrypt", got)
	}
	if got := Operation(99).String(); got != "unknown" {
		t.Fatalf("Operation(99) = %q, want unknown", got)
	}
}
