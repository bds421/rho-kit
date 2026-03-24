package requestid

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/httpx"
	"github.com/google/uuid"
)

func TestWithRequestID_GeneratesID(t *testing.T) {
	var capturedID string
	handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID == "" {
		t.Error("expected request ID to be generated")
	}
	parsed, err := uuid.Parse(capturedID)
	if err != nil {
		t.Fatalf("generated ID %q is not a valid UUID: %v", capturedID, err)
	}
	if parsed.Version() != 7 {
		t.Errorf("generated UUID version = %d, want 7", parsed.Version())
	}
	if rec.Header().Get(Header) != capturedID {
		t.Error("X-Request-Id response header doesn't match context value")
	}
}

func TestWithRequestID_UsesIncomingHeader(t *testing.T) {
	var capturedID string
	handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, "incoming-id-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID != "incoming-id-123" {
		t.Errorf("capturedID = %q, want %q", capturedID, "incoming-id-123")
	}
}

func TestWithRequestID_RejectsLongID(t *testing.T) {
	var capturedID string
	handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	longID := strings.Repeat("a", 129)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, longID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID == longID {
		t.Error("long ID should be rejected and replaced with generated one")
	}
	if capturedID == "" {
		t.Error("a new ID should be generated for invalid input")
	}
}

func TestWithRequestID_RejectsControlChars(t *testing.T) {
	var capturedID string
	handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, "id-with\nnewline")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID == "id-with\nnewline" {
		t.Error("ID with control chars should be rejected")
	}
}

func TestWithRequestID_AcceptsMaxLength(t *testing.T) {
	var capturedID string
	handler := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	maxLenID := strings.Repeat("x", 128)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, maxLenID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID != maxLenID {
		t.Error("ID at max length should be accepted")
	}
}

func TestIsValidRequestID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"valid", "abc-123", true},
		{"max length", strings.Repeat("a", 128), true},
		{"too long", strings.Repeat("a", 129), false},
		{"newline", "abc\n123", false},
		{"tab", "abc\t123", false},
		{"null byte", "abc\x00123", false},
		{"printable ascii", "ABCdef-123_456.789", true},
		{"non-ascii", "abc\x80def", false},
		{"contains space", "abc 123", false},
		{"only spaces", "   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidRequestID(tt.input)
			if got != tt.want {
				t.Errorf("isValidRequestID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
