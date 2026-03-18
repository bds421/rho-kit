package encrypt

import (
	"crypto/rand"
	"testing"
)

func benchKey(b *testing.B) []byte {
	b.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		b.Fatalf("generate key: %v", err)
	}
	return key
}

func BenchmarkEncrypt(b *testing.B) {
	enc, err := NewFieldEncryptor(benchKey(b))
	if err != nil {
		b.Fatal(err)
	}
	plaintext := "my-secret-password-for-benchmarking"

	b.ResetTimer()
	for b.Loop() {
		_, _ = enc.Encrypt(plaintext)
	}
}

func BenchmarkDecrypt(b *testing.B) {
	enc, err := NewFieldEncryptor(benchKey(b))
	if err != nil {
		b.Fatal(err)
	}
	ciphertext, err := enc.Encrypt("my-secret-password-for-benchmarking")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for b.Loop() {
		_, _ = enc.Decrypt(ciphertext)
	}
}

func BenchmarkEncryptDecrypt_RoundTrip(b *testing.B) {
	enc, err := NewFieldEncryptor(benchKey(b))
	if err != nil {
		b.Fatal(err)
	}
	plaintext := "my-secret-password-for-benchmarking"

	b.ResetTimer()
	for b.Loop() {
		ct, _ := enc.Encrypt(plaintext)
		_, _ = enc.Decrypt(ct)
	}
}
