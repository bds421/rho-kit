package signing

import (
	"testing"
)

func BenchmarkSign(b *testing.B) {
	body := []byte(`{"title":"test","message":"hello world","status":"active"}`)
	secret := testSecret

	b.ResetTimer()
	for b.Loop() {
		_, _, _ = Sign(secret, body)
	}
}

func BenchmarkVerify(b *testing.B) {
	body := []byte(`{"title":"test","message":"hello world","status":"active"}`)
	secret := testSecret
	sig, ts, _ := Sign(secret, body)

	b.ResetTimer()
	for b.Loop() {
		_ = Verify(secret, body, ts, sig, DefaultSignatureMaxAge)
	}
}

func BenchmarkSignAndVerify_RoundTrip(b *testing.B) {
	body := []byte(`{"title":"test","message":"hello world","status":"active"}`)
	secret := testSecret

	b.ResetTimer()
	for b.Loop() {
		sig, ts, _ := Sign(secret, body)
		_ = Verify(secret, body, ts, sig, DefaultSignatureMaxAge)
	}
}
