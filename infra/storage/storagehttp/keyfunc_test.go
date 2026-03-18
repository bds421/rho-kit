package storagehttp

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
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
		// UUID is 36 chars + "/" + prefix + ext.
		assert.Len(t, key, len("avatars/")+36+len(".jpg"))
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

	t.Run("omits extension for application/octet-stream", func(t *testing.T) {
		t.Parallel()
		fn := UUIDKeyFunc("uploads")
		r := httptest.NewRequest("POST", "/upload", nil)
		meta := storage.ObjectMeta{ContentType: "application/octet-stream"}

		key, err := fn(r, "data.bin", meta)
		require.NoError(t, err)
		// UUID only, no extension.
		assert.Len(t, key, len("uploads/")+36)
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
