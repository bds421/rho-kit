package reqsign

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

func TestVerifyWithCustomMaxAge(t *testing.T) {
	store := testStore()
	signTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	// 2 minutes later — within default 5min but outside custom 1min.
	verifyTime := signTime.Add(2 * time.Minute)

	signSigner := signing.NewSigner(signing.WithClock(fixedClock(signTime)))
	verifySigner := signing.NewSigner(signing.WithClock(fixedClock(verifyTime)))

	body := []byte(`test`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	err := VerifyRequest(req, body, store,
		WithVerifySigner(verifySigner),
		WithMaxAge(1*time.Minute),
	)
	if err == nil {
		t.Fatal("expected error for custom maxAge exceeded, got nil")
	}
}

func TestWithMaxAgeZeroFallsBackToDefault(t *testing.T) {
	store := testStore()
	signTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	// 2 minutes later — within the 5min default but would fail with 0 if taken literally.
	verifyTime := signTime.Add(2 * time.Minute)

	signSigner := signing.NewSigner(signing.WithClock(fixedClock(signTime)))
	verifySigner := signing.NewSigner(signing.WithClock(fixedClock(verifyTime)))

	body := []byte(`{"action":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// WithMaxAge(0) should be ignored, falling back to the 5min default.
	err := VerifyRequest(req, body, store,
		WithVerifySigner(verifySigner),
		WithMaxAge(0),
	)
	if err != nil {
		t.Fatalf("expected WithMaxAge(0) to fall back to default 5min, got error: %v", err)
	}
}

func TestWithMaxAgeNegativeFallsBackToDefault(t *testing.T) {
	store := testStore()
	signTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	// 2 minutes later — within the 5min default but would fail if negative were taken literally.
	verifyTime := signTime.Add(2 * time.Minute)

	signSigner := signing.NewSigner(signing.WithClock(fixedClock(signTime)))
	verifySigner := signing.NewSigner(signing.WithClock(fixedClock(verifyTime)))

	body := []byte(`{"action":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// WithMaxAge(-1s) should be ignored, falling back to the 5min default.
	err := VerifyRequest(req, body, store,
		WithVerifySigner(verifySigner),
		WithMaxAge(-1*time.Second),
	)
	if err != nil {
		t.Fatalf("expected WithMaxAge(-1s) to fall back to default 5min, got error: %v", err)
	}
}

func TestWithSignerNilUsesDefault(t *testing.T) {
	store := testStore()

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	// Sign with nil signer option — should use default signer and succeed.
	if err := SignRequest(req, body, store, WithSigner(nil)); err != nil {
		t.Fatalf("SignRequest with nil signer failed: %v", err)
	}

	if req.Header.Get(HeaderSignature) == "" {
		t.Error("expected X-Signature header to be set when using nil signer")
	}
}

func TestWithSignMaxBodySize_RejectsOversizedBody(t *testing.T) {
	store := testStore()

	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("base transport should not be called for oversized body")
		return nil, nil
	})

	// Set a small custom max body size of 100 bytes.
	transport := NewSigningTransport(base, store, WithSignMaxBodySize(100))

	oversized := make([]byte, 101)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/test", bytes.NewReader(oversized))

	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error for body exceeding custom max body size, got nil")
	}
}

func TestWithSignMaxBodySize_AcceptsFittingBody(t *testing.T) {
	store := testStore()

	var reached bool
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		reached = true
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	transport := NewSigningTransport(base, store, WithSignMaxBodySize(100))

	fitting := make([]byte, 100)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/test", bytes.NewReader(fitting))

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success for body within custom max body size, got: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !reached {
		t.Error("base transport was not reached")
	}
}

func TestWithVerifyMaxBodySize_RejectsOversizedBody(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	// Set a small custom max body size of 100 bytes.
	handler := RequireSignedRequest(store,
		WithVerifySigner(signer),
		WithVerifyMaxBodySize(100),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	oversized := make([]byte, 101)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(oversized))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestWithVerifyMaxBodySize_AcceptsFittingBody(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	var reached bool
	handler := RequireSignedRequest(store,
		WithVerifySigner(signer),
		WithVerifyMaxBodySize(100),
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	fitting := make([]byte, 100)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(fitting))
	// Sign the request so it passes verification.
	if err := SignRequest(req, fitting, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(fitting))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if !reached {
		t.Error("handler was not reached")
	}
}

func TestWithVerifySignerNilUsesDefault(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	clockSigner := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(clockSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Pass WithVerifySigner(nil) first, then the real signer — nil should be
	// ignored so the second option sets the signer correctly.
	err := VerifyRequest(req, body, store,
		WithVerifySigner(nil),
		WithVerifySigner(clockSigner),
	)
	if err != nil {
		t.Fatalf("VerifyRequest should succeed when nil signer is followed by valid signer: %v", err)
	}
}
