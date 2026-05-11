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
		{name: "empty segment", key: "uploads//avatar.png", wantErr: "empty path segments"},
		{name: "trailing slash", key: "uploads/avatar.png/", wantErr: "empty path segments"},
		{name: "null byte", key: "file\x00.txt", wantErr: "invalid characters"},
		{name: "newline", key: "file\n.txt", wantErr: "invalid characters"},
		{name: "carriage return", key: "file\r.txt", wantErr: "invalid characters"},
		{name: "space", key: "file name.txt", wantErr: "invalid characters"},
		{name: "tab", key: "file\tname.txt", wantErr: "invalid characters"},
		{name: "invalid utf8", key: string([]byte{'f', 'i', 'l', 'e', 0xff}), wantErr: "invalid characters"},
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
				assert.ErrorIs(t, err, ErrValidation)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prefix  string
		wantErr string
	}{
		{name: "empty prefix"},
		{name: "simple", prefix: "uploads"},
		{name: "directory style", prefix: "uploads/"},
		{name: "nested", prefix: "uploads/2026/"},
		{name: "empty segment", prefix: "uploads//2026", wantErr: "empty path segments"},
		{name: "double trailing slash", prefix: "uploads//", wantErr: "empty path segments"},
		{name: "leading slash", prefix: "/uploads", wantErr: "must not start"},
		{name: "dot segment", prefix: "uploads/./", wantErr: "path traversal"},
		{name: "dotdot segment", prefix: "uploads/../", wantErr: "path traversal"},
		{name: "backslash", prefix: `uploads\2026`, wantErr: "backslashes"},
		{name: "space", prefix: "uploads 2026", wantErr: "invalid characters"},
		{name: "tab", prefix: "uploads\t2026", wantErr: "invalid characters"},
		{name: "invalid utf8", prefix: string([]byte{'u', 'p', 0xff}), wantErr: "invalid characters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePrefix(tt.prefix)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.ErrorIs(t, err, ErrValidation)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPathTraversalErrorsDoNotReflectSegments(t *testing.T) {
	t.Parallel()

	err := ValidateKey("secret/../file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
	assert.NotContains(t, err.Error(), "(\"..\")")

	err = ValidatePrefix("secret/./")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
	assert.NotContains(t, err.Error(), "(\".\")")
}

func TestValidateListOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    ListOptions
		wantErr string
	}{
		{name: "zero value"},
		{name: "bounded page", opts: ListOptions{MaxKeys: 10}},
		{name: "valid cursor", opts: ListOptions{StartAfter: "uploads/file.txt"}},
		{name: "negative max keys", opts: ListOptions{MaxKeys: -1}, wantErr: "MaxKeys"},
		{name: "invalid cursor", opts: ListOptions{StartAfter: "bad key"}, wantErr: "StartAfter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateListOptions(tt.opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.ErrorIs(t, err, ErrValidation)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
