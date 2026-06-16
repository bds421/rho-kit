package compress_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/httpx/v2/middleware/compress"
)

func newHandler(body []byte, headers http.Header) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		_, _ = w.Write(body)
	})
}

func decodeGzip(t *testing.T, b []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	return out
}

func TestMiddleware_CompressesAboveMinSize(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatalf("Content-Length should be cleared when compressing")
	}
	if !containsVary(rec.Header(), "Accept-Encoding") {
		t.Fatalf("Vary header missing Accept-Encoding")
	}
	got := decodeGzip(t, rec.Body.Bytes())
	if !bytes.Equal(got, body) {
		t.Fatalf("decompressed body mismatch")
	}
}

func TestMiddleware_PassesThroughBelowMinSize(t *testing.T) {
	body := []byte("tiny")
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding should be unset for sub-min-size body")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body changed unexpectedly: %q", rec.Body.String())
	}
	// Vary still set even on passthrough so downstream caches behave.
	if !containsVary(rec.Header(), "Accept-Encoding") {
		t.Fatalf("Vary header missing on passthrough")
	}
}

func TestMiddleware_NoAcceptEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding set despite missing Accept-Encoding")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body mutated")
	}
}

func TestMiddleware_BinaryContentTypePassesThrough(t *testing.T) {
	body := bytes.Repeat([]byte{0xFF}, 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"image/png"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("binary type should not be compressed")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body mutated")
	}
}

func TestMiddleware_RangeRequestPassesThrough(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-100")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("range request should not be compressed")
	}
}

func TestMiddleware_HEADRequestPassesThrough(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("HEAD response should not be compressed")
	}
}

func TestMiddleware_AlreadyEncodedPassesThrough(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{
		"Content-Type":     {"text/plain"},
		"Content-Encoding": {"br"},
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br (preserved)", got)
	}
}

func TestMiddleware_NoTransformHonored(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{
		"Content-Type":  {"text/plain"},
		"Cache-Control": {"no-transform"},
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("no-transform should disable compression")
	}
}

func TestMiddleware_AcceptEncodingQValue(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// q=0 explicitly disables gzip; no other supported encoding offered.
	req.Header.Set("Accept-Encoding", "gzip;q=0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("q=0 must disable gzip")
	}
}

func TestMiddleware_WildcardAcceptEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("wildcard should accept gzip")
	}
}

// TestMiddleware_WildcardDoesNotSelectRefusedEncoding verifies RFC 9110
// §12.5.3: '*' matches codings NOT explicitly mentioned, so a coding the
// client refused with q=0 must not be selected via the wildcard.
func TestMiddleware_WildcardDoesNotSelectRefusedEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// gzip is the only registered encoder; refusing it with q=0 means the
	// wildcard has nothing left to match, so the response must be uncompressed.
	req.Header.Set("Accept-Encoding", "gzip;q=0, *")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("wildcard must not select the q=0-refused gzip, got Content-Encoding=%q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body mismatch: got %d bytes, want %d", rec.Body.Len(), len(body))
	}
}

// TestMiddleware_AcceptEncodingAcrossMultipleHeaderLines verifies that an
// Accept-Encoding list split across multiple field lines is parsed as one
// list, so a gzip preference on a later line is still honored.
func TestMiddleware_AcceptEncodingAcrossMultipleHeaderLines(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// br is unsupported; gzip arrives on a separate field line.
	req.Header.Add("Accept-Encoding", "br")
	req.Header.Add("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("gzip on a second Accept-Encoding line must negotiate, got %q", got)
	}
}

func TestMiddleware_ETagDegradedToWeak(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{
		"Content-Type": {"text/plain"},
		"ETag":         {`"abc123"`},
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("ETag"); !strings.HasPrefix(got, "W/") {
		t.Fatalf("ETag should be weak after compression, got %q", got)
	}
}

func TestMiddleware_VaryDeduplicates(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Values("Vary"); len(got) != 1 {
		t.Fatalf("expected one Vary header, got %v", got)
	}
}

// hijackableRecorder is a httptest.ResponseRecorder that also implements
// http.Hijacker so we can test the WebSocket-upgrade passthrough path.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, errors.New("hijack: not a real connection")
}

func TestMiddleware_HijackPassesThrough(t *testing.T) {
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := compress.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("wrapper did not expose Hijacker")
		}
		_, _, _ = hj.Hijack()
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)

	if !rec.hijacked {
		t.Fatalf("Hijack was not forwarded to underlying writer")
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Hijack path should not set Content-Encoding")
	}
}

// TestMiddleware_WriteAfterHijackReturnsErrHijacked verifies that a buggy
// handler writing after hijacking gets http.ErrHijacked (net/http's contract)
// instead of a nil-pointer panic from dereferencing the released buffers.
func TestMiddleware_WriteAfterHijackReturnsErrHijacked(t *testing.T) {
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	var writeErr error
	var flushPanicked bool
	h := compress.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		hj := w.(http.Hijacker)
		_, _, _ = hj.Hijack()
		// Writing after hijack must not panic; it must report ErrHijacked.
		_, writeErr = w.Write([]byte("late"))
		// Flush after hijack must be a safe no-op.
		func() {
			defer func() {
				if r := recover(); r != nil {
					flushPanicked = true
				}
			}()
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}()
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("write after hijack panicked instead of returning ErrHijacked: %v", r)
		}
	}()
	h.ServeHTTP(rec, req)

	if !errors.Is(writeErr, http.ErrHijacked) {
		t.Fatalf("write after hijack = %v, want http.ErrHijacked", writeErr)
	}
	if flushPanicked {
		t.Fatal("flush after hijack panicked")
	}
}

func TestMiddleware_BufferCeilingBailsToPassthrough(t *testing.T) {
	// Body larger than the test's MaxBuffer; verify uncompressed bytes
	// arrive intact even though we tried to compress at first.
	body := bytes.Repeat([]byte("x"), 4096)
	h := compress.Middleware(
		compress.WithMinSize(8192),
		compress.WithMaxBuffer(1024), // smaller than body — forces bail
	)(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatalf("ceiling bail should have prevented compression")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("ceiling bail produced %d bytes, want %d", rec.Body.Len(), len(body))
	}
}

func TestSelectEncoder_NoOverlapReturnsIdentity(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware()(newHandler(body, http.Header{"Content-Type": {"text/plain"}}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("non-overlapping encoding should pass through, got %q", rec.Header().Get("Content-Encoding"))
	}
}

func TestMiddleware_WithoutGzip(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	h := compress.Middleware(compress.WithoutGzip())(
		newHandler(body, http.Header{"Content-Type": {"text/plain"}}),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("WithoutGzip should leave gzip Accept-Encoding unhandled")
	}
}

func TestMiddleware_FlusherCommitsCompressed(t *testing.T) {
	h := compress.Middleware(compress.WithMinSize(4))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello world this is enough"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}),
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Flush above MinSize should commit compressed")
	}
}

func TestMiddleware_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil option")
		}
	}()
	_ = compress.Middleware(nil)
}

func TestWithGzipLevel_PanicsOnOutOfRange(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on out-of-range gzip level")
		}
	}()
	_ = compress.WithGzipLevel(99)
}

func containsVary(h http.Header, value string) bool {
	for _, v := range h.Values("Vary") {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return true
			}
		}
	}
	return false
}
