package correlationid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/httpx"
)

func TestWithCorrelationID_GeneratesID(t *testing.T) {
	var capturedID string
	handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.CorrelationID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID == "" {
		t.Error("expected correlation ID to be generated")
	}
	if len(capturedID) != 32 {
		t.Errorf("generated ID length = %d, want 32 (hex-encoded 16 bytes)", len(capturedID))
	}
	if rec.Header().Get(Header) != capturedID {
		t.Error("X-Correlation-Id response header doesn't match context value")
	}
}

func TestWithCorrelationID_UsesIncomingHeader(t *testing.T) {
	var capturedID string
	handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.CorrelationID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(Header, "trace-abc-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID != "trace-abc-123" {
		t.Errorf("capturedID = %q, want %q", capturedID, "trace-abc-123")
	}
	if rec.Header().Get(Header) != "trace-abc-123" {
		t.Error("response header should echo the incoming correlation ID")
	}
}

func TestWithCorrelationID_RejectsInvalidHeader(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"too long", strings.Repeat("a", 129)},
		{"control chars", "id-with\nnewline"},
		{"null byte", "id\x00null"},
		{"tab", "id\twith-tab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedID string
			handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedID = httpx.CorrelationID(r.Context())
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(Header, tt.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if capturedID == tt.value {
				t.Errorf("invalid correlation ID %q should be rejected", tt.value)
			}
			if capturedID == "" {
				t.Error("a new ID should be generated for invalid input")
			}
			if len(capturedID) != 32 {
				t.Errorf("generated ID length = %d, want 32", len(capturedID))
			}
		})
	}
}

func TestWithCorrelationID_AcceptsMaxLength(t *testing.T) {
	var capturedID string
	handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = httpx.CorrelationID(r.Context())
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

func TestContextRoundTrip(t *testing.T) {
	ctx := context.Background()

	if id := httpx.CorrelationID(ctx); id != "" {
		t.Errorf("empty context should return empty string, got %q", id)
	}

	ctx = httpx.SetCorrelationID(ctx, "test-correlation-id")
	if id := httpx.CorrelationID(ctx); id != "test-correlation-id" {
		t.Errorf("CorrelationID = %q, want %q", id, "test-correlation-id")
	}
}

func TestPropagateHTTP(t *testing.T) {
	ctx := httpx.SetCorrelationID(context.Background(), "propagated-id")
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	PropagateHTTP(ctx, req)

	if got := req.Header.Get(Header); got != "propagated-id" {
		t.Errorf("header = %q, want %q", got, "propagated-id")
	}
}

func TestPropagateHTTP_NoCorrelationID(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	PropagateHTTP(ctx, req)

	if got := req.Header.Get(Header); got != "" {
		t.Errorf("header should be empty when no correlation ID in context, got %q", got)
	}
}

func TestPropagateMessageHeader(t *testing.T) {
	ctx := httpx.SetCorrelationID(context.Background(), "msg-correlation-id")

	key, value := PropagateMessageHeader(ctx)

	if key != Header {
		t.Errorf("key = %q, want %q", key, Header)
	}
	if value != "msg-correlation-id" {
		t.Errorf("value = %q, want %q", value, "msg-correlation-id")
	}
}

func TestPropagateMessageHeader_NoCorrelationID(t *testing.T) {
	ctx := context.Background()

	key, value := PropagateMessageHeader(ctx)

	if key != "" || value != "" {
		t.Errorf("expected empty key/value, got (%q, %q)", key, value)
	}
}

func TestIsValidCorrelationID_MaxLenWiring(t *testing.T) {
	// Full IsValid table tests live in idutil_test.go.
	// This smoke test verifies the maxCorrelationIDLen (128) wiring.
	ok := isValidCorrelationID(strings.Repeat("a", 128))
	if !ok {
		t.Error("128-char ID should be accepted")
	}
	notOK := isValidCorrelationID(strings.Repeat("a", 129))
	if notOK {
		t.Error("129-char ID should be rejected")
	}
}
