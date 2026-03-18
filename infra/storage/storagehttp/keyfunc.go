package storagehttp

import (
	"net/http"
	"path"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/uuid"

	"github.com/bds421/rho-kit/infra/storage"
)

// KeyFunc derives a storage key from the request, original filename, and
// validated metadata. See [UploadOptions.KeyFunc] for details.
type KeyFunc = func(r *http.Request, filename string, meta storage.ObjectMeta) (string, error)

// UUIDKeyFunc returns a [KeyFunc] that generates unique storage keys.
// The returned key has the format "<prefix>/<uuid><ext>" where ext is
// derived from meta.ContentType using the mimetype library. If the content
// type is empty or unrecognised, the extension is omitted.
//
// Example:
//
//	UUIDKeyFunc("avatars") → "avatars/550e8400-e29b-41d4-a716-446655440000.jpg"
//	UUIDKeyFunc("")        → "550e8400-e29b-41d4-a716-446655440000.jpg"
func UUIDKeyFunc(prefix string) KeyFunc {
	return func(_ *http.Request, _ string, meta storage.ObjectMeta) (string, error) {
		ext := extensionFromContentType(meta.ContentType)
		id := uuid.Must(uuid.NewV7()).String()
		key := id + ext
		if prefix != "" {
			key = path.Join(prefix, key)
		}
		return key, nil
	}
}

// extensionFromContentType returns the file extension (including the leading
// dot) for the given MIME type using the mimetype library's registry.
// Returns "" for empty, "application/octet-stream", or unrecognised types.
func extensionFromContentType(contentType string) string {
	if contentType == "" || contentType == "application/octet-stream" {
		return ""
	}
	m := mimetype.Lookup(contentType)
	if m == nil {
		return ""
	}
	return m.Extension()
}
