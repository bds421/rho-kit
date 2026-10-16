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
// Only the first non-interim call takes effect; subsequent calls are no-ops.
//
// Invalid status codes (outside 100..999) are mapped to 500 BEFORE
// delegating so the recorder cannot trigger the stdlib panic that
// would unwind the handler chain. Wave 68 closed a hostile-review
// finding for this surface: a buggy handler returning a stale int
// would otherwise crash the request after the recorder had already
// updated its internal state.
//
// 1xx interim responses (100..199, e.g. 103 Early Hints) are forwarded
// to the underlying writer but do NOT latch the recorder: net/http
// permits any number of interim responses followed by the single final
// status. Latching on a 1xx code would swallow the final WriteHeader and
// cause Status() to report the interim code, corrupting metrics and logs.
func (r *ResponseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	if code < 100 || code > 999 {
		code = http.StatusInternalServerError
	}
	// Interim 1xx responses do not commit the final status. Forward them
	// to the underlying writer and keep waiting for the final code.
	if code >= 100 && code < 200 {
		r.ResponseWriter.WriteHeader(code)
		return
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

// Flush retains http.Flusher compatibility. Use http.ResponseController.Flush
// when the caller needs the underlying flush error.
func (r *ResponseRecorder) Flush() {
	_ = r.FlushError()
}

// FlushError preserves the native error and records a supported flush before
// delegation: even a failed or panicking flush may already have committed the
// response. Unsupported writers do not commit a header. An explicit final status
// remains unchanged; an implicit commit keeps the recorder's default 200.
//
//nolint:wrapcheck // native flush error identity is preserved by design
func (r *ResponseRecorder) FlushError() error {
	if !SupportsNativeFlush(r.ResponseWriter) {
		return http.ErrNotSupported
	}
	r.wroteHeader = true
	return http.NewResponseController(r.ResponseWriter).Flush()
}

// SupportsNativeFlush follows error-aware and legacy flush capabilities through
// transparent wrappers. Kit recorders report the underlying capability instead
// of their unconditional forwarding methods; a nil-Unwrap buffering barrier with
// only void Flush is unsupported. A functional FlushError is never skipped for
// Unwrap. This performs no I/O and does not promise wire delivery through other
// buffering wrappers.
func SupportsNativeFlush(w http.ResponseWriter) bool {
	for {
		switch writer := w.(type) {
		case interface{ SupportsNativeFlush() bool }:
			return writer.SupportsNativeFlush()
		case interface{ FlushError() error }:
			// A functional error-reporting wrapper owns its flush operation;
			// ResponseController prefers it to both Flusher and Unwrap.
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			inner := writer.Unwrap()
			// The buffering timeout deliberately hides its native writer.
			if inner == nil {
				return false
			}
			if _, ok := w.(http.Flusher); ok {
				return true
			}
			w = inner
		case http.Flusher:
			return true
		default:
			return false
		}
	}
}

// SupportsNativeFlush reports the wrapped writer's capability without treating
// this recorder's unconditional forwarding methods as a native implementation.
func (r *ResponseRecorder) SupportsNativeFlush() bool { return SupportsNativeFlush(r.ResponseWriter) }

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
