package reqsign

import (
	"bytes"
	"errors"
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
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch, got %v", err)
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

func TestVerifyQueryParameterTampering(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy?env=prod", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Tamper with query parameter: change env=prod to env=staging.
	tampered := httptest.NewRequest(http.MethodPost, "/api/deploy?env=staging", bytes.NewReader(body))
	tampered.Header = req.Header

	err := VerifyRequest(tampered, body, store, WithVerifySigner(signer))
	if err == nil {
		t.Fatal("expected error for query parameter tampering, got nil")
	}
}

func TestVerifyHTTPMethodTampering(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	// Tamper with HTTP method: change POST to PUT.
	tampered := httptest.NewRequest(http.MethodPut, "/api/deploy", bytes.NewReader(body))
	tampered.Header = req.Header

	err := VerifyRequest(tampered, body, store, WithVerifySigner(signer))
	if err == nil {
		t.Fatal("expected error for HTTP method tampering, got nil")
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
