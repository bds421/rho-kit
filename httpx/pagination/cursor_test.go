package pagination

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCursorParams_defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	cp, err := ParseCursorParams(r, 20, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20", cp.Limit)
	}
	if cp.Cursor != "" {
		t.Errorf("Cursor = %q, want empty", cp.Cursor)
	}
}

func TestParseCursorParams_customValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?cursor=abc-123&limit=50", nil)
	cp, err := ParseCursorParams(r, 20, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 50 {
		t.Errorf("Limit = %d, want 50", cp.Limit)
	}
	if cp.Cursor != "abc-123" {
		t.Errorf("Cursor = %q, want 'abc-123'", cp.Cursor)
	}
}

func TestParseCursorParams_clampsToMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=500", nil)
	cp, err := ParseCursorParams(r, 20, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 100 {
		t.Errorf("Limit = %d, want 100 (clamped)", cp.Limit)
	}
}

func TestParseCursorParams_negativeUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=-5", nil)
	cp, err := ParseCursorParams(r, 20, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (default)", cp.Limit)
	}
}

func TestParseCursorParams_zeroUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=0", nil)
	cp, err := ParseCursorParams(r, 20, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (default)", cp.Limit)
	}
}

func TestParseCursorParams_invalidLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=abc", nil)
	cp, err := ParseCursorParams(r, 20, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (default)", cp.Limit)
	}
}

func TestParseCursorParams_defaultGreaterThanMaxClampsToMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	cp, err := ParseCursorParams(r, 200, 100)
	if err != nil {
		t.Fatalf("ParseCursorParams returned error: %v", err)
	}

	if cp.Limit != 100 {
		t.Errorf("Limit = %d, want 100 (clamped)", cp.Limit)
	}
}

func TestParseCursorParams_rejectsOversizedCursor(t *testing.T) {
	cursor := strings.Repeat("a", MaxCursorLen+1)
	r := httptest.NewRequest(http.MethodGet, "/items?cursor="+cursor, nil)

	_, err := ParseCursorParams(r, 20, 100)
	if !errors.Is(err, ErrCursorTooLong) {
		t.Fatalf("ParseCursorParams error = %v, want ErrCursorTooLong", err)
	}
}

func TestParseCursorParams_rejectsInvalidLimitConfig(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)

	for _, tt := range []struct {
		name         string
		defaultLimit int
		maxLimit     int
	}{
		{name: "zero default", defaultLimit: 0, maxLimit: 100},
		{name: "negative default", defaultLimit: -1, maxLimit: 100},
		{name: "zero max", defaultLimit: 20, maxLimit: 0},
		{name: "negative max", defaultLimit: 20, maxLimit: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCursorParams(r, tt.defaultLimit, tt.maxLimit)
			if !errors.Is(err, ErrInvalidLimitConfig) {
				t.Fatalf("ParseCursorParams error = %v, want ErrInvalidLimitConfig", err)
			}
		})
	}
}

func TestParseCursorParams_rejectsInvalidRequest(t *testing.T) {
	for _, tt := range []struct {
		name string
		r    *http.Request
	}{
		{name: "nil request", r: nil},
		{name: "nil URL", r: &http.Request{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCursorParams(tt.r, 20, 100)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("ParseCursorParams error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestParseCursorParams_rejectsAmbiguousQueryParams(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
	}{
		{name: "duplicate cursor", url: "/items?cursor=a&cursor=b"},
		{name: "duplicate limit", url: "/items?limit=10&limit=20"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.url, nil)
			_, err := ParseCursorParams(r, 20, 100)
			if !errors.Is(err, ErrAmbiguousQueryParam) {
				t.Fatalf("ParseCursorParams error = %v, want ErrAmbiguousQueryParam", err)
			}
			if err.Error() != ErrAmbiguousQueryParam.Error() {
				t.Fatalf("ParseCursorParams error = %q, want stable ambiguous-query error", err)
			}
		})
	}
}

func TestValidateCursor_empty(t *testing.T) {
	if err := ValidateCursorUUID(""); err != nil {
		t.Errorf("empty cursor should be valid: %v", err)
	}
}

func TestValidateCursor_validUUID(t *testing.T) {
	if err := ValidateCursorUUID("550e8400-e29b-41d4-a716-446655440000"); err != nil {
		t.Errorf("valid UUID should pass: %v", err)
	}
}

func TestValidateCursor_invalidUUID(t *testing.T) {
	err := ValidateCursorUUID("not-a-uuid")
	if err == nil {
		t.Error("invalid UUID should fail")
		return
	}
	if strings.Contains(err.Error(), "not-a-uuid") {
		t.Fatalf("invalid UUID error leaked raw cursor: %v", err)
	}
}

type item struct {
	ID   string
	Name string
}

func TestBuildResult_hasMore(t *testing.T) {
	// Fetch limit+1 items to detect hasMore
	items := []item{
		{ID: "3", Name: "c"},
		{ID: "2", Name: "b"},
		{ID: "1", Name: "a"},
	}

	result := BuildResult(items, 2, func(i item) string { return i.ID })

	if !result.HasMore {
		t.Error("expected HasMore=true")
	}
	if len(result.Data) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Data))
	}
	if result.NextCursor != "2" {
		t.Errorf("NextCursor = %q, want '2'", result.NextCursor)
	}
}

func TestBuildResult_noMore(t *testing.T) {
	items := []item{
		{ID: "2", Name: "b"},
		{ID: "1", Name: "a"},
	}

	result := BuildResult(items, 5, func(i item) string { return i.ID })

	if result.HasMore {
		t.Error("expected HasMore=false")
	}
	if len(result.Data) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Data))
	}
	if result.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty when HasMore=false", result.NextCursor)
	}
}

func TestBuildResult_exactlyLimit(t *testing.T) {
	items := []item{
		{ID: "2", Name: "b"},
		{ID: "1", Name: "a"},
	}

	result := BuildResult(items, 2, func(i item) string { return i.ID })

	if result.HasMore {
		t.Error("expected HasMore=false when items == limit")
	}
	if len(result.Data) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Data))
	}
}

func TestBuildResult_empty(t *testing.T) {
	var items []item
	result := BuildResult(items, 10, func(i item) string { return i.ID })

	if result.HasMore {
		t.Error("expected HasMore=false for empty result")
	}
	if result.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty", result.NextCursor)
	}
	if len(result.Data) != 0 {
		t.Errorf("expected 0 items, got %d", len(result.Data))
	}
}

func TestBuildResult_doesNotMutateInput(t *testing.T) {
	items := []item{
		{ID: "3", Name: "c"},
		{ID: "2", Name: "b"},
		{ID: "1", Name: "a"},
	}
	originalLen := len(items)

	BuildResult(items, 2, func(i item) string { return i.ID })

	if len(items) != originalLen {
		t.Errorf("input slice length changed from %d to %d", originalLen, len(items))
	}
}

func TestBuildResult_singleItem(t *testing.T) {
	items := []item{{ID: "1", Name: "a"}}
	result := BuildResult(items, 10, func(i item) string { return i.ID })

	if result.HasMore {
		t.Error("expected HasMore=false")
	}
	if result.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty when HasMore=false", result.NextCursor)
	}
}

func TestCursorSigner_Close_ZeroesSecret(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	signer, err := NewCursorSigner(secret)
	if err != nil {
		t.Fatalf("NewCursorSigner: %v", err)
	}
	// Sanity: encoding produces a non-empty cursor before Close.
	if signer.Encode("payload") == "" {
		t.Fatal("expected non-empty cursor before Close")
	}
	if err := signer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := signer.Encode("payload"); got != "" {
		t.Fatalf("Encode after Close = %q, want \"\"", got)
	}
	if _, err := signer.Decode("AaBb.AaBb"); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("Decode after Close = %v, want ErrCursorInvalid", err)
	}
	// Idempotent.
	if err := signer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCursorSigner_Close_NilReceiver(t *testing.T) {
	var s *CursorSigner
	if err := s.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}
