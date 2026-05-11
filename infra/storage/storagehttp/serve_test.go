package storagehttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestServeFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("rejects nil backend", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/files/foo.txt", nil)

		err := ServeFile(w, r, nil, "foo.txt", ServeOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backend is required")
	})

	t.Run("rejects nil response writer before opening object", func(t *testing.T) {
		t.Parallel()
		backend := &countingGetBackend{}
		r := httptest.NewRequest(http.MethodGet, "/files/foo.txt", nil)

		err := ServeFile(nil, r, backend, "foo.txt", ServeOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "response writer is required")
		assert.Equal(t, 0, backend.gets)
	})

	t.Run("rejects nil request before opening object", func(t *testing.T) {
		t.Parallel()
		backend := &countingGetBackend{}
		w := httptest.NewRecorder()

		err := ServeFile(w, nil, backend, "foo.txt", ServeOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request is required")
		assert.Equal(t, 0, backend.gets)
	})

	t.Run("serves file with correct headers", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		require.NoError(t, backend.Put(ctx, "report.pdf", bytes.NewReader([]byte("pdf content")), storage.ObjectMeta{
			ContentType: "application/pdf",
		}))

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/files/report.pdf", nil)

		err := ServeFile(w, r, backend, "report.pdf", ServeOptions{})
		require.NoError(t, err)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, "application/pdf", resp.Header.Get("Content-Type"))
		assert.Contains(t, resp.Header.Get("Content-Disposition"), "inline")
		assert.Contains(t, resp.Header.Get("Content-Disposition"), "filename=")
		assert.Contains(t, resp.Header.Get("Content-Disposition"), "report.pdf")
	})

	t.Run("attachment disposition forces download", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		require.NoError(t, backend.Put(ctx, "data.csv", bytes.NewReader([]byte("a,b,c")), storage.ObjectMeta{}))

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/download", nil)

		err := ServeFile(w, r, backend, "data.csv", ServeOptions{
			ContentDisposition: "attachment",
			Filename:           "export.csv",
		})
		require.NoError(t, err)

		assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
		assert.Contains(t, w.Header().Get("Content-Disposition"), "export.csv")
	})

	t.Run("sets Cache-Control when specified", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		require.NoError(t, backend.Put(ctx, "icon.png", bytes.NewReader([]byte("png")), storage.ObjectMeta{}))

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/icon.png", nil)

		err := ServeFile(w, r, backend, "icon.png", ServeOptions{
			CacheControl: "public, max-age=3600",
		})
		require.NoError(t, err)

		assert.Equal(t, "public, max-age=3600", w.Header().Get("Cache-Control"))
	})

	t.Run("rejects invalid Cache-Control before opening object", func(t *testing.T) {
		t.Parallel()
		backend := &countingGetBackend{}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/download", nil)

		err := ServeFile(w, r, backend, "file.txt", ServeOptions{
			CacheControl: "public\r\nX-Evil: injected",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Cache-Control")
		assert.Equal(t, 0, backend.gets)
		assert.Empty(t, w.Header().Values("X-Evil"))
	})

	t.Run("rejects non-ascii Cache-Control before opening object", func(t *testing.T) {
		t.Parallel()
		for name, value := range map[string]string{
			"del":          "public\x7f",
			"invalid utf8": string([]byte{'p', 'u', 'b', 'l', 'i', 'c', 0xff}),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				backend := &countingGetBackend{}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/download", nil)

				err := ServeFile(w, r, backend, "file.txt", ServeOptions{
					CacheControl: value,
				})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "Cache-Control")
				assert.Equal(t, 0, backend.gets)
			})
		}
	})

	t.Run("returns ErrObjectNotFound for missing key", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/missing", nil)

		err := ServeFile(w, r, backend, "nonexistent.txt", ServeOptions{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, storage.ErrObjectNotFound))
	})

	t.Run("supports Range requests with local backend", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		content := []byte("0123456789")
		require.NoError(t, backend.Put(ctx, "range.txt", bytes.NewReader(content), storage.ObjectMeta{}))

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/range.txt", nil)
		r.Header.Set("Range", "bytes=2-5")

		err := ServeFile(w, r, backend, "range.txt", ServeOptions{})
		require.NoError(t, err)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
		assert.Equal(t, "bytes 2-5/10", resp.Header.Get("Content-Range"))
	})

	t.Run("derives filename from nested key", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		require.NoError(t, backend.Put(ctx, "a/b/c/deep.txt", bytes.NewReader([]byte("deep")), storage.ObjectMeta{}))

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/deep", nil)

		err := ServeFile(w, r, backend, "a/b/c/deep.txt", ServeOptions{})
		require.NoError(t, err)

		assert.Contains(t, w.Header().Get("Content-Disposition"), "deep.txt")
	})

	t.Run("sanitizes override filename for Content-Disposition", func(t *testing.T) {
		t.Parallel()
		backend := newLocalBackend(t)
		require.NoError(t, backend.Put(ctx, "safe/report.txt", bytes.NewReader([]byte("report")), storage.ObjectMeta{}))

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/download", nil)

		err := ServeFile(w, r, backend, "safe/report.txt", ServeOptions{
			Filename: "../secret\r\nX-Evil: injected.txt",
		})
		require.NoError(t, err)

		cd := w.Header().Get("Content-Disposition")
		assert.Contains(t, cd, "secretX-Evil: injected.txt")
		assert.NotContains(t, cd, "\r")
		assert.NotContains(t, cd, "\n")
		assert.Empty(t, w.Header().Values("X-Evil"))
	})

	t.Run("streams non-seekable reader without Range support", func(t *testing.T) {
		t.Parallel()
		content := []byte("streamed content")
		backend := &nonSeekableBackend{
			data:        content,
			contentType: "text/plain",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/stream", nil)

		err := ServeFile(w, r, backend, "stream.txt", ServeOptions{})
		require.NoError(t, err)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/plain", resp.Header.Get("Content-Type"))

		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, content, body)
	})

	t.Run("falls back when stored content type is invalid", func(t *testing.T) {
		t.Parallel()
		backend := &nonSeekableBackend{
			data:        []byte(`{"ok":true}`),
			contentType: "text/plain\r\nX-Evil: injected",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/download", nil)

		err := ServeFile(w, r, backend, "report.json", ServeOptions{})
		require.NoError(t, err)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		assert.Empty(t, resp.Header.Values("X-Evil"))
	})

	t.Run("invalid stored content type without extension becomes octet stream", func(t *testing.T) {
		t.Parallel()
		backend := &nonSeekableBackend{
			data:        []byte("data"),
			contentType: "not-a-media-type",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/download", nil)

		err := ServeFile(w, r, backend, "object", ServeOptions{})
		require.NoError(t, err)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	})
}

// nonSeekableBackend returns a non-seekable io.ReadCloser from Get,
// exercising the streaming fallback path in ServeFile.
type nonSeekableBackend struct {
	data        []byte
	contentType string
}

func (b *nonSeekableBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}

func (b *nonSeekableBackend) Get(_ context.Context, _ string) (io.ReadCloser, storage.ObjectMeta, error) {
	// io.NopCloser wraps a Reader, NOT a ReadSeeker, so the streaming path is used.
	return io.NopCloser(bytes.NewReader(b.data)), storage.ObjectMeta{
		ContentType: b.contentType,
		Size:        int64(len(b.data)),
	}, nil
}

func (b *nonSeekableBackend) Delete(context.Context, string) error         { return nil }
func (b *nonSeekableBackend) Exists(context.Context, string) (bool, error) { return false, nil }

type countingGetBackend struct {
	gets int
}

func (b *countingGetBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return nil
}

func (b *countingGetBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	b.gets++
	return io.NopCloser(bytes.NewReader([]byte("data"))), storage.ObjectMeta{}, nil
}

func (b *countingGetBackend) Delete(context.Context, string) error { return nil }
func (b *countingGetBackend) Exists(context.Context, string) (bool, error) {
	return false, nil
}

func TestServeFile_ETag(t *testing.T) {
	t.Parallel()

	t.Run("sets ETag header", func(t *testing.T) {
		t.Parallel()
		backend := &nonSeekableBackend{
			data:        []byte("content"),
			contentType: "text/plain",
		}
		// Override Get to include ETag.
		etagBackend := &etagBackend{
			nonSeekableBackend: backend,
			etag:               "abc123",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/file", nil)

		err := ServeFile(w, r, etagBackend, "file.txt", ServeOptions{})
		require.NoError(t, err)

		assert.Equal(t, `"abc123"`, w.Header().Get("ETag"))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("returns 304 on If-None-Match", func(t *testing.T) {
		t.Parallel()
		backend := &etagBackend{
			nonSeekableBackend: &nonSeekableBackend{
				data:        []byte("content"),
				contentType: "text/plain",
			},
			etag: "abc123",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/file", nil)
		r.Header.Set("If-None-Match", `"abc123"`)

		err := ServeFile(w, r, backend, "file.txt", ServeOptions{})
		require.NoError(t, err)

		assert.Equal(t, http.StatusNotModified, w.Code)
		assert.Empty(t, w.Body.Bytes())
	})

	t.Run("returns 304 on matching second If-None-Match line", func(t *testing.T) {
		t.Parallel()
		backend := &etagBackend{
			nonSeekableBackend: &nonSeekableBackend{
				data:        []byte("content"),
				contentType: "text/plain",
			},
			etag: "abc123",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/file", nil)
		r.Header.Add("If-None-Match", `"different"`)
		r.Header.Add("If-None-Match", `"abc123"`)

		err := ServeFile(w, r, backend, "file.txt", ServeOptions{})
		require.NoError(t, err)

		assert.Equal(t, http.StatusNotModified, w.Code)
		assert.Empty(t, w.Body.Bytes())
	})

	t.Run("serves body on ETag mismatch", func(t *testing.T) {
		t.Parallel()
		backend := &etagBackend{
			nonSeekableBackend: &nonSeekableBackend{
				data:        []byte("content"),
				contentType: "text/plain",
			},
			etag: "abc123",
		}

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/file", nil)
		r.Header.Set("If-None-Match", `"different"`)

		err := ServeFile(w, r, backend, "file.txt", ServeOptions{})
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, []byte("content"), w.Body.Bytes())
	})
}

// etagBackend wraps nonSeekableBackend but adds an ETag to ObjectMeta.
type etagBackend struct {
	*nonSeekableBackend
	etag string
}

func (b *etagBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	rc, meta, err := b.nonSeekableBackend.Get(ctx, key)
	if err != nil {
		return nil, meta, err
	}
	meta.ETag = b.etag
	return rc, meta, nil
}
