package idutil

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGenerate(t *testing.T) {
	id := Generate()

	// Must be a valid UUID v7 (36 chars: 8-4-4-4-12 hex with hyphens).
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("Generate() returned invalid UUID %q: %v", id, err)
	}
	if parsed.Version() != 7 {
		t.Errorf("Generate() UUID version = %d, want 7", parsed.Version())
	}

	id2 := Generate()
	if id == id2 {
		t.Error("Generate() should produce unique IDs")
	}
}

func TestGenerate_Format(t *testing.T) {
	id := Generate()
	if len(id) != 36 {
		t.Errorf("Generate() length = %d, want 36 (UUID format)", len(id))
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   bool
	}{
		{"empty", "", 128, false},
		{"valid", "abc-123", 128, true},
		{"max length", strings.Repeat("a", 128), 128, true},
		{"too long", strings.Repeat("a", 129), 128, false},
		{"newline", "abc\n123", 128, false},
		{"tab", "abc\t123", 128, false},
		{"null byte", "abc\x00123", 128, false},
		{"printable ascii", "ABCdef-123_456.789", 128, true},
		{"non-ascii", "abc\x80def", 128, false},
		{"contains space", "abc 123", 128, false},
		{"only spaces", "   ", 128, false},
		{"custom max length", strings.Repeat("a", 65), 64, false},
		{"custom max length ok", strings.Repeat("a", 64), 64, true},
		{"uuid format", "01961234-5678-7abc-8def-0123456789ab", 128, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValid(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("IsValid(%q, %d) = %v, want %v", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
