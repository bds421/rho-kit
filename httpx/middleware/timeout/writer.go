package timeout

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
)

// ErrResponseTooLarge is returned by Write when the buffered response
// exceeds the maximum buffer size configured for this middleware
// (default 1 MiB, see [defaultMaxBufferSize]; override with
// [WithMaxBufferSize]). This prevents silent data loss — callers can
// detect truncation and stop writing.
var ErrResponseTooLarge = errors.New("timeout: response exceeds buffer limit")

// timeoutWriter buffers the handler's response so we can discard it if the
// timeout fires first. Only one of writeToReal (success) or writeTimeout
// writes to the real ResponseWriter.

// defaultMaxBufferSize is the default per-request response buffer cap.
// Lowered from 10 MiB to 1 MiB: under a thundering-herd of timing-out
// requests, the previous cap let 10k concurrent attackers hold 100 GiB of
// transient memory. Production callers expecting larger response payloads
// should pair Timeout with body-size limits and override via
// [WithMaxBufferSize].
const defaultMaxBufferSize = 1 << 20 // 1 MiB

type timeoutWriter struct {
	w http.ResponseWriter

	mu          sync.Mutex
	h           http.Header
	code        int
	buf         []byte
	wroteHeader bool
	written     bool
	flushWarned bool
	truncated  bool // body was cut short by bufferCap; drop Content-Length on flush

	maxBuffer int          // per-instance cap; 0 falls back to defaultMaxBufferSize
	logger    *slog.Logger // configured logger; nil falls back to slog.Default
}

// warnLogger returns the configured logger, falling back to slog.Default so
// the Flush misconfiguration signal is never silently dropped. This mirrors
// the late-panic fallback in Timeout (cfg.logger / drainLateHandler): a
// service that wires a structured non-default logger via WithLogger sees the
// SSE/streaming warning on the same sink as its late-panic records.
func (tw *timeoutWriter) warnLogger() *slog.Logger {
	if tw.logger != nil {
		return tw.logger
	}
	return slog.Default()
}

func (tw *timeoutWriter) bufferCap() int {
	if tw.maxBuffer > 0 {
		return tw.maxBuffer
	}
	return defaultMaxBufferSize
}

// Header returns the buffered header map.
//
// The map itself is not mutex-protected (matching the http.ResponseWriter
// contract that Header/Write/WriteHeader must not be called concurrently
// from user code). However, the timeout middleware creates concurrency
// between the handler goroutine and the timeout goroutine. To handle this
// safely, writeToReal and writeTimeout snapshot the headers under the mutex
// before accessing them, ensuring no concurrent map read/write.
func (tw *timeoutWriter) Header() http.Header {
	return tw.h
}

// WriteHeader buffers the status code for later flushing. No-op after timeout.
//
// This mirrors the http.ResponseWriter contract enforced by the stdlib: the
// first WriteHeader latches the final status and later calls are superfluous
// no-ops. 1xx informational codes do not latch a final status — the buffered
// writer cannot send them early, so they are dropped, leaving a subsequent
// Write/WriteHeader to set the real status (defaulting to 200). This keeps
// handler behaviour identical inside and outside the middleware.
func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return
	}
	if code < 100 || code > 999 {
		// Sibling buffered writers map invalid codes to 500 rather than
		// panicking the handler goroutine; stay consistent with them.
		code = http.StatusInternalServerError
	}
	// 1xx informational responses are not the final status; ignore them so a
	// later WriteHeader/Write can still set the real status code.
	if code < 200 {
		return
	}
	if tw.wroteHeader {
		return
	}
	tw.wroteHeader = true
	tw.code = code
}

// Write buffers response data. Returns ErrHandlerTimeout after timeout.
func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return 0, http.ErrHandlerTimeout
	}
	// Mirror the stdlib: the first Write implies WriteHeader(StatusOK) and
	// latches the final status, so any later WriteHeader call is a no-op.
	if !tw.wroteHeader {
		tw.wroteHeader = true
		if tw.code == 0 {
			tw.code = http.StatusOK
		}
	}
	remaining := tw.bufferCap() - len(tw.buf)
	if remaining <= 0 {
		tw.truncated = true
		return 0, ErrResponseTooLarge
	}
	if len(b) > remaining {
		tw.buf = append(tw.buf, b[:remaining]...)
		tw.truncated = true
		return remaining, ErrResponseTooLarge
	}
	tw.buf = append(tw.buf, b...)
	return len(b), nil
}

// writeToReal flushes the buffered response to the real ResponseWriter.
// Headers are snapshot under the mutex to prevent concurrent map read/write
// with the handler goroutine that may still be calling Header().Set().
func (tw *timeoutWriter) writeToReal() {
	tw.mu.Lock()
	if tw.written {
		tw.mu.Unlock()
		return
	}
	tw.written = true

	// Snapshot headers and buffered data under the lock, then release
	// before writing to the real writer (which may be slow).
	hdrs := make(http.Header, len(tw.h))
	for k, v := range tw.h {
		cp := make([]string, len(v))
		copy(cp, v)
		hdrs[k] = cp
	}
	code := tw.code
	if code == 0 {
		code = http.StatusOK
	}
	buf := tw.buf
	truncated := tw.truncated
	tw.mu.Unlock()

	for k, v := range hdrs {
		tw.w.Header()[k] = v
	}
	// Handler-set Content-Length is wrong when Write truncated the body.
	// Drop it so net/http derives length from the bytes actually sent.
	if truncated {
		tw.w.Header().Del("Content-Length")
	}
	tw.w.WriteHeader(code)
	if len(buf) > 0 {
		_, _ = tw.w.Write(buf)
	}
}

// Unwrap always returns nil.
//
// Returning the raw ResponseWriter would let a handler goroutine reach the
// real connection via http.ResponseController (Hijack / SetWriteDeadline)
// while the middleware goroutine may concurrently call writeTimeout on the
// same writer — a data race on net/http response internals. Routes that
// legitimately hijack or stream (WebSocket, SSE) must bypass the timeout
// via [WithWebSocketUpgradeBypass] or by not wrapping the route.
func (tw *timeoutWriter) Unwrap() http.ResponseWriter {
	return nil
}

// Flush implements http.Flusher. Since the timeout writer buffers the response,
// Flush is a no-op — flushing the buffer before the timeout decision is made
// would defeat the purpose of the timeout guard. SSE endpoints should bypass
// the timeout middleware entirely (like WebSocket endpoints).
func (tw *timeoutWriter) Flush() {
	// Intentionally a no-op. The buffer is flushed to the real writer only
	// when writeToReal is called (handler completed before timeout).
	tw.mu.Lock()
	if !tw.flushWarned {
		tw.flushWarned = true
		logger := tw.warnLogger()
		tw.mu.Unlock()
		logger.Warn("timeout middleware: Flush() called but is a no-op; SSE/streaming endpoints should bypass the timeout middleware")
		return
	}
	tw.mu.Unlock()
}

// writeTimeout writes the JSON timeout response if the handler hasn't already
// written a response.
func (tw *timeoutWriter) writeTimeout() {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.written {
		return
	}
	tw.written = true

	tw.w.Header().Set("Content-Type", "application/json")
	tw.w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = tw.w.Write([]byte(`{"error":"request timeout","code":"TIMEOUT"}` + "\n"))
}
