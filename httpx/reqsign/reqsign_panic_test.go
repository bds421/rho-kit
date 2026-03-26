package reqsign

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSignRequest_PanicsNilKeyStore(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil KeyStore")
		}
		if msg, ok := r.(string); !ok || msg != nilKeyStoreMsg {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_ = SignRequest(req, nil, nil)
}

func TestVerifyRequest_PanicsNilKeyStore(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil KeyStore")
		}
		if msg, ok := r.(string); !ok || msg != nilKeyStoreMsg {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_ = VerifyRequest(req, nil, nil)
}

func TestNewSigningTransport_PanicsNilKeyStore(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil KeyStore")
		}
		if msg, ok := r.(string); !ok || msg != nilKeyStoreMsg {
			t.Errorf("unexpected panic value: %v", r)
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
		if msg, ok := r.(string); !ok || msg != nilKeyStoreMsg {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	RequireSignedRequest(nil)
}
