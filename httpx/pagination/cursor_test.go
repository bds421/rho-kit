package pagination

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseCursorParams_defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	cp := ParseCursorParams(r, 20, 100)

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20", cp.Limit)
	}
	if cp.Cursor != "" {
		t.Errorf("Cursor = %q, want empty", cp.Cursor)
	}
}

func TestParseCursorParams_customValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?cursor=abc-123&limit=50", nil)
	cp := ParseCursorParams(r, 20, 100)

	if cp.Limit != 50 {
		t.Errorf("Limit = %d, want 50", cp.Limit)
	}
	if cp.Cursor != "abc-123" {
		t.Errorf("Cursor = %q, want 'abc-123'", cp.Cursor)
	}
}

func TestParseCursorParams_clampsToMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=500", nil)
	cp := ParseCursorParams(r, 20, 100)

	if cp.Limit != 100 {
		t.Errorf("Limit = %d, want 100 (clamped)", cp.Limit)
	}
}

func TestParseCursorParams_negativeUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=-5", nil)
	cp := ParseCursorParams(r, 20, 100)

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (default)", cp.Limit)
	}
}

func TestParseCursorParams_zeroUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=0", nil)
	cp := ParseCursorParams(r, 20, 100)

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (default)", cp.Limit)
	}
}

func TestParseCursorParams_invalidLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=abc", nil)
	cp := ParseCursorParams(r, 20, 100)

	if cp.Limit != 20 {
		t.Errorf("Limit = %d, want 20 (default)", cp.Limit)
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
	if err := ValidateCursorUUID("not-a-uuid"); err == nil {
		t.Error("invalid UUID should fail")
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
