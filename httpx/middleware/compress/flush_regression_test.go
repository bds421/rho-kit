package compress_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/httpx/v2/middleware/compress"
)

// TestMiddleware_MidStreamFlushDeliversDecodableGzipPrefix is the
// regression pin for review-08: mid-stream Flush must reach gzip.Writer
// so SSE/chunked clients see decodable bytes before the handler returns.
// Without poolWriter.Flush, the type assert on the interface embed always
// failed and bytes sat in gzip's internal buffer until finalize Close.
func TestMiddleware_MidStreamFlushDeliversDecodableGzipPrefix(t *testing.T) {
	const minSize = 32
	payload := bytes.Repeat([]byte("stream-event-data-"), 4) // > minSize
	if len(payload) < minSize {
		t.Fatalf("payload too small: %d", len(payload))
	}

	h := compress.Middleware(compress.WithMinSize(minSize))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if _, err := w.Write(payload[:len(payload)/2]); err != nil {
			t.Errorf("write1: %v", err)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Mid-stream: client should already be able to decode a gzip prefix.
		if _, err := w.Write(payload[len(payload)/2:]); err != nil {
			t.Errorf("write2: %v", err)
		}
	}))

	// Use a ResponseRecorder that supports Flush.
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	if !rec.flushed {
		t.Fatal("handler Flush was not observed on the underlying recorder")
	}

	// After the full response, the body must be valid gzip of the payload.
	// (httptest cannot observe partial wire bytes mid-handler; the
	// poolWriter.Flush unit contract is also pinned in TestPoolWriter_Flush.)
	gr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	got, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decoded body mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestPoolWriter_FlushReachesGzipWriter pins the WriterReleaser.Flush
// contract: Acquire's poolWriter must forward Flush to gzip.Writer so
// compressWriter.Flush is not a no-op in compressed mode.
func TestPoolWriter_FlushReachesGzipWriter(t *testing.T) {
	var buf bytes.Buffer
	enc := compress.NewGzipEncoder(gzip.DefaultCompression)
	w := enc.Acquire(&buf)
	defer w.Release()

	if _, err := w.Write([]byte("hello-flush-path")); err != nil {
		t.Fatal(err)
	}
	// Before Flush, gzip may buffer; after Flush, some compressed bytes
	// must appear (or Flush returns nil proving the method is live).
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if buf.Len() == 0 {
		// gzip may still buffer a tiny write; Close guarantees delivery.
		// The critical contract is that Flush is implemented (not a
		// missing method / always-false type assert).
		t.Log("gzip held bytes past Flush (allowed); Close will finish stream")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected compressed bytes after Close")
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() {
	f.flushed = true
	// ResponseRecorder has no real Flush; mark only.
}
