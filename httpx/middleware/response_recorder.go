package middleware

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
)

// ResponseRecorder wraps http.ResponseWriter to capture the HTTP status code.
// It guards against double WriteHeader calls and supports Flush, Hijack, Push,
// ReadFrom, and Unwrap for compatibility with streaming, WebSocket,
// http.ResponseController, HTTP/2 server push, and large-file transfer paths.
type ResponseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	hijacked    bool
}

// Status returns the recorded HTTP status code (default 200).
func (r *ResponseRecorder) Status() int { return r.statusCode }

// WroteHeader reports whether the handler has started the HTTP response.
func (r *ResponseRecorder) WroteHeader() bool { return r.wroteHeader }

// WasHijacked returns true if the connection was hijacked (e.g., WebSocket upgrade).
func (r *ResponseRecorder) WasHijacked() bool { return r.hijacked }

// NewResponseRecorder creates a ResponseRecorder with a default 200 status.
func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
	return &ResponseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

// WriteHeader records the status code and delegates to the underlying writer.
// Only the first call takes effect; subsequent calls are no-ops.
//
// Invalid status codes (outside 100..999) are mapped to 500 BEFORE
// delegating so the recorder cannot trigger the stdlib panic that
// would unwind the handler chain. Wave 68 closed a hostile-review
// finding for this surface: a buggy handler returning a stale int
// would otherwise crash the request after the recorder had already
// updated its internal state.
func (r *ResponseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	if code < 100 || code > 999 {
		code = http.StatusInternalServerError
	}
	r.statusCode = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

// Write delegates to the underlying writer, implicitly sending a 200 header
// if WriteHeader has not been called (matching net/http behaviour).
func (r *ResponseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		// Make the implicit 200 explicit, matching net/http's behavior.
		// This ensures the recorder and underlying writer have consistent state.
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Flush delegates to the underlying writer if it implements http.Flusher.
func (r *ResponseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker by delegating to the underlying ResponseWriter.
// Required for WebSocket upgrades (gorilla/websocket asserts http.Hijacker).
func (r *ResponseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		conn, rw, err := h.Hijack()
		if err == nil {
			r.hijacked = true
		}
		return conn, rw, err
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// Unwrap returns the underlying ResponseWriter for http.ResponseController compatibility.
func (r *ResponseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Push delegates to the underlying writer when it implements [http.Pusher].
func (r *ResponseRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := r.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// ReadFrom delegates to the underlying writer's optimized copy path when
// available, while still recording the implicit 200 status.
func (r *ResponseRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{r.ResponseWriter}, src)
}

type writerOnly struct{ io.Writer }
