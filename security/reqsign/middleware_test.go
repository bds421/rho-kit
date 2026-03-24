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

func TestMiddleware_ValidSignature(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := []byte(`{"action":"test"}`)

	var downstreamBody []byte
	handler := RequireSignedRequest(store, WithVerifySigner(signer))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			downstreamBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))
	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}
	// Replace body since SignRequest doesn't modify it.
	req.Body = io.NopCloser(bytes.NewReader(body))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(downstreamBody, body) {
		t.Errorf("downstream body = %q, want %q", downstreamBody, body)
	}
}

func TestMiddleware_MissingSignature(t *testing.T) {
	store := testStore()
	handler := RequireSignedRequest(store)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestMiddleware_InvalidSignature(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	handler := RequireSignedRequest(store, WithVerifySigner(signer))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	body := []byte(`{"action":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))
	// Set garbage signature headers.
	req.Header.Set(HeaderSignature, "sha256=invalid")
	req.Header.Set(HeaderTimestamp, "1718452800")
	req.Header.Set(HeaderKeyID, "primary")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestMiddleware_ExpiredSignature(t *testing.T) {
	store := testStore()
	signTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	verifyTime := signTime.Add(10 * time.Minute)

	signSigner := signing.NewSigner(signing.WithClock(fixedClock(signTime)))
	verifySigner := signing.NewSigner(signing.WithClock(fixedClock(verifyTime)))

	handler := RequireSignedRequest(store, WithVerifySigner(verifySigner))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	body := []byte(`{"action":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))
	if err := SignRequest(req, body, store, WithSigner(signSigner)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestMiddleware_BodyTooLarge(t *testing.T) {
	store := testStore()
	handler := RequireSignedRequest(store)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	// Create a body larger than maxBodySize (1 MiB).
	largeBody := make([]byte, maxBodySize+1)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(largeBody))
	// Set signature headers so we don't fail on missing headers first.
	req.Header.Set(HeaderSignature, "sha256=placeholder")
	req.Header.Set(HeaderTimestamp, "1718452800")
	req.Header.Set(HeaderKeyID, "primary")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rr.Code)
	}
}

func TestMiddleware_GETRequest(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	handler := RequireSignedRequest(store, WithVerifySigner(signer))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	if err := SignRequest(req, nil, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
}
