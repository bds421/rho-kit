package reqsign

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
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
	err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
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

	err := VerifyRequest(req, nil, store, WithVerifySigner(signer), freshNonceStoreOpt())
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
	otherStore := signing.NewStaticKeyStore(map[string][]byte{
		"primary": testKey(64, 99),
	}, "primary")

	err := VerifyRequest(req, body, otherStore, WithVerifySigner(signer), freshNonceStoreOpt())
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

	err := VerifyRequest(req, body, store, WithVerifySigner(verifySigner), freshNonceStoreOpt())
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
	err := VerifyRequest(req, tampered, store, WithVerifySigner(signer), freshNonceStoreOpt())
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
	req.Header.Set(HeaderNonce, "abc-nonce")

	err := VerifyRequest(req, nil, store, freshNonceStoreOpt())
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

	err := VerifyRequest(req, nil, store, freshNonceStoreOpt())
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

	err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
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

	err := VerifyRequest(tampered, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
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

	err := VerifyRequest(tampered, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
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
	handler := RequireSignedRequest(store, WithVerifySigner(signer), freshNonceStoreOpt())(
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
	oldStore := signing.NewStaticKeyStore(map[string][]byte{
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
	newStore := signing.NewStaticKeyStore(map[string][]byte{
		"v1": key1,
		"v2": key2,
	}, "v2")

	err := VerifyRequest(req, body, newStore, WithVerifySigner(signer), freshNonceStoreOpt())
	if err != nil {
		t.Fatalf("VerifyRequest should accept old key during rotation: %v", err)
	}
}

// FR-025 [HIGH] regression: a captured signed request was previously
// replayable for the entire MaxAge window. The nonce store now records
// every accepted nonce within MaxAge so a second presentation of the
// same wire bytes is rejected with ErrReplay.
func TestVerifyRequest_RejectsReplay(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"transfer":"1000USD"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/transfer", bytes.NewReader(body))
	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}

	// Use a SHARED nonce store across both verifications to model the
	// real deployment: the verifier persists nonces across requests.
	nonceOpt := freshNonceStoreOpt()
	if err := VerifyRequest(req, body, store, WithVerifySigner(signer), nonceOpt); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	// Second presentation of the IDENTICAL wire bytes is the replay.
	err := VerifyRequest(req, body, store, WithVerifySigner(signer), nonceOpt)
	if err == nil {
		t.Fatal("expected ErrReplay on second verification of same nonce, got nil")
	}
	if !errors.Is(err, ErrReplay) {
		t.Errorf("expected ErrReplay, got %v", err)
	}
}

// Companion: a missing nonce header is rejected explicitly so
// pre-FR-025 callers cannot bypass replay protection by simply
// omitting the new header.
func TestVerifyRequest_RejectsMissingNonce(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"x":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/x", bytes.NewReader(body))
	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}
	// Strip the nonce header — exact pre-fix behaviour from a legacy
	// signer that has no nonce concept.
	req.Header.Del(HeaderNonce)

	err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if !errors.Is(err, ErrNonceMissing) {
		t.Errorf("expected ErrNonceMissing, got %v", err)
	}
}

// Companion: oversized nonce headers are rejected so an attacker
// cannot inflate nonce-store keys to pathological lengths.
func TestVerifyRequest_RejectsOversizedNonce(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/x", bytes.NewReader(body))
	req.Header.Set(HeaderSignature, "sha256=abc")
	req.Header.Set(HeaderTimestamp, "1718452800")
	req.Header.Set(HeaderKeyID, "primary")
	req.Header.Set(HeaderNonce, strings.Repeat("a", nonceMaxLen+1))

	err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if !errors.Is(err, ErrNonceTooLong) {
		t.Errorf("expected ErrNonceTooLong, got %v", err)
	}
}

// Companion: RequireSignedRequest must panic at construction without
// a NonceStore — fail-loud at startup, not on first request.
func TestRequireSignedRequest_PanicsWithoutNonceStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when NonceStore not wired")
		}
	}()
	RequireSignedRequest(testStore())
}
