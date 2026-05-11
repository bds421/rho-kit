package contextutil_test

import (
	"strings"
	"testing"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

func TestIsValidCorrelationToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"uuid", "01961234-5678-7abc-8def-0123456789ab", true},
		{"letters numbers separators", "trace.ABC_123-xyz", true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", contextutil.MaxCorrelationIDLen+1), false},
		{"slash path", "/reset/token", false},
		{"email", "alice@example.com", false},
		{"key value", "token=secret", false},
		{"comma list", "a,b", false},
		{"colon", "trace:span", false},
		{"space", "trace span", false},
		{"newline", "trace\nspan", false},
		{"unicode", "trace-é", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contextutil.IsValidCorrelationToken(tt.input, contextutil.MaxCorrelationIDLen)
			if got != tt.want {
				t.Fatalf("IsValidCorrelationToken(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
