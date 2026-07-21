package compress

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"strings"
)

// compressWriter wraps http.ResponseWriter and decides per-response
// whether to compress based on Content-Type, body size, and headers
// set by the handler.
//
// Lifecycle:
//   - WriteHeader / first Write triggers the eligibility decision.
//   - Below MinSize and no Flush(): buffered in memory; on finalize()
//     written through uncompressed.
//   - Above MinSize OR explicit Flush(): compressed writer engaged;
//     subsequent writes stream compressed.
//   - Above MaxBufferSize while still buffering: bail out to
//     uncompressed streaming (logged at warn level).
//
// The wrapper proxies Flush, Hijack, and Push to the underlying writer
// when supported so chunked transfer encoding, WebSocket upgrades, and
// HTTP/2 server push continue to work.
type compressWriter struct {
	http.ResponseWriter
	encoder Encoder
	cfg     *config

	status      int
	wroteHeader bool

	buf       *bytes.Buffer // nil after streaming begins
	cw        WriterReleaser
	mode      writerMode
	closed    bool
	hijacked  bool // Hijack() was called; finalize must not touch the writer
	completed bool // next.ServeHTTP returned normally (not via panic)
}

type writerMode int

const (
	modeUndecided writerMode = iota
	modePassthrough
	modeCompressed
)

func (cw *compressWriter) Header() http.Header { return cw.ResponseWriter.Header() }

func (cw *compressWriter) WriteHeader(status int) {
	if cw.wroteHeader {
		return
	}
	// 1xx interim responses (e.g. 103 Early Hints) must not latch the
	// final status / wroteHeader — they are informational and may be
	// followed by the real status. Forward and return immediately.
	if status >= 100 && status < 200 {
		cw.ResponseWriter.WriteHeader(status)
		return
	}
	cw.status = status
	cw.wroteHeader = true
	// Decide eligibility based on headers set so far. If the handler
	// has already set Content-Encoding, we must not double-encode.
	if !headersAllowCompress(cw.Header(), cw.cfg.contentTypes) {
		cw.mode = modePassthrough
		cw.ResponseWriter.WriteHeader(status)
		return
	}
	// Hold the header write until we know whether to compress —
	// compression must set Content-Encoding and clear Content-Length
	// before the response prelude flushes.
}

func (cw *compressWriter) Write(p []byte) (int, error) {
	if cw.hijacked {
		// The connection was hijacked; the buffers are gone and any framing
		// we add would corrupt the raw stream. Mirror net/http's own writer
		// by returning http.ErrHijacked instead of dereferencing nil buffers.
		return 0, http.ErrHijacked
	}
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.mode == modePassthrough {
		return cw.ResponseWriter.Write(p)
	}
	if cw.mode == modeCompressed {
		return cw.cw.Write(p)
	}
	// modeUndecided: decide between compressed and passthrough.
	//
	// If the buffered prefix plus this write already clears MinSize we
	// commit to compression immediately. The total size is known to
	// exceed the threshold, so there is no reason to keep buffering — even
	// if it would also exceed MaxBuffer (the MaxBuffer ceiling exists to
	// bound memory while *waiting* to learn the size, not to disqualify a
	// response we already know is large enough to compress).
	if cw.buf.Len()+len(p) >= cw.cfg.minSize {
		cw.buf.Write(p)
		// p was fully accepted into the response pipeline; return len(p)
		// even when the flush fails so io.Copy / bufio.Writer do not
		// retry bytes that were already consumed (io.Writer contract).
		if err := cw.commitCompressed(); err != nil {
			return len(p), err
		}
		return len(p), nil
	}
	// Still below MinSize. Bail to passthrough only if buffering this
	// write would breach MaxBuffer (reachable when MinSize > MaxBuffer).
	if cw.buf.Len()+len(p) > cw.cfg.maxBuffer {
		// Bail out: flush buffered bytes + this write uncompressed.
		cw.commitPassthrough()
		if _, err := cw.ResponseWriter.Write(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	cw.buf.Write(p)
	return len(p), nil
}

// Flush forces a streaming decision. If buffered bytes are below the
// minimum, we commit to passthrough (compression has no benefit on
// sub-threshold flushes). Otherwise we commit to compressed streaming.
func (cw *compressWriter) Flush() {
	if cw.hijacked {
		// No buffers to flush after hijack; touching the writer would corrupt
		// the raw connection.
		return
	}
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	switch cw.mode {
	case modeUndecided:
		if cw.buf.Len() < cw.cfg.minSize {
			cw.commitPassthrough()
		} else {
			_ = cw.commitCompressed()
			// Undecided→compressed transition: flush the newly acquired
			// encoder so buffered compressed bytes actually reach the
			// client on this Flush call (not only on the next one).
			if cw.cw != nil {
				_ = cw.cw.Flush()
			}
		}
	case modeCompressed:
		// Force the encoder to push any buffered bytes to the wire.
		// WriterReleaser.Flush reaches the dynamic gzip/zstd writer
		// (poolWriter delegates); a type-assert on the interface
		// embed alone would always fail.
		if cw.cw != nil {
			_ = cw.cw.Flush()
		}
	}
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when it implements
// http.Hijacker (WebSocket upgrade path). The hijacker takes ownership
// of the raw connection — any HTTP response framing we add (status
// line, headers, compressed body) would corrupt the upgrade handshake.
// We therefore mark the response as hijacked and pass the call through
// without touching ResponseWriter; finalize() becomes a no-op.
func (cw *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := cw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("compress: underlying ResponseWriter does not implement http.Hijacker")
	}
	cw.hijacked = true
	// Release any in-flight compressor WITHOUT Close: Close would flush a
	// gzip trailer through the response path we are about to abandon, which
	// could corrupt the hijacked connection. The pooled encoder is Reset on
	// its next Acquire, so skipping Close here leaks nothing.
	if cw.cw != nil {
		cw.cw.Release()
		cw.cw = nil
	}
	cw.buf = nil
	return hj.Hijack()
}

// Push forwards to the underlying writer when it implements
// http.Pusher (HTTP/2 server push). The pushed promise is a separate
// request/response cycle and is unaffected by this compress layer.
func (cw *compressWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := cw.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (cw *compressWriter) commitPassthrough() {
	cw.mode = modePassthrough
	cw.ResponseWriter.WriteHeader(cw.status)
	if cw.buf != nil && cw.buf.Len() > 0 {
		_, _ = cw.ResponseWriter.Write(cw.buf.Bytes())
		cw.buf = nil
	}
}

// commitCompressed transitions to compressed streaming, writes the
// response prelude, and flushes any buffered prefix through the encoder.
// It returns only the flush error: the byte count of the buffered prefix
// is bookkeeping internal to the writer and must NOT be reported back to
// a Write caller (the count would exceed that caller's input length,
// violating io.Writer and breaking io.Copy / bufio.Writer accounting).
func (cw *compressWriter) commitCompressed() error {
	cw.mode = modeCompressed
	h := cw.Header()
	h.Set("Content-Encoding", cw.encoder.ContentEncoding())
	// Compressed length is unknown until close; remove any
	// pre-computed Content-Length so net/http picks chunked.
	h.Del("Content-Length")
	// ETag rewrite: an upstream-set strong ETag describes the
	// uncompressed representation. RFC 9110 §8.8 says ETags vary by
	// representation, so degrade strong ETags to weak ones when the
	// response is recoded. (Weak ETags compare semantically.)
	if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		h.Set("ETag", "W/"+etag)
	}
	cw.ResponseWriter.WriteHeader(cw.status)
	cw.cw = cw.encoder.Acquire(cw.ResponseWriter)
	if cw.buf != nil && cw.buf.Len() > 0 {
		_, err := cw.cw.Write(cw.buf.Bytes())
		cw.buf = nil
		return err
	}
	return nil
}

// finalize is deferred by Middleware so every response path runs it,
// even on handler panics. It flushes a buffered-only response (handler
// returned without crossing MinSize) and releases compressor resources.
// No-op after Hijack: the raw connection has been handed off and we
// must not write to the underlying ResponseWriter.
//
// completed reports whether next.ServeHTTP returned normally. When it is
// false the handler is panicking: the deferred finalize runs during stack
// unwinding, BEFORE an outer recover middleware catches the panic. If we
// committed a 200 here, recover would observe an already-started response
// and could only log instead of emitting a clean 500 — every panic under
// the middleware would surface to the client as a 200. On the panic path
// we therefore release the encoder without writing a new response prelude
// and discard any sub-threshold buffered bytes, leaving the underlying
// writer untouched so recover can take over.
func (cw *compressWriter) finalize() {
	if cw.closed || cw.hijacked {
		return
	}
	cw.closed = true

	if !cw.completed {
		// Handler panicked. Do not start or commit a response.
		if cw.cw != nil {
			// Headers already went out (mode became compressed before the
			// panic). Close to flush the encoder's trailer for the partial
			// body and release the pooled writer.
			_ = cw.cw.Close()
			cw.cw.Release()
			cw.cw = nil
		}
		// Drop any buffered prefix; it was never sent and the response is
		// incomplete. Leaving wroteHeader false lets recover send a 500.
		cw.buf = nil
		return
	}

	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.mode == modeUndecided {
		// Handler emitted < MinSize and never flushed.
		cw.commitPassthrough()
		return
	}
	if cw.mode == modeCompressed && cw.cw != nil {
		_ = cw.cw.Close()
		cw.cw.Release()
		cw.cw = nil
	}
}

// Unwrap exposes the underlying writer to middleware that uses Go 1.20+
// http.ResponseController (request controllers, advanced flush). Without
// this, Flush/Hijack via ResponseController would silently fall back to
// the wrapper's own methods, which is fine, but Unwrap keeps the chain
// visible for diagnostics and future controller methods.
func (cw *compressWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

func headersAllowCompress(h http.Header, contentTypes []string) bool {
	// Already-encoded responses pass through.
	if h.Get("Content-Encoding") != "" {
		return false
	}
	// Cache-Control: no-transform forbids intermediary recoding.
	if cc := h.Get("Cache-Control"); cc != "" {
		for _, token := range strings.Split(cc, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "no-transform") {
				return false
			}
		}
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		// Without a Content-Type we can't safely decide eligibility.
		// Default to passthrough — false-positive compression of an
		// unknown binary type is worse than missing a JSON body whose
		// handler forgot the header.
		return false
	}
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	for _, allowed := range contentTypes {
		if strings.HasPrefix(ct, strings.ToLower(allowed)) {
			return true
		}
	}
	return false
}
