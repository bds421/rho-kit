package storagehttp

import (
	"net/http"
	"path"
	"strings"

	"github.com/gabriel-vasile/mimetype"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// KeyFunc derives a storage key from the request, original filename, and
// validated metadata. See [UploadOptions.KeyFunc] for details.
type KeyFunc = func(r *http.Request, filename string, meta storage.ObjectMeta) (string, error)

// UUIDKeyFunc returns a [KeyFunc] that generates unique storage keys.
// The returned key has the format "<prefix>/<uuid><ext>" where ext is
// derived from meta.ContentType using the mimetype library. If the content
// type is empty, "application/octet-stream", or unrecognised, the
// extension falls back to a sanitised version of the original filename's
// extension (alphanumeric + dot only, length-limited to 8 chars). If the
// fallback yields no usable extension, the key has none.
//
// Panics if prefix is not a valid storage prefix (see [storage.ValidatePrefix]).
//
// Example:
//
//	UUIDKeyFunc("avatars") → "avatars/550e8400-e29b-41d4-a716-446655440000.jpg"
//	UUIDKeyFunc("")        → "550e8400-e29b-41d4-a716-446655440000.jpg"
func UUIDKeyFunc(prefix string) KeyFunc {
	if err := storage.ValidatePrefix(prefix); err != nil {
		panic("storagehttp: UUIDKeyFunc: invalid prefix: " + err.Error())
	}
	prefix = strings.TrimSuffix(prefix, "/")

	return func(_ *http.Request, filename string, meta storage.ObjectMeta) (string, error) {
		ext := extensionFromContentType(meta.ContentType)
		if ext == "" {
			ext = extensionFromFilename(filename)
		}
		key := id.New() + ext
		if prefix != "" {
			key = prefix + "/" + key
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

// extensionFromFilename extracts the trailing .ext from a user-supplied
// filename and rejects anything that isn't a small alphanumeric extension.
// Defends against:
//   - path-traversal (".../etc/passwd" → "")
//   - oversized extensions (".giiiiiiiiiiiiiiiiiiiiiif" → "")
//   - special characters that break URLs or filesystems
//
// Returns "" when the filename has no extension or the extension fails the
// allowlist; the caller is expected to omit the extension in that case
// rather than trust client-supplied bytes.
func extensionFromFilename(filename string) string {
	ext := path.Ext(filename)
	if ext == "" || ext == "." {
		return ""
	}
	// Strip leading dot, validate, then re-add.
	body := ext[1:]
	if len(body) == 0 || len(body) > 8 {
		return ""
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9':
			// allowed
		default:
			return ""
		}
	}
	return "." + body
}
