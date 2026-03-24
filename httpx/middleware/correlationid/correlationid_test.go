package correlationid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/core/contextutil"
	"github.com/bds421/rho-kit/httpx"
	"github.com/google/uuid"
)

func TestWithCorrelationID_GeneratesID(t *testing.T) {
	var capturedID string
	handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = contextutil.CorrelationID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedID == "" {
		t.Error("expected correlation ID to be generated")
	}
	// Generated IDs should now be valid UUID v7 format (36 chars).
	parsed, err := uuid.Parse(capturedID)
	if err != nil {
		t.Errorf("generated ID %q is not a valid UUID: %v", capturedID, err)
	}
	if parsed.Version() != 7 {
		t.Errorf("generated UUID version = %d, want 7", parsed.Version())
	}
	if rec.Header().Get(Header) != capturedID {
		t.Error("X-Correlation-Id response header doesn't match context value")
	}
}

func TestWithCorrelationID_UsesIncomingHeader(t *testing.T) {
	var capturedID string
	handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = contextutil.CorrelationID(r.Context())
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
		{"space in value", "trace abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedID string
			handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedID = contextutil.CorrelationID(r.Context())
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
			// Generated IDs should be UUID v7 format (36 chars).
			if len(capturedID) != 36 {
				t.Errorf("generated ID length = %d, want 36 (UUID format)", len(capturedID))
			}
		})
	}
}

func TestWithCorrelationID_AcceptsMaxLength(t *testing.T) {
	var capturedID string
	handler := WithCorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = contextutil.CorrelationID(r.Context())
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

	//nolint:staticcheck // intentionally testing deprecated shim
	if id := httpx.CorrelationID(ctx); id != "" {
		t.Errorf("empty context should return empty string, got %q", id)
	}

	//nolint:staticcheck // intentionally testing deprecated shim
	ctx = httpx.SetCorrelationID(ctx, "test-correlation-id")
	//nolint:staticcheck // intentionally testing deprecated shim
	if id := httpx.CorrelationID(ctx); id != "test-correlation-id" {
		t.Errorf("CorrelationID = %q, want %q", id, "test-correlation-id")
	}
}

func TestDeprecatedPropagateHTTP(t *testing.T) {
	// Verify deprecated wrappers still delegate correctly.
	//nolint:staticcheck // intentionally testing deprecated shim
	ctx := httpx.SetCorrelationID(context.Background(), "deprecated-id")
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	PropagateHTTP(ctx, req)

	if got := req.Header.Get(Header); got != "deprecated-id" {
		t.Errorf("header = %q, want %q", got, "deprecated-id")
	}
}

func TestDeprecatedPropagateMessageHeader(t *testing.T) {
	//nolint:staticcheck // intentionally testing deprecated shim
	ctx := httpx.SetCorrelationID(context.Background(), "deprecated-msg-id")

	key, value := PropagateMessageHeader(ctx)

	if key != Header {
		t.Errorf("key = %q, want %q", key, Header)
	}
	if value != "deprecated-msg-id" {
		t.Errorf("value = %q, want %q", value, "deprecated-msg-id")
	}
}

func TestIsValidCorrelationID_MaxLenWiring(t *testing.T) {
	ok := isValidCorrelationID(strings.Repeat("a", 128))
	if !ok {
		t.Error("128-char ID should be accepted")
	}
	notOK := isValidCorrelationID(strings.Repeat("a", 129))
	if notOK {
		t.Error("129-char ID should be rejected")
	}
}
