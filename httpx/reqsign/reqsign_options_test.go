package reqsign

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/crypto/v2/signing"
)

func assertBodyTooLargeStable(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
	for _, s := range forbidden {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("body-size error leaked %q: %v", s, err)
		}
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
		freshNonceStoreOpt(),
	)
	if err == nil {
		t.Fatal("expected error for custom maxAge exceeded, got nil")
	}
}

func TestReqsignOptionsRejectInvalidValues(t *testing.T) {
	assertPanics := func(name string, fn func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			fn()
		})
	}

	assertPanics("WithSigner nil", func() { WithSigner(nil) })
	assertPanics("WithVerifySigner nil", func() { WithVerifySigner(nil) })
	assertPanics("WithMaxAge zero", func() { WithMaxAge(0) })
	assertPanics("WithMaxAge negative", func() { WithMaxAge(-time.Second) })
	assertPanics("WithSignMaxBodySize zero", func() { WithSignMaxBodySize(0) })
	assertPanics("WithSignMaxBodySize negative", func() { WithSignMaxBodySize(-1) })
	assertPanics("WithVerifyMaxBodySize zero", func() { WithVerifyMaxBodySize(0) })
	assertPanics("WithVerifyMaxBodySize negative", func() { WithVerifyMaxBodySize(-1) })
	assertPanics("NewSigningTransport nil option", func() {
		NewSigningTransport(http.DefaultTransport, testStore(), nil)
	})
	assertPanics("SignRequest nil option", func() {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		_ = SignRequest(req, nil, testStore(), nil)
	})
	assertPanics("VerifyRequest nil option", func() {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		_ = VerifyRequest(req, nil, testStore(), nil)
	})
	assertPanics("RequireSignedRequest nil option", func() {
		RequireSignedRequest(testStore(), nil)
	})
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
	assertBodyTooLargeStable(t, err, "100", "101")
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
		freshNonceStoreOpt(),
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
		freshNonceStoreOpt(),
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

func TestSignRequest_RejectsOversizedBody(t *testing.T) {
	store := testStore()

	body := make([]byte, 101)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	err := SignRequest(req, body, store, WithSignMaxBodySize(100))
	if err == nil {
		t.Fatal("expected ErrBodyTooLarge for oversized body, got nil")
	}
	assertBodyTooLargeStable(t, err, "100", "101")
	if req.Header.Get(HeaderSignature) != "" {
		t.Error("oversized body must not produce a signature header")
	}
}

func TestVerifyRequest_RejectsOversizedBody(t *testing.T) {
	store := testStore()
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	signer := signing.NewSigner(signing.WithClock(fixedClock(now)))

	body := make([]byte, 50)
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader(body))

	if err := SignRequest(req, body, store, WithSigner(signer)); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}

	oversized := make([]byte, 101)
	err := VerifyRequest(req, oversized, store,
		WithVerifySigner(signer),
		WithVerifyMaxBodySize(100),
		freshNonceStoreOpt(),
	)
	if err == nil {
		t.Fatal("expected ErrBodyTooLarge for oversized body during verify, got nil")
	}
	assertBodyTooLargeStable(t, err, "100", "101")
}
