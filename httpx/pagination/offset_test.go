package pagination

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseOffset_defaults(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	limit, offset, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 25 {
		t.Errorf("limit = %d, want 25", limit)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0", offset)
	}
}

func TestParseOffset_customValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=10&offset=40", nil)
	limit, offset, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 10 {
		t.Errorf("limit = %d, want 10", limit)
	}
	if offset != 40 {
		t.Errorf("offset = %d, want 40", offset)
	}
}

func TestParseOffset_clampsLimitToMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=500", nil)
	limit, _, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 100 {
		t.Errorf("limit = %d, want 100 (clamped)", limit)
	}
}

func TestParseOffset_zeroLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=0", nil)
	limit, _, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 25 {
		t.Errorf("limit = %d, want 25 (default)", limit)
	}
}

func TestParseOffset_negativeLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=-5", nil)
	limit, _, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 25 {
		t.Errorf("limit = %d, want 25 (default)", limit)
	}
}

func TestParseOffset_invalidLimitUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?limit=abc", nil)
	limit, _, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 25 {
		t.Errorf("limit = %d, want 25 (default)", limit)
	}
}

func TestParseOffset_invalidOffsetUsesDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?offset=abc", nil)
	_, offset, err := ParseOffset(r, 25, 7, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if offset != 7 {
		t.Errorf("offset = %d, want 7 (default)", offset)
	}
}

func TestParseOffset_negativeOffsetClampsToZero(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items?offset=-5", nil)
	_, offset, err := ParseOffset(r, 25, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0 (clamped)", offset)
	}
}

func TestParseOffset_defaultGreaterThanMaxClampsToMax(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	limit, _, err := ParseOffset(r, 250, 0, 100)
	if err != nil {
		t.Fatalf("ParseOffset returned error: %v", err)
	}
	if limit != 100 {
		t.Errorf("limit = %d, want 100 (clamped)", limit)
	}
}

func TestParseOffset_rejectsInvalidLimitConfig(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)

	for _, tt := range []struct {
		name         string
		defaultLimit int
		maxLimit     int
	}{
		{name: "zero default", defaultLimit: 0, maxLimit: 100},
		{name: "negative default", defaultLimit: -1, maxLimit: 100},
		{name: "zero max", defaultLimit: 25, maxLimit: 0},
		{name: "negative max", defaultLimit: 25, maxLimit: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseOffset(r, tt.defaultLimit, 0, tt.maxLimit)
			if !errors.Is(err, ErrInvalidLimitConfig) {
				t.Fatalf("ParseOffset error = %v, want ErrInvalidLimitConfig", err)
			}
		})
	}
}

func TestParseOffset_rejectsInvalidDefaultOffset(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/items", nil)

	_, _, err := ParseOffset(r, 25, -1, 100)
	if !errors.Is(err, ErrInvalidOffsetConfig) {
		t.Fatalf("ParseOffset error = %v, want ErrInvalidOffsetConfig", err)
	}
}

func TestParseOffset_rejectsInvalidRequest(t *testing.T) {
	for _, tt := range []struct {
		name string
		r    *http.Request
	}{
		{name: "nil request", r: nil},
		{name: "nil URL", r: &http.Request{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParseOffset(tt.r, 25, 0, 100)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("ParseOffset error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestParseOffset_rejectsAmbiguousQueryParams(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
	}{
		{name: "duplicate limit", url: "/items?limit=10&limit=20"},
		{name: "duplicate offset", url: "/items?offset=10&offset=20"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.url, nil)
			_, _, err := ParseOffset(r, 25, 0, 100)
			if !errors.Is(err, ErrAmbiguousQueryParam) {
				t.Fatalf("ParseOffset error = %v, want ErrAmbiguousQueryParam", err)
			}
			if err.Error() != ErrAmbiguousQueryParam.Error() {
				t.Fatalf("ParseOffset error = %q, want stable ambiguous-query error", err)
			}
		})
	}
}
