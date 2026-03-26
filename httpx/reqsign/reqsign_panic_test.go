package reqsign

import (
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestNewSigningTransport_PanicsNilKeyStore(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil KeyStore")
		}
	}()
	NewSigningTransport(http.DefaultTransport, nil)
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
