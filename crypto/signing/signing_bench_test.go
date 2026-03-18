package signing

import (
	"testing"
)

func BenchmarkSign(b *testing.B) {
	body := []byte(`{"title":"test","message":"hello world","status":"active"}`)
	secret := testSecret

	b.ResetTimer()
	for b.Loop() {
		_, _, _ = Sign(body, secret)
	}
}

func BenchmarkVerify(b *testing.B) {
	body := []byte(`{"title":"test","message":"hello world","status":"active"}`)
	secret := testSecret
	sig, ts, _ := Sign(body, secret)

	b.ResetTimer()
	for b.Loop() {
		_, _ = Verify(secret, body, ts, sig, DefaultSignatureMaxAge)
	}
}

func BenchmarkSignAndVerify_RoundTrip(b *testing.B) {
	body := []byte(`{"title":"test","message":"hello world","status":"active"}`)
	secret := testSecret

	b.ResetTimer()
	for b.Loop() {
		sig, ts, _ := Sign(body, secret)
		_, _ = Verify(secret, body, ts, sig, DefaultSignatureMaxAge)
	}
}
