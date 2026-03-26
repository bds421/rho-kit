package reqsign

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

func BenchmarkSignRequest(b *testing.B) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy","env":"production"}`)

	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/api/deploy?env=prod", bytes.NewReader(body))
		_ = SignRequest(req, body, store, WithSigner(signer))
	}
}

func BenchmarkVerifyRequest(b *testing.B) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy","env":"production"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy?env=prod", bytes.NewReader(body))
	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for b.Loop() {
		_ = VerifyRequest(req, body, store, WithVerifySigner(signer))
	}
}

func BenchmarkCanonicalBytes(b *testing.B) {
	body := []byte(`{"action":"deploy","env":"production","replicas":3}`)

	b.ResetTimer()
	for b.Loop() {
		_ = canonicalBytes(http.MethodPost, "/api/deploy?env=prod&region=us-east-1", body)
	}
}
