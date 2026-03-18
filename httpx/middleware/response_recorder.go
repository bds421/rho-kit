package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

// ResponseRecorder wraps http.ResponseWriter to capture the HTTP status code.
// It guards against double WriteHeader calls and supports Flush, Hijack, and
// Unwrap for compatibility with streaming, WebSocket, and http.ResponseController.
type ResponseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	hijacked    bool
}

// Status returns the recorded HTTP status code (default 200).
func (r *ResponseRecorder) Status() int { return r.statusCode }

// WasHijacked returns true if the connection was hijacked (e.g., WebSocket upgrade).
func (r *ResponseRecorder) WasHijacked() bool { return r.hijacked }

// NewResponseRecorder creates a ResponseRecorder with a default 200 status.
func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
	return &ResponseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

// WriteHeader records the status code and delegates to the underlying writer.
// Only the first call takes effect; subsequent calls are no-ops.
func (r *ResponseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
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
