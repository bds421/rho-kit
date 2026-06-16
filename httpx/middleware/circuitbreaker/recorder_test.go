package circuitbreaker_test

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cbmw "github.com/bds421/rho-kit/httpx/v2/middleware/circuitbreaker"
	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
)

// capWriter is a test http.ResponseWriter that also implements
// http.Flusher, http.Hijacker, http.Pusher, and io.ReaderFrom so we
// can confirm the middleware's status recorder forwards those
// optional interfaces to the underlying writer (matching the sibling
// middleware.ResponseRecorder contract). A bare httptest.ResponseRecorder
// only implements Flusher, so we need a richer fake for Hijack/Push/ReadFrom.
type capWriter struct {
	header     http.Header
	body       bytes.Buffer
	status     int
	writes     [][]byte
	flushed    bool
	hijacked   bool
	pushTarget string
	readFromN  int64
}

func newCapWriter() *capWriter {
	return &capWriter{header: make(http.Header)}
}

func (c *capWriter) Header() http.Header { return c.header }

func (c *capWriter) Write(b []byte) (int, error) {
	cp := append([]byte(nil), b...)
	c.writes = append(c.writes, cp)
	return c.body.Write(b)
}

func (c *capWriter) WriteHeader(code int) { c.status = code }

func (c *capWriter) Flush() { c.flushed = true }

func (c *capWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c.hijacked = true
	return nil, nil, nil
}

func (c *capWriter) Push(target string, _ *http.PushOptions) error {
	c.pushTarget = target
	return nil
}

func (c *capWriter) ReadFrom(src io.Reader) (int64, error) {
	n, err := c.body.ReadFrom(src)
	c.readFromN += n
	return n, err
}

// TestMiddleware_ForwardsFlusher confirms a handler behind the breaker
// can assert http.Flusher (typical for SSE) and the flush reaches the
// underlying writer rather than being silently dropped.
func TestMiddleware_ForwardsFlusher(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, time.Minute)
	var sawFlusher bool
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			f.Flush()
		}
	}))

	w := newCapWriter()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, sawFlusher, "handler must be able to assert http.Flusher through the recorder")
	require.True(t, w.flushed, "Flush must reach the underlying writer")
}

// TestMiddleware_ForwardsHijacker confirms WebSocket-style upgrades work:
// gorilla/websocket asserts http.Hijacker on the writer it is handed.
func TestMiddleware_ForwardsHijacker(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, time.Minute)
	var sawHijacker bool
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		sawHijacker = ok
		if ok {
			_, _, _ = hj.Hijack()
		}
	}))

	w := newCapWriter()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, sawHijacker, "handler must be able to assert http.Hijacker through the recorder")
	require.True(t, w.hijacked, "Hijack must reach the underlying writer")
}

// TestMiddleware_ForwardsPusher confirms HTTP/2 server push survives the wrapper.
func TestMiddleware_ForwardsPusher(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, time.Minute)
	var sawPusher bool
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p, ok := w.(http.Pusher)
		sawPusher = ok
		if ok {
			_ = p.Push("/asset.js", nil)
		}
	}))

	w := newCapWriter()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, sawPusher, "handler must be able to assert http.Pusher through the recorder")
	require.Equal(t, "/asset.js", w.pushTarget, "Push must reach the underlying writer")
}

// TestMiddleware_ForwardsReaderFrom confirms the sendfile fast path
// (io.ReaderFrom) is preserved for large-file transfers.
func TestMiddleware_ForwardsReaderFrom(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, time.Minute)
	var sawReaderFrom bool
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rf, ok := w.(io.ReaderFrom)
		sawReaderFrom = ok
		if ok {
			_, _ = rf.ReadFrom(bytes.NewBufferString("payload"))
		}
	}))

	w := newCapWriter()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.True(t, sawReaderFrom, "handler must be able to assert io.ReaderFrom through the recorder")
	require.Equal(t, "payload", w.body.String(), "ReadFrom must reach the underlying writer")
}

// TestMiddleware_InformationalStatusDoesNotLatch confirms a 1xx
// informational response (e.g. 103 Early Hints) does NOT latch the
// recorded status: a subsequent final WriteHeader(500) must win, both
// at the underlying writer and for the breaker's trip decision.
func TestMiddleware_InformationalStatusDoesNotLatch(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(1, time.Minute)
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints) // 103
		w.WriteHeader(http.StatusInternalServerError)
	}))

	w := newCapWriter()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusInternalServerError, w.status,
		"final 500 must reach the underlying writer, not be swallowed by the 103")
	require.Equal(t, "open", cb.State(),
		"breaker must trip on the real 500, not evaluate the 103 informational status")
}

// TestMiddleware_InformationalStatusForwarded confirms 1xx headers are
// still forwarded to the client immediately (Early Hints semantics),
// not dropped.
func TestMiddleware_InformationalStatusForwarded(t *testing.T) {
	cb := circuitbreaker.NewCircuitBreaker(3, time.Minute)
	var seen []int
	h := cbmw.Middleware(cbmw.WithBreaker(cb))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints) // 103
		w.WriteHeader(http.StatusOK)
	}))

	w := &recordingWriter{capWriter: newCapWriter(), codes: &seen}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, []int{http.StatusEarlyHints, http.StatusOK}, seen,
		"both the 103 informational header and the final 200 must reach the underlying writer")
}

// recordingWriter captures every WriteHeader code that reaches the
// underlying writer so we can assert 1xx headers are forwarded.
type recordingWriter struct {
	*capWriter
	codes *[]int
}

func (r *recordingWriter) WriteHeader(code int) {
	*r.codes = append(*r.codes, code)
	r.capWriter.WriteHeader(code)
}
