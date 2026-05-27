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

	buf      *bytes.Buffer // nil after streaming begins
	cw       WriterReleaser
	mode     writerMode
	closed   bool
	hijacked bool // Hijack() was called; finalize must not touch the writer
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
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.mode == modePassthrough {
		return cw.ResponseWriter.Write(p)
	}
	if cw.mode == modeCompressed {
		return cw.cw.Write(p)
	}
	// modeUndecided: buffer until we cross MinSize or MaxBuffer.
	if cw.buf.Len()+len(p) > cw.cfg.maxBuffer {
		// Bail out: flush buffered bytes + this write uncompressed.
		cw.commitPassthrough()
		if _, err := cw.ResponseWriter.Write(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	cw.buf.Write(p)
	if cw.buf.Len() >= cw.cfg.minSize {
		return cw.commitCompressed()
	}
	return len(p), nil
}

// Flush forces a streaming decision. If buffered bytes are below the
// minimum, we commit to passthrough (compression has no benefit on
// sub-threshold flushes). Otherwise we commit to compressed streaming.
func (cw *compressWriter) Flush() {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	switch cw.mode {
	case modeUndecided:
		if cw.buf.Len() < cw.cfg.minSize {
			cw.commitPassthrough()
		} else {
			_, _ = cw.commitCompressed()
		}
	case modeCompressed:
		// Force the encoder to push any buffered bytes to the wire.
		if flusher, ok := cw.cw.(interface{ Flush() error }); ok {
			_ = flusher.Flush()
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
	// Release any in-flight compressor; pool entries are not safe to
	// reuse after we abandon the response.
	if cw.cw != nil {
		_ = cw.cw.Close()
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

func (cw *compressWriter) commitCompressed() (int, error) {
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
		n, err := cw.cw.Write(cw.buf.Bytes())
		cw.buf = nil
		return n, err
	}
	return 0, nil
}

// finalize is deferred by Middleware so every response path runs it,
// even on handler panics. It flushes a buffered-only response (handler
// returned without crossing MinSize) and releases compressor resources.
// No-op after Hijack: the raw connection has been handed off and we
// must not write to the underlying ResponseWriter.
func (cw *compressWriter) finalize() {
	if cw.closed || cw.hijacked {
		return
	}
	cw.closed = true
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
