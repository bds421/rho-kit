package apikey

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractToken_RejectsDuplicateAuthorization(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Authorization", "Bearer key_a_b")
	req.Header.Add("Authorization", "Bearer key_c_d")
	if _, ok := extractToken(req); ok {
		t.Fatal("duplicate Authorization must be rejected")
	}
}

func TestExtractToken_RejectsOversized(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", strings.Repeat("k", maxTokenLen+1))
	if _, ok := extractToken(req); ok {
		t.Fatal("oversized token must be rejected")
	}
}

func TestExtractToken_AcceptsBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer good_token_value")
	got, ok := extractToken(req)
	if !ok || got != "good_token_value" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}
