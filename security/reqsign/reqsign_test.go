package reqsign

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	if req.Header.Get(HeaderSignature) == "" {
		t.Error("expected X-Signature header to be set")
	}
	if req.Header.Get(HeaderTimestamp) == "" {
		t.Error("expected X-Signature-Timestamp header to be set")
	}
	if req.Header.Get(HeaderKeyID) != "primary" {
		t.Errorf("X-Signature-KeyID = %q, want %q", req.Header.Get(HeaderKeyID), "primary")
	}

	// Verify with same clock.
	err := VerifyRequest(req, body, store, WithVerifySigner(signer))
	if err != nil {
		t.Fatalf("VerifyRequest failed: %v", err)
	}
}

func TestSignAndVerifyEmptyBody(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)

	if err := SignRequest(req, nil, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	err := VerifyRequest(req, nil, store, WithVerifySigner(signer))
	if err != nil {
		t.Fatalf("VerifyRequest failed for empty body: %v", err)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Verify against a different store with different key for "primary".
	otherStore := NewStaticKeyStore(map[string][]byte{
		"primary": testKey(64, 99),
	}, "primary")

	err := VerifyRequest(req, body, otherStore, WithVerifySigner(signer))
	if err == nil {
		t.Fatal("expected error for wrong key, got nil")
	}
}

func TestVerifyExpiredTimestamp(t *testing.T) {
	store := testStore()
	signTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	verifyTime := signTime.Add(10 * time.Minute)

	signSigner := signing.NewSigner(signing.WithClock(fixedClock(signTime)))
	verifySigner := signing.NewSigner(signing.WithClock(fixedClock(verifyTime)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	err := VerifyRequest(req, body, store, WithVerifySigner(verifySigner))
	if err == nil {
		t.Fatal("expected error for expired timestamp, got nil")
	}
}

func TestVerifyTamperedBody(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Verify with tampered body.
	tampered := []byte(`{"action":"destroy"}`)
	err := VerifyRequest(req, tampered, store, WithVerifySigner(signer))
	if err == nil {
		t.Fatal("expected error for tampered body, got nil")
	}
}

func TestVerifyInvalidTimestamp(t *testing.T) {
	store := testStore()

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set(HeaderSignature, "sha256=abc")
	req.Header.Set(HeaderTimestamp, "not-a-number")
	req.Header.Set(HeaderKeyID, "primary")

	err := VerifyRequest(req, nil, store)
	if err == nil {
		t.Fatal("expected error for invalid timestamp, got nil")
	}

	if got := err.Error(); !strings.Contains(got, "invalid timestamp") {
		t.Errorf("error = %q, want it to contain %q", got, "invalid timestamp")
	}
}

func TestVerifyMissingHeaders(t *testing.T) {
	store := testStore()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)

	err := VerifyRequest(req, nil, store)
	if err != ErrMissingHeaders {
		t.Errorf("expected ErrMissingHeaders, got %v", err)
	}
}

func TestVerifyUnknownKeyID(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`test`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Override key ID to unknown value.
	req.Header.Set(HeaderKeyID, "nonexistent")

	err := VerifyRequest(req, body, store, WithVerifySigner(signer))
	if err != ErrKeyNotFound {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

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
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	clockSigner := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	// Sign with nil signer option — should use default signer and succeed.
	if err := SignRequest(req, body, store, WithSigner(nil)); err != nil {
		t.Fatalf("SignRequest with nil signer failed: %v", err)
	}

	if req.Header.Get(HeaderSignature) == "" {
		t.Error("expected X-Signature header to be set when using nil signer")
	}

	// Verify with default signer (no clock override), so re-sign with clock signer for deterministic verify.
	req2 := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
	if err := SignRequest(req2, body, store, WithSigner(clockSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Verify with nil verify signer — should use default signer and succeed.
	err := VerifyRequest(req2, body, store, WithVerifySigner(nil), WithVerifySigner(clockSigner))
	if err != nil {
		t.Fatalf("VerifyRequest with nil verify signer failed: %v", err)
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

func TestTransportToMiddlewareIntegration(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	// Set up a server with RequireSignedRequest middleware.
	var handlerReached bool
	handler := RequireSignedRequest(store, WithVerifySigner(signer))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerReached = true
			w.WriteHeader(http.StatusOK)
		}),
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Create an HTTP client with SigningTransport.
	client := &http.Client{
		Transport: NewSigningTransport(nil, store, WithSigner(signer)),
	}

	body := []byte(`{"action":"integrate"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/test?env=prod", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !handlerReached {
		t.Error("handler was not reached through transport -> middleware flow")
	}
}

func TestVerifyWithRotatedKey(t *testing.T) {
	key1 := testKey(32, 10)
	key2 := testKey(48, 11)

	// Sign with old key.
	oldStore := NewStaticKeyStore(map[string][]byte{
		"v1": key1,
		"v2": key2,
	}, "v1")

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`test`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	if err := SignRequest(req, body, oldStore, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Verify with new store where v2 is current but v1 still present.
	newStore := NewStaticKeyStore(map[string][]byte{
		"v1": key1,
		"v2": key2,
	}, "v2")

	err := VerifyRequest(req, body, newStore, WithVerifySigner(signer))
	if err != nil {
		t.Fatalf("VerifyRequest should accept old key during rotation: %v", err)
	}
}
