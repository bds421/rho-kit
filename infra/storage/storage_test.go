package storage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{name: "valid simple key", key: "file.txt"},
		{name: "valid nested key", key: "uploads/2026/01/avatar.png"},
		{name: "valid with hyphens", key: "my-bucket/some-file.jpg"},
		{name: "empty key", key: "", wantErr: "must not be empty"},
		{name: "null byte", key: "file\x00.txt", wantErr: "invalid characters"},
		{name: "newline", key: "file\n.txt", wantErr: "invalid characters"},
		{name: "carriage return", key: "file\r.txt", wantErr: "invalid characters"},
		{name: "too long", key: strings.Repeat("a", MaxKeyLen+1), wantErr: "exceeds maximum length"},
		{name: "exactly max length", key: strings.Repeat("a", MaxKeyLen)},
		{name: "leading slash", key: "/etc/passwd", wantErr: "must not start with a slash"},
		{name: "path traversal simple", key: "../etc/passwd", wantErr: "path traversal"},
		{name: "path traversal nested", key: "uploads/../../etc/passwd", wantErr: "path traversal"},
		{name: "path traversal mid-path", key: "a/../b", wantErr: "path traversal"},
		{name: "dotdot as filename is ok", key: "uploads/..hidden", wantErr: ""},
		{name: "dotdot in extension is ok", key: "file..txt", wantErr: ""},
		{name: "single dot segment", key: "./file.txt", wantErr: "path traversal"},
		{name: "dot in middle", key: "a/./b", wantErr: "path traversal"},
		{name: "backslash", key: "uploads\\file.txt", wantErr: "backslash"},
		{name: "backslash traversal", key: "uploads\\..\\etc\\passwd", wantErr: "backslash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateKey(tt.key)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
