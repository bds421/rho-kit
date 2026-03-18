package storagehttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/infra/storage/localbackend"
)

func TestParseAndStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("rejects nil KeyFunc", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "KeyFunc is required")
	})

	t.Run("uploads file successfully", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("hello world"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		result, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: passthroughKeyFunc,
		})
		require.NoError(t, err)

		assert.Equal(t, "hello.txt", result.Key)

		// Verify the file was stored.
		rc, _, err := backend.Get(ctx, "hello.txt")
		require.NoError(t, err)
		defer func() { _ = rc.Close() }()
		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, []byte("hello world"), got)
	})

	t.Run("uses custom KeyFunc", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "photo.jpg", []byte("img"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		result, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: func(_ *http.Request, filename string, _ storage.ObjectMeta) (string, error) {
				return "uploads/custom-" + filename, nil
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "uploads/custom-photo.jpg", result.Key)

		ok, err := backend.Exists(ctx, "uploads/custom-photo.jpg")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("uses custom FormField", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "document", "doc.pdf", []byte("pdf"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		result, err := ParseAndStore(ctx, r, backend, UploadOptions{
			FormField: "document",
			KeyFunc:   passthroughKeyFunc,
		})
		require.NoError(t, err)
		assert.Equal(t, "doc.pdf", result.Key)
	})

	t.Run("returns error when form field not found", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "other", "file.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			FormField: "file",
			KeyFunc:   passthroughKeyFunc,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no file part")
	})

	t.Run("applies validators", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "big.txt", []byte("this is too long"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: passthroughKeyFunc,
			Validators: []storage.Validator{
				storage.MaxFileSize(5),
			},
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrValidation))
	})

	t.Run("returns error on invalid multipart", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		r := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not multipart")))
		r.Header.Set("Content-Type", "text/plain")

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: passthroughKeyFunc,
		})
		require.Error(t, err)
	})

	t.Run("rejects empty filename", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)

		// Build a multipart body with an empty filename field.
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		// CreateFormField creates a part with no filename (just a form field).
		part, err := w.CreateFormField("file")
		require.NoError(t, err)
		_, err = part.Write([]byte("data"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		r := httptest.NewRequest(http.MethodPost, "/upload", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())

		_, err = ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: passthroughKeyFunc,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no filename")
	})

	t.Run("returns error when KeyFunc fails", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "file.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: func(_ *http.Request, _ string, _ storage.ObjectMeta) (string, error) {
				return "", errors.New("key derivation failed")
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "key derivation")
	})
}

func newLocalBackend(t *testing.T) *localbackend.LocalBackend {
	t.Helper()
	b, err := localbackend.New(t.TempDir())
	require.NoError(t, err)
	return b
}

// passthroughKeyFunc uses the original filename as the key (for tests only).
func passthroughKeyFunc(_ *http.Request, filename string, _ storage.ObjectMeta) (string, error) {
	return filename, nil
}

func createMultipartBody(t *testing.T, fieldName, fileName string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(fieldName, fileName)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}
