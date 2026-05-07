package pagination

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseOffset_defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	limit, offset := ParseOffset(r, 25, 0, 100)
	if limit != 25 {
		t.Errorf("limit = %d, want 25", limit)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0", offset)
	}
}

func TestParseOffset_customValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=10&offset=40", nil)
	limit, offset := ParseOffset(r, 25, 0, 100)
	if limit != 10 {
		t.Errorf("limit = %d, want 10", limit)
	}
	if offset != 40 {
		t.Errorf("offset = %d, want 40", offset)
	}
}

func TestParseOffset_clampsLimitToMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=500", nil)
	limit, _ := ParseOffset(r, 25, 0, 100)
	if limit != 100 {
		t.Errorf("limit = %d, want 100 (clamped)", limit)
	}
}

func TestParseOffset_zeroLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=0", nil)
	limit, _ := ParseOffset(r, 25, 0, 100)
	if limit != 25 {
		t.Errorf("limit = %d, want 25 (default)", limit)
	}
}

func TestParseOffset_negativeLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=-5", nil)
	limit, _ := ParseOffset(r, 25, 0, 100)
	if limit != 25 {
		t.Errorf("limit = %d, want 25 (default)", limit)
	}
}

func TestParseOffset_invalidLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=abc", nil)
	limit, _ := ParseOffset(r, 25, 0, 100)
	if limit != 25 {
		t.Errorf("limit = %d, want 25 (default)", limit)
	}
}

func TestParseOffset_invalidOffsetUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?offset=abc", nil)
	_, offset := ParseOffset(r, 25, 7, 100)
	if offset != 7 {
		t.Errorf("offset = %d, want 7 (default)", offset)
	}
}

func TestParseOffset_negativeOffsetClampsToZero(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?offset=-5", nil)
	_, offset := ParseOffset(r, 25, 0, 100)
	if offset != 0 {
		t.Errorf("offset = %d, want 0 (clamped)", offset)
	}
}

func TestParseOffset_noMaxLimit(t *testing.T) {
	// maxLimit=0 disables the upper clamp — caller can opt out of it.
	r := httptest.NewRequest(http.MethodGet, "/items?limit=10000", nil)
	limit, _ := ParseOffset(r, 25, 0, 0)
	if limit != 10000 {
		t.Errorf("limit = %d, want 10000 (no clamp)", limit)
	}
}
