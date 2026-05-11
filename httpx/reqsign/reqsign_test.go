package reqsign

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

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

func TestSignRequestReturnsNonceGenerationError(t *testing.T) {
	prev := nonceRandReader
	nonceRandReader = failingReader{}
	t.Cleanup(func() { nonceRandReader = prev })

	req := httptest.NewRequest(http.MethodPost, "/api/deploy", nil)
	err := SignRequest(req, nil, testStore())
	if err == nil {
		t.Fatal("expected nonce generation error")
	}
	if !strings.Contains(err.Error(), "generate nonce") {
		t.Fatalf("error = %v, want generate nonce context", err)
	}
	if got := req.Header.Get(HeaderNonce); got != "" {
		t.Fatalf("nonce header = %q, want empty on signing failure", got)
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
	req.Header.Set(HeaderTimestamp, "secret-token")
	req.Header.Set(HeaderKeyID, "primary")
	req.Header.Set(HeaderNonce, testNonce("invalid-timestamp"))

	err := VerifyRequest(req, nil, store, freshNonceStoreOpt())
	if err == nil {
		t.Fatal("expected error for invalid timestamp, got nil")
	}
	if !errors.Is(err, ErrTimestampInvalid) {
		t.Fatalf("expected ErrTimestampInvalid, got %v", err)
	}
	if got := err.Error(); strings.Contains(got, "secret-token") {
		t.Errorf("timestamp parse error leaked header value: %q", got)
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

func TestVerifyRequest_RejectsDuplicateSignatureHeaders(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))
	body := []byte(`{"action":"deploy"}`)

	for _, header := range []string{HeaderSignature, HeaderTimestamp, HeaderKeyID, HeaderNonce} {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
			if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
				t.Fatalf("SignRequest failed: %v", err)
			}
			req.Header.Add(header, req.Header.Get(header))

			err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
			if !errors.Is(err, ErrInvalidHeaders) {
				t.Fatalf("expected ErrInvalidHeaders for duplicate %s, got %v", header, err)
			}
		})
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

func TestVerifyHostTampering(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "https://service-a.example.com/api/deploy", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	tampered := httptest.NewRequest(http.MethodPost, "https://service-b.example.com/api/deploy", bytes.NewReader(body))
	tampered.Header = req.Header

	err := VerifyRequest(tampered, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if err == nil {
		t.Fatal("expected error for host tampering, got nil")
	}
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch, got %v", err)
	}
}

func TestVerifyHostCanonicalizationIsCaseInsensitive(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "https://SERVICE-A.example.com/api/deploy", bytes.NewReader(body))
	req.Host = "SERVICE-A.example.com"
	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	verify := httptest.NewRequest(http.MethodPost, "https://service-a.example.com/api/deploy", bytes.NewReader(body))
	verify.Host = "service-a.example.com"
	verify.Header = req.Header

	err := VerifyRequest(verify, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if err != nil {
		t.Fatalf("VerifyRequest should accept host case changes, got %v", err)
	}
}

func TestVerifyContentTypeTampering(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	tampered := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
	tampered.Header = req.Header.Clone()
	tampered.Header.Set("Content-Type", "text/plain")

	err := VerifyRequest(tampered, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected ErrSignatureMismatch, got %v", err)
	}
}

func TestVerifyRequest_RejectsDuplicateContentType(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}
	req.Header.Add("Content-Type", "text/plain")

	err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("expected ErrInvalidHeaders, got %v", err)
	}
}

func TestSignRequest_RejectsInvalidContentTypeValue(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json\r\nX-Evil: 1")

	err := SignRequest(req, body, store, WithSigner(signer))
	if !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("expected ErrInvalidHeaders, got %v", err)
	}
}

func TestSignRequest_RejectsInvalidCurrentKeyID(t *testing.T) {
	body := []byte(`{"action":"deploy"}`)

	for _, keyID := range []string{
		"",
		" primary",
		"primary,secondary",
		strings.Repeat("k", keyIDMaxLen+1),
	} {
		t.Run(keyID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
			err := SignRequest(req, body, malformedCurrentKeyStore{keyID: keyID})
			if !errors.Is(err, ErrInvalidHeaders) {
				t.Fatalf("expected ErrInvalidHeaders, got %v", err)
			}
			if req.Header.Get(HeaderSignature) != "" {
				t.Fatalf("signature header was set despite invalid key ID")
			}
		})
	}
}

func TestVerifyRequest_RejectsInvalidSignatureHeaderValues(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/deploy", bytes.NewReader(body))
	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}
	req.Header.Set(HeaderKeyID, "primary\r\nX-Evil: 1")

	err := VerifyRequest(req, body, store, WithVerifySigner(signer), freshNonceStoreOpt())
	if !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("expected ErrInvalidHeaders, got %v", err)
	}
}

func TestVerifyRequest_RejectsOversizedSignatureHeaders(t *testing.T) {
	store := testStore()
	body := []byte(`{}`)

	tests := []struct {
		name   string
		header string
		value  string
		want   error
	}{
		{name: "signature", header: HeaderSignature, value: "sha256=" + strings.Repeat("a", sha256.Size*2+1), want: ErrInvalidHeaders},
		{name: "timestamp", header: HeaderTimestamp, value: strings.Repeat("1", timestampMaxLen+1), want: ErrInvalidHeaders},
		{name: "key id", header: HeaderKeyID, value: strings.Repeat("k", keyIDMaxLen+1), want: ErrInvalidHeaders},
		{name: "nonce", header: HeaderNonce, value: strings.Repeat("a", nonceMaxLen+1), want: ErrNonceTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/x", bytes.NewReader(body))
			req.Header.Set(HeaderSignature, "sha256="+strings.Repeat("a", sha256.Size*2))
			req.Header.Set(HeaderTimestamp, "1718452800")
			req.Header.Set(HeaderKeyID, "primary")
			req.Header.Set(HeaderNonce, testNonce("oversized-headers"))
			req.Header.Set(tt.header, tt.value)

			err := VerifyRequest(req, body, store, freshNonceStoreOpt())
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestVerifyRequest_RejectsAmbiguousCommaHeaders(t *testing.T) {
	store := testStore()
	body := []byte(`{}`)

	for _, header := range []string{HeaderSignature, HeaderTimestamp, HeaderKeyID, HeaderNonce} {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/x", bytes.NewReader(body))
			req.Header.Set(HeaderSignature, "sha256="+strings.Repeat("a", sha256.Size*2))
			req.Header.Set(HeaderTimestamp, "1718452800")
			req.Header.Set(HeaderKeyID, "primary")
			req.Header.Set(HeaderNonce, testNonce("comma-headers"))
			req.Header.Set(header, req.Header.Get(header)+",evil")

			err := VerifyRequest(req, body, store, freshNonceStoreOpt())
			if !errors.Is(err, ErrInvalidHeaders) {
				t.Fatalf("expected ErrInvalidHeaders, got %v", err)
			}
		})
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

func TestVerifyRequest_RejectsMalformedNonceBeforeStore(t *testing.T) {
	store := testStore()
	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/x", bytes.NewReader(body))
	req.Header.Set(HeaderSignature, "sha256=abc")
	req.Header.Set(HeaderTimestamp, "1718452800")
	req.Header.Set(HeaderKeyID, "primary")
	req.Header.Set(HeaderNonce, "not-base64")

	nonceStore := &recordingNonceStore{}
	err := VerifyRequest(req, body, store, WithNonceStore(nonceStore))
	if !errors.Is(err, ErrNonceInvalid) {
		t.Fatalf("expected ErrNonceInvalid, got %v", err)
	}
	if nonceStore.calls != 0 {
		t.Fatalf("malformed nonce reached nonce store %d times", nonceStore.calls)
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

type recordingNonceStore struct {
	calls int
}

func (s *recordingNonceStore) SeenOrStore(string) (bool, error) {
	s.calls++
	return true, nil
}

type malformedCurrentKeyStore struct {
	keyID string
}

func (s malformedCurrentKeyStore) Key(string) ([]byte, bool) {
	return testKey(32, 1), true
}

func (s malformedCurrentKeyStore) CurrentKeyID() (string, []byte) {
	return s.keyID, testKey(32, 1)
}
