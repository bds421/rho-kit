package reqsign

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/signing"
)

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSigningTransport_SetsHeaders(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	var captured *http.Request
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	transport := NewSigningTransport(base, store, WithSigner(signer))

	body := []byte(`{"deploy":true}`)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/deploy", bytes.NewReader(body))

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	if captured.Header.Get(HeaderSignature) == "" {
		t.Error("expected X-Signature header")
	}
	if captured.Header.Get(HeaderTimestamp) == "" {
		t.Error("expected X-Signature-Timestamp header")
	}
	if captured.Header.Get(HeaderKeyID) != "primary" {
		t.Errorf("X-Signature-KeyID = %q, want %q", captured.Header.Get(HeaderKeyID), "primary")
	}
}

func TestSigningTransport_BodyPreserved(t *testing.T) {
	store := testStore()

	var capturedBody []byte
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		capturedBody = b
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	transport := NewSigningTransport(base, store)

	body := []byte(`{"data":"important"}`)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/test", bytes.NewReader(body))

	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}

	if !bytes.Equal(capturedBody, body) {
		t.Errorf("body = %q, want %q", capturedBody, body)
	}
}

func TestSigningTransport_NilBody(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	transport := NewSigningTransport(base, store, WithSigner(signer))

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/api/status", nil)

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if req.Header.Get(HeaderSignature) == "" {
		t.Error("expected signature header on GET request")
	}
}

func TestSigningTransport_OversizedBody(t *testing.T) {
	store := testStore()

	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("base transport should not be called for oversized body")
		return nil, nil
	})

	transport := NewSigningTransport(base, store)

	// Create a body larger than maxSignBodySize (1 MiB).
	oversized := make([]byte, 1<<20+1)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/upload", bytes.NewReader(oversized))

	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
}

func TestSigningTransport_NilBase(t *testing.T) {
	// NewSigningTransport with nil base should not panic.
	store := testStore()
	transport := NewSigningTransport(nil, store)
	if transport.base == nil {
		t.Error("expected default transport when nil base passed")
	}
}
