package reqsign

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSignRequest_NilKeyStoreReturnsError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	err := SignRequest(req, nil, nil)
	if !errors.Is(err, ErrNilKeyStore) {
		t.Fatalf("expected ErrNilKeyStore, got %v", err)
	}
}

func TestVerifyRequest_NilKeyStoreReturnsError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	err := VerifyRequest(req, nil, nil)
	if !errors.Is(err, ErrNilKeyStore) {
		t.Fatalf("expected ErrNilKeyStore, got %v", err)
	}
}

func TestSignRequest_InvalidRequestReturnsError(t *testing.T) {
	emptyMethod := httptest.NewRequest(http.MethodGet, "/", nil)
	emptyMethod.Method = ""
	invalidMethod := httptest.NewRequest(http.MethodGet, "/", nil)
	invalidMethod.Method = "GET\nsecret-token"
	invalidHost := httptest.NewRequest(http.MethodGet, "/", nil)
	invalidHost.Host = "secret-token bad"
	cases := []struct {
		name     string
		req      *http.Request
		notInErr string
	}{
		{name: "nil request", req: nil},
		{name: "nil URL", req: &http.Request{Method: http.MethodGet, Header: make(http.Header)}},
		{name: "empty method", req: emptyMethod},
		{name: "invalid method", req: invalidMethod, notInErr: "secret-token"},
		{name: "empty host", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}, Header: make(http.Header)}},
		{name: "invalid host", req: invalidHost, notInErr: "secret-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SignRequest(tc.req, nil, testStore())
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
			if tc.notInErr != "" && strings.Contains(err.Error(), tc.notInErr) {
				t.Fatalf("error leaked %q: %v", tc.notInErr, err)
			}
		})
	}
}

func TestSignRequest_InitializesNilHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header = nil

	if err := SignRequest(req, nil, testStore()); err != nil {
		t.Fatalf("SignRequest failed: %v", err)
	}
	if req.Header.Get(HeaderSignature) == "" {
		t.Fatal("expected signature header to be set")
	}
}

func TestSignRequest_RejectsDuplicateContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Type", "text/plain")

	err := SignRequest(req, nil, testStore())
	if !errors.Is(err, ErrInvalidHeaders) {
		t.Fatalf("expected ErrInvalidHeaders, got %v", err)
	}
}

func TestVerifyRequest_InvalidRequestReturnsError(t *testing.T) {
	emptyMethod := httptest.NewRequest(http.MethodGet, "/", nil)
	emptyMethod.Method = ""
	invalidMethod := httptest.NewRequest(http.MethodGet, "/", nil)
	invalidMethod.Method = "GET\nsecret-token"
	invalidHost := httptest.NewRequest(http.MethodGet, "/", nil)
	invalidHost.Host = "secret-token bad"
	cases := []struct {
		name     string
		req      *http.Request
		notInErr string
	}{
		{name: "nil request", req: nil},
		{name: "nil URL", req: &http.Request{Method: http.MethodGet, Header: make(http.Header)}},
		{name: "empty method", req: emptyMethod},
		{name: "invalid method", req: invalidMethod, notInErr: "secret-token"},
		{name: "empty host", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}, Header: make(http.Header)}},
		{name: "invalid host", req: invalidHost, notInErr: "secret-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyRequest(tc.req, nil, testStore(), freshNonceStoreOpt())
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
			if tc.notInErr != "" && strings.Contains(err.Error(), tc.notInErr) {
				t.Fatalf("error leaked %q: %v", tc.notInErr, err)
			}
		})
	}
}

func TestNewSigningTransport_PanicsNilKeyStore(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil KeyStore")
		}
	}()
	NewSigningTransport(http.DefaultTransport, nil)
}

func TestSigningTransport_InvalidRequestReturnsError(t *testing.T) {
	transport := NewSigningTransport(http.DefaultTransport, testStore())
	_, err := transport.RoundTrip(nil)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestRequireSignedRequest_PanicsNilKeyStore(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil KeyStore")
		}
	}()
	RequireSignedRequest(nil)
}
