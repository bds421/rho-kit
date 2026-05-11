package reqsign

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
)

type transportReadErrorBody struct {
	err error
}

func (b transportReadErrorBody) Read([]byte) (int, error) {
	return 0, b.err
}

func (b transportReadErrorBody) Close() error {
	return nil
}

type transportCloseErrorBody struct {
	*bytes.Reader
	err error
}

func (b transportCloseErrorBody) Close() error {
	return b.err
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

func TestSigningTransport_ClonesOptions(t *testing.T) {
	opts := []SignOption{WithSignMaxBodySize(1024)}

	transport := NewSigningTransport(nil, testStore(), opts...)
	opts[0] = nil

	if len(transport.opts) != 1 {
		t.Fatalf("opts len = %d, want 1", len(transport.opts))
	}
	if transport.opts[0] == nil {
		t.Fatal("transport option was aliased to caller slice")
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

	var captured *http.Request
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
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
	// Verify the clone (not the original) received signature headers.
	if captured.Header.Get(HeaderSignature) == "" {
		t.Error("expected signature header on GET request")
	}
	// Original request must remain unmodified.
	if req.Header.Get(HeaderSignature) != "" {
		t.Error("original request should not be mutated by RoundTrip")
	}
}

func TestSigningTransport_OversizedBody(t *testing.T) {
	store := testStore()

	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("base transport should not be called for oversized body")
		return nil, nil
	})

	transport := NewSigningTransport(base, store)

	// Create a body larger than MaxBodySize (1 MiB).
	oversized := make([]byte, 1<<20+1)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/upload", bytes.NewReader(oversized))

	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
	assertBodyTooLargeStable(t, err, "1048576", "1048577")
}

func TestSigningTransport_BufferBodyErrorsAreStable(t *testing.T) {
	readErr := errors.New("reader failed for secret-token")
	readReq, _ := http.NewRequest(http.MethodPost, "http://example.com/api/upload", nil)
	readReq.Body = transportReadErrorBody{err: readErr}

	body, err := bufferBody(readReq, 1024)
	if err == nil {
		t.Fatal("expected read error")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil", body)
	}
	if err.Error() != "reqsign: read request body failed" {
		t.Fatalf("error = %q", err.Error())
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("expected wrapped read cause, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("read error leaked request data: %v", err)
	}

	closeErr := errors.New("close failed for secret-token")
	closeReq, _ := http.NewRequest(http.MethodPost, "http://example.com/api/upload", nil)
	closeReq.Body = transportCloseErrorBody{Reader: bytes.NewReader([]byte("body")), err: closeErr}

	body, err = bufferBody(closeReq, 1024)
	if err == nil {
		t.Fatal("expected close error")
	}
	if body != nil {
		t.Fatalf("body = %q, want nil", body)
	}
	if err.Error() != "reqsign: close request body failed" {
		t.Fatalf("error = %q", err.Error())
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected wrapped close cause, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("close error leaked request data: %v", err)
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

func TestSigningTransport_NilBaseUsesKitTransportWhenDefaultTransportReplaced(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	transport := NewSigningTransport(nil, testStore())
	if _, ok := transport.base.(*http.Transport); !ok {
		t.Fatalf("nil base = %T, want *http.Transport fallback", transport.base)
	}
}

// FR-024 [HIGH] regression: SigningTransport used to drain the
// caller's request body via the shallow Clone, so outer retry
// middleware re-reading the original request saw an empty body. The
// fix buffers once and restores fresh independent readers on both
// the caller's req and the clone.
func TestSigningTransport_PreservesCallerBodyAfterRoundTrip(t *testing.T) {
	store := testStore()
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Drain the clone — what a real transport would do.
		_, _ = io.ReadAll(req.Body)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	transport := NewSigningTransport(base, store)

	payload := []byte(`{"data":"intact"}`)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api", bytes.NewReader(payload))

	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	// Caller's body MUST still be readable. Pre-fix this returned 0 bytes.
	if req.Body == nil {
		t.Fatal("req.Body is nil after RoundTrip")
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read req.Body: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("caller body drained — got %q, want %q (FR-024 regression)", got, payload)
	}
	if req.ContentLength != int64(len(payload)) {
		t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(payload))
	}
}

// FR-024 follow-up: clone.GetBody must be set so net/http redirect
// and 100-Continue replay paths can replay the body.
func TestSigningTransport_CloneGetBodyEnablesReplay(t *testing.T) {
	store := testStore()
	var captured *http.Request
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured = req
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	transport := NewSigningTransport(base, store)

	payload := []byte("retryable")
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/x", bytes.NewReader(payload))
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if captured == nil {
		t.Fatal("base RoundTripper not invoked")
	}
	first, _ := io.ReadAll(captured.Body)
	if !bytes.Equal(first, payload) {
		t.Errorf("first read body = %q, want %q", first, payload)
	}
	if captured.GetBody == nil {
		t.Fatal("clone.GetBody must be set so redirects replay the body")
	}
	rc, err := captured.GetBody()
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	defer func() { _ = rc.Close() }()
	second, _ := io.ReadAll(rc)
	if !bytes.Equal(second, payload) {
		t.Errorf("replay body = %q, want %q", second, payload)
	}
}
