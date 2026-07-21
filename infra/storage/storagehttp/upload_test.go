package storagehttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/localbackend"
)

func TestParseAndStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("rejects nil backend", func(t *testing.T) {
		t.Parallel()
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, nil, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backend is required")
	})

	t.Run("rejects nil context", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)
		var nilContext context.Context

		_, err := ParseAndStore(nilContext, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "context is required")
	})

	t.Run("rejects nil request", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)

		_, err := ParseAndStore(ctx, nil, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request is required")
	})

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

	t.Run("rejects missing MaxFileSize", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: passthroughKeyFunc,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MaxFileSize is required")
	})

	t.Run("rejects negative MaxFileSize", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: -42,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MaxFileSize must be positive")
		assert.NotContains(t, err.Error(), "-42")
	})

	t.Run("uploads file successfully", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("hello world"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		result, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
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

	t.Run("closes upload-validator-owned reader when backend fails before consuming", func(t *testing.T) {
		t.Parallel()
		closed := false
		backendErr := errors.New("backend stopped before reading secret-token")
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("hello world"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, failingPutBackend{err: backendErr}, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
			Validators: []storage.Validator{
				func(context.Context, io.Reader, *storage.ObjectMeta) (io.Reader, error) {
					return &uploadTrackingReadCloser{Reader: bytes.NewReader([]byte("validated")), closed: &closed}, nil
				},
			},
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, backendErr)
		assert.EqualError(t, err, "storagehttp: store failed")
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "backend stopped")
		assert.True(t, closed)
	})

	t.Run("detaches upload validators before execution", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("hello world"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		validators := make([]storage.Validator, 2)
		validators[0] = func(_ context.Context, reader io.Reader, _ *storage.ObjectMeta) (io.Reader, error) {
			validators[1] = func(context.Context, io.Reader, *storage.ObjectMeta) (io.Reader, error) {
				return nil, errors.New("mutated validator ran")
			}
			return reader, nil
		}
		validators[1] = func(_ context.Context, reader io.Reader, _ *storage.ObjectMeta) (io.Reader, error) {
			return reader, nil
		}

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: Unlimited,
			Validators:  validators,
		})
		require.NoError(t, err)
	})

	t.Run("rejects duplicate file part Content-Type", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="hello.txt"`)
		header.Add("Content-Type", "text/plain")
		header.Add("Content-Type", "application/octet-stream")
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write([]byte("hello world"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		r := httptest.NewRequest(http.MethodPost, "/upload", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())

		_, err = ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multiple Content-Type")
	})

	t.Run("rejects invalid file part Content-Type before key derivation", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="hello.txt"`)
		header.Set("Content-Type", "not-a-media-type")
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write([]byte("hello world"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		r := httptest.NewRequest(http.MethodPost, "/upload", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())

		keyFuncCalled := false
		_, err = ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: func(_ *http.Request, _ string, _ storage.ObjectMeta) (string, error) {
				keyFuncCalled = true
				return "hello.txt", nil
			},
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrValidation))
		assert.False(t, keyFuncCalled, "invalid metadata must not reach KeyFunc")
	})

	t.Run("default MaxFileSize cap rejects oversize", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		// Body is 16 bytes; cap at 5 — the auto-injected cap rejects without
		// any caller-provided Validators slice.
		body, contentType := createMultipartBody(t, "file", "big.txt", []byte("this is too long"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 5,
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrValidation))
	})

	t.Run("Unlimited opts out of cap", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "hello.txt", []byte("hello world"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: Unlimited,
		})
		require.NoError(t, err)
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
			MaxFileSize: 1 << 20,
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
			FormField:   "document",
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
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
			FormField:   "secret-token",
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no file part")
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("applies validators", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "big.txt", []byte("this is too long"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
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
		r := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not multipart secret-token")))
		r.Header.Set("Content-Type", "text/plain; token=secret-token")

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.EqualError(t, err, "storagehttp: parse multipart failed")
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("MaxBytesReader overflow is ErrValidation", func(t *testing.T) {
		t.Parallel()
		// Unit-test the classification helper: *http.MaxBytesError must wrap
		// ErrValidation so callers map it to 4xx rather than 500.
		err := wrapUploadError("storagehttp: read multipart part failed", &http.MaxBytesError{Limit: 10})
		require.Error(t, err)
		assert.ErrorIs(t, err, storage.ErrValidation)
		assert.Contains(t, err.Error(), "too large")
	})

	t.Run("returns stable error on malformed multipart part", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body := bytes.NewBufferString("--boundary\r\nsecret-token\r\n\r\nx\r\n--boundary--\r\n")
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.EqualError(t, err, "storagehttp: read multipart part failed")
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("returns stable error for skipped part byte budget", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, err := w.CreateFormField("other")
		require.NoError(t, err)
		_, err = part.Write([]byte("secret-token"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		r := httptest.NewRequest(http.MethodPost, "/upload", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())

		_, err = ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:              passthroughKeyFunc,
			MaxFileSize:          1 << 20,
			MaxTotalSkippedBytes: 3,
		})
		require.Error(t, err)
		assert.EqualError(t, err, "storagehttp: cumulative non-file part bytes exceed limit")
		assert.NotContains(t, err.Error(), "3")
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("returns stable error after too many skipped parts", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for i := 0; i < maxSkippedParts+1; i++ {
			part, err := w.CreateFormField("other")
			require.NoError(t, err)
			_, err = part.Write([]byte("x"))
			require.NoError(t, err)
		}
		require.NoError(t, w.Close())

		r := httptest.NewRequest(http.MethodPost, "/upload", &buf)
		r.Header.Set("Content-Type", w.FormDataContentType())

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.EqualError(t, err, "storagehttp: too many non-file parts")
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
			KeyFunc:     passthroughKeyFunc,
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no filename")
	})

	t.Run("returns error when KeyFunc fails", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		keyErr := errors.New("key derivation failed for secret-token")
		body, contentType := createMultipartBody(t, "file", "file.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: func(_ *http.Request, _ string, _ storage.ObjectMeta) (string, error) {
				return "", keyErr
			},
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, keyErr)
		assert.EqualError(t, err, "storagehttp: key derivation failed")
		assert.NotContains(t, err.Error(), "secret-token")
	})

	t.Run("returns error when KeyFunc panics", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		body, contentType := createMultipartBody(t, "file", "file.txt", []byte("x"))
		r := httptest.NewRequest(http.MethodPost, "/upload", body)
		r.Header.Set("Content-Type", contentType)

		_, err := ParseAndStore(ctx, r, backend, UploadOptions{
			KeyFunc: func(_ *http.Request, _ string, _ storage.ObjectMeta) (string, error) {
				panic("key derivation exploded")
			},
			MaxFileSize: 1 << 20,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "key derivation")
	})
}

func TestPartContentTypeRejectsInvalidRawHeaderValue(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]string{
		"control":      "text/plain\n",
		"nul":          "text/plain\x00",
		"invalid utf8": string([]byte{'t', 'e', 'x', 't', '/', 'p', 'l', 'a', 'i', 'n', 0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			part := &multipart.Part{
				Header: textproto.MIMEHeader{
					"Content-Type": {value},
				},
			}
			_, err := partContentType(part)
			require.Error(t, err)
		})
	}
}

func newLocalBackend(t *testing.T) *localbackend.Backend {
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

type uploadTrackingReadCloser struct {
	io.Reader
	closed *bool
}

func (r *uploadTrackingReadCloser) Close() error {
	*r.closed = true
	return nil
}

type failingPutBackend struct {
	err error
}

func (b failingPutBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return b.err
}

func (failingPutBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
}

func (failingPutBackend) Delete(context.Context, string) error {
	return nil
}

func (failingPutBackend) Exists(context.Context, string) (bool, error) {
	return false, nil
}


func TestParseAndStore_WireCapIncludesSkippedBudget(t *testing.T) {
	// MaxFileSize + MaxTotalSkippedBytes + overhead must fit legitimate
	// non-file parts within the documented skipped-parts budget.
	backend, err := localbackend.New(t.TempDir())
	require.NoError(t, err)
	// 2 MiB skipped field + 1 MiB file, MaxFileSize=1MiB, skipped budget 4MiB.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormField("note")
	require.NoError(t, err)
	_, err = fw.Write(bytes.Repeat([]byte("x"), 2<<20))
	require.NoError(t, err)
	part, err := w.CreateFormFile("file", "a.bin")
	require.NoError(t, err)
	_, err = part.Write(bytes.Repeat([]byte("y"), 1<<20))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := ParseAndStore(context.Background(), req, backend, UploadOptions{
		KeyFunc:              UUIDKeyFunc("u"),
		MaxFileSize:          1 << 20,
		MaxTotalSkippedBytes: 4 << 20,
	})
	require.NoError(t, err, "wire cap must admit MaxFileSize + MaxTotalSkippedBytes")
	assert.Equal(t, int64(1<<20), res.Size)
}
