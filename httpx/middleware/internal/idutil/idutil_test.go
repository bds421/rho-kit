package idutil

import (
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	id := Generate()
	if len(id) != 32 {
		t.Errorf("Generate() length = %d, want 32", len(id))
	}

	id2 := Generate()
	if id == id2 {
		t.Error("Generate() should produce unique IDs")
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
