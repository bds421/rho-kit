package storagehttp

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestUUIDKeyFunc(t *testing.T) {
	t.Parallel()

	t.Run("generates key with prefix and extension from content type", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("avatars")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: "image/jpeg"}

		key, err := fn(r, "photo.jpg", meta)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(key, "avatars/"))
		assert.True(t, strings.HasSuffix(key, ".jpg"))
		require.NoError(t, storage.ValidateKey(key))
		// UUID is 36 chars + "/" + prefix + ext.
		assert.Len(t, key, len("avatars/")+36+len(".jpg"))
	})

	t.Run("trims trailing slash from valid prefix", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("avatars/")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: "image/jpeg"}

		key, err := fn(r, "photo.jpg", meta)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(key, "avatars/"))
		assert.False(t, strings.Contains(key, "//"))
		require.NoError(t, storage.ValidateKey(key))
	})

	t.Run("works without prefix", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: "application/pdf"}

		key, err := fn(r, "document.pdf", meta)
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(key, ".pdf"))
		assert.False(t, strings.Contains(key, "/"))
	})

	t.Run("omits extension for empty content type", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("files")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{}

		key, err := fn(r, "README", meta)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(key, "files/"))
		assert.False(t, strings.HasSuffix(key, "."))
	})

	t.Run("generates unique keys", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("test")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: "text/plain"}

		key1, _ := fn(r, "file.txt", meta)
		key2, _ := fn(r, "file.txt", meta)
		assert.NotEqual(t, key1, key2)
	})

	t.Run("derives extension from content type not filename", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("uploads")
		r := httptest.NewRequest("POST", "/upload", nil)
		// Filename says .php but content type says PNG — extension should be .png.
		meta := storage.ObjectMeta{ContentType: "image/png"}

		key, err := fn(r, "exploit.php", meta)
		require.NoError(t, err)
		assert.False(t, strings.HasSuffix(key, ".php"))
		assert.True(t, strings.HasSuffix(key, ".png"))
	})

	t.Run("falls back to filename ext when content type unrecognised", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("uploads")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: "application/octet-stream"}

		key, err := fn(r, "data.bin", meta)
		require.NoError(t, err)
		// MIME yields no extension, but filename's ".bin" passes the
		// alphanumeric allowlist so it's preserved.
		assert.True(t, strings.HasSuffix(key, ".bin"))
	})

	t.Run("omits extension when neither content type nor filename helps", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("uploads")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: ""}

		key, err := fn(r, "noext", meta)
		require.NoError(t, err)
		assert.Len(t, key, len("uploads/")+36)
	})

	t.Run("rejects malicious filename ext", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("uploads")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: ""}

		// Path traversal via ext + special chars must not appear in key.
		key, err := fn(r, "evil/../../etc/passwd", meta)
		require.NoError(t, err)
		// path.Ext("evil/../../etc/passwd") returns "" — no extension to leak.
		assert.False(t, strings.Contains(key, ".."))
		assert.False(t, strings.Contains(key, "passwd"))
	})

	t.Run("panics on invalid prefixes", func(t *testing.T) {
		t.Parallel()
		for _, prefix := range []string{
			"/avatars",
			"avatars/..",
			"avatars//2026",
			`avatars\2026`,
		} {
			prefix := prefix
			t.Run(prefix, func(t *testing.T) {
				t.Parallel()
				assert.Panics(t, func() {
					UUIDKeyFunc(prefix)
				})
			})
		}
	})

	t.Run("invalid prefix panic does not reflect prefix", func(t *testing.T) {
		t.Parallel()
		assert.PanicsWithValue(t, "storagehttp: UUIDKeyFunc: invalid UUIDKeyFunc prefix", func() {
			UUIDKeyFunc("secret-token/..")
		})
	})
}

func TestExtensionFromContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		want        string
	}{
		{"jpeg", "image/jpeg", ".jpg"},
		{"png", "image/png", ".png"},
		{"pdf", "application/pdf", ".pdf"},
		{"empty", "", ""},
		{"octet-stream", "application/octet-stream", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extensionFromContentType(tt.contentType)
			assert.Equal(t, tt.want, got)
		})
	}
}
