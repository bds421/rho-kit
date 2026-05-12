package envelope

import (
	"bytes"
	"context"
	"testing"

	"github.com/bds421/rho-kit/crypto/v2/envelope/kekstatic"
)

var benchEnvelopeBlob []byte
var benchEnvelopePlaintext []byte

func BenchmarkEncrypt(b *testing.B) {
	enc := newBenchmarkEncryptor(b)
	plaintext := bytes.Repeat([]byte("a"), 1024)
	aad := []byte("tenant:acme:table:orders")

	b.SetBytes(int64(len(plaintext)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		blob, err := enc.Encrypt(context.Background(), plaintext, aad)
		if err != nil {
			b.Fatal(err)
		}
		benchEnvelopeBlob = blob
	}
}

func BenchmarkDecrypt(b *testing.B) {
	enc := newBenchmarkEncryptor(b)
	plaintext := bytes.Repeat([]byte("a"), 1024)
	aad := []byte("tenant:acme:table:orders")
	blob, err := enc.Encrypt(context.Background(), plaintext, aad)
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(plaintext)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pt, err := enc.Decrypt(context.Background(), blob, aad)
		if err != nil {
			b.Fatal(err)
		}
		benchEnvelopePlaintext = pt
	}
}

func BenchmarkRewrap(b *testing.B) {
	kek := newBenchmarkKEK(b)
	enc := NewEncryptor(kek)
	plaintext := bytes.Repeat([]byte("a"), 1024)
	blob, err := enc.Encrypt(context.Background(), plaintext, []byte("tenant:acme"))
	if err != nil {
		b.Fatal(err)
	}
	if err := kek.AddKey("bench-key-v2", bytes.Repeat([]byte{0x23}, 32)); err != nil {
		b.Fatal(err)
	}
	if err := kek.Rotate("bench-key-v2"); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(plaintext)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rewrapped, err := enc.Rewrap(context.Background(), blob)
		if err != nil {
			b.Fatal(err)
		}
		benchEnvelopeBlob = rewrapped
	}
}

func newBenchmarkEncryptor(b *testing.B) *Encryptor {
	b.Helper()
	return NewEncryptor(newBenchmarkKEK(b))
}

func newBenchmarkKEK(b *testing.B) *kekstatic.KEK {
	b.Helper()
	kek, err := kekstatic.NewKEK("bench-key-v1", bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		b.Fatal(err)
	}
	return kek
}
