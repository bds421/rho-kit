package compress_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/httpx/v2/middleware/compress"
)

// TestWrite_ReturnsInputLengthAcrossCommit verifies that Write never reports
// more bytes consumed than the caller handed it. The undecided->compressed
// transition flushes previously buffered bytes through the encoder; the byte
// count from that flush must not leak back to the caller, or io.Copy treats it
// as errInvalidWrite and bufio.Writer accounting corrupts.
func TestWrite_ReturnsInputLengthAcrossCommit(t *testing.T) {
	// minSize chosen so the first small write buffers and the second write
	// crosses the threshold, triggering commitCompressed with a non-empty
	// buffer that includes the first write's bytes.
	const chunk = 600
	h := compress.Middleware(compress.WithMinSize(chunk + 1))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			first := bytes.Repeat([]byte("a"), chunk)
			second := bytes.Repeat([]byte("b"), chunk)

			n1, err := w.Write(first)
			if err != nil {
				t.Errorf("first Write error: %v", err)
			}
			if n1 != len(first) {
				t.Errorf("first Write returned n=%d, want %d", n1, len(first))
			}

			n2, err := w.Write(second)
			if err != nil {
				t.Errorf("second Write error: %v", err)
			}
			if n2 != len(second) {
				t.Errorf("second Write returned n=%d, want %d (must never exceed input length)", n2, len(second))
			}
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The body must still round-trip correctly.
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip encoding, got %q", rec.Header().Get("Content-Encoding"))
	}
	got := decodeGzip(t, rec.Body.Bytes())
	want := append(bytes.Repeat([]byte("a"), chunk), bytes.Repeat([]byte("b"), chunk)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("decompressed body mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// TestWrite_IOCopyCompatibleAcrossCommit exercises the exact real-world trigger
// from the finding: a handler writes a small prefix, then io.Copy streams the
// rest across the minSize threshold. io.Copy validates that the writer never
// reports nw > nr; a buggy Write that returns the full buffer length makes
// io.Copy fail with io.ErrShortWrite / errInvalidWrite.
func TestWrite_IOCopyCompatibleAcrossCommit(t *testing.T) {
	const minSize = 64
	var copyErr error
	h := compress.Middleware(compress.WithMinSize(minSize))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			// Small prefix buffered below minSize.
			_, _ = io.WriteString(w, "prefix:")
			// Stream a body that crosses minSize during the first chunk
			// io.Copy hands to Write; that chunk's commit flushes the
			// buffered prefix too.
			src := bytes.NewReader(bytes.Repeat([]byte("z"), minSize*4))
			_, copyErr = io.Copy(w, src)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if copyErr != nil {
		t.Fatalf("io.Copy failed because Write violated the io.Writer contract: %v", copyErr)
	}
	got := decodeGzip(t, rec.Body.Bytes())
	want := append([]byte("prefix:"), bytes.Repeat([]byte("z"), minSize*4)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("body mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// TestWrite_LargeSingleWriteCompresses verifies that a single write that
// already exceeds minSize is compressed even when it also exceeds maxBuffer.
// Such a write needs no buffering — its size is already known to clear the
// threshold — so bailing to passthrough wastes the compression opportunity.
func TestWrite_LargeSingleWriteCompresses(t *testing.T) {
	// Single write larger than maxBuffer but well above minSize.
	body := bytes.Repeat([]byte("a"), 300<<10) // 300 KiB
	h := compress.Middleware(
		compress.WithMinSize(compress.DefaultMinSize), // 1 KiB
		compress.WithMaxBuffer(256<<10),               // 256 KiB (default)
	)(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("single write above minSize should compress, got Content-Encoding=%q",
			rec.Header().Get("Content-Encoding"))
	}
	got := decodeGzip(t, rec.Body.Bytes())
	if !bytes.Equal(got, body) {
		t.Fatalf("decompressed body mismatch: got %d bytes, want %d", len(got), len(body))
	}
}

// TestMiddleware_PanicDoesNotCommitResponse verifies that when the wrapped
// handler panics before any bytes are written, the deferred finalize() does
// NOT commit a 200 OK to the underlying writer. An outer recover middleware
// (which sits outside compress in the kit stack) must still be able to write a
// 500. We emulate the recover boundary with our own deferred recover that
// checks whether compress already wrote a header.
func TestMiddleware_PanicDoesNotCommitResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapped := compress.Middleware()(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			panic("boom before any write")
		}),
	)

	// Emulate the outer recover middleware boundary: it observes the writer
	// after the inner stack (compress + its deferred finalize) has unwound.
	rw := &observingWriter{ResponseWriter: rec}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic to propagate past compress middleware")
			}
			// At this point compress's deferred finalize() has already run.
			// If it committed a status, the recover boundary can no longer
			// send a clean 500 — the client would receive that committed
			// status instead.
			if rw.wroteHeader {
				t.Fatalf("compress committed status %d during panic unwind; "+
					"outer recover can no longer send 500", rw.status)
			}
		}()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		wrapped.ServeHTTP(rw, req)
	}()
}

// observingWriter records whether WriteHeader/Write reached the underlying
// writer, mirroring how the recover middleware's recordingWriter detects an
// already-started response.
type observingWriter struct {
	http.ResponseWriter
	wroteHeader bool
	status      int
}

func (o *observingWriter) WriteHeader(code int) {
	if !o.wroteHeader {
		o.wroteHeader = true
		o.status = code
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *observingWriter) Write(p []byte) (int, error) {
	if !o.wroteHeader {
		o.wroteHeader = true
		o.status = http.StatusOK
	}
	return o.ResponseWriter.Write(p)
}
