package compress

import (
	"bytes"
	"compress/gzip"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// DefaultMinSize is the body-size threshold (in bytes) below which the
// middleware leaves the response uncompressed. Sub-KiB bodies compress
// to overhead that exceeds the savings; the per-request bookkeeping
// alone costs more than the bytes saved.
const DefaultMinSize = 1024

// MaxBufferSize caps the amount of body bytes the middleware buffers
// while waiting to learn whether the response will exceed
// [DefaultMinSize]. Above this ceiling the middleware gives up on
// negotiation, flushes the buffered prefix uncompressed, and streams
// the tail uncompressed. Keeps memory bounded under hostile or buggy
// handlers that emit megabytes before Flush.
const MaxBufferSize = 256 << 10 // 256 KiB

// DefaultContentTypes is the allowlist of MIME types eligible for
// compression. Binary types (images, video, archives) are intentionally
// absent: re-compressing them wastes CPU for no gain and breaks
// Content-Length expectations for downstream proxies that don't honor
// Transfer-Encoding: chunked on those types.
var DefaultContentTypes = []string{
	"text/",
	"application/json",
	"application/javascript",
	"application/xml",
	"application/x-www-form-urlencoded",
	"application/problem+json",
	"application/ld+json",
	"image/svg+xml",
}

type config struct {
	encoders     []Encoder
	minSize      int
	maxBuffer    int
	contentTypes []string
	logger       *slog.Logger
}

// Option configures the [Middleware].
type Option func(*config)

// WithEncoder registers a [Encoder]. Order matters: encoders are tried
// in the order registered for any given Accept-Encoding token, so the
// preferred algorithm (typically brotli when available) should come
// first. Panics on a nil encoder.
func WithEncoder(e Encoder) Option {
	if e == nil {
		panic("compress: WithEncoder requires a non-nil encoder")
	}
	return func(c *config) {
		c.encoders = append(c.encoders, e)
	}
}

// WithGzipLevel replaces the default gzip encoder with one at the
// supplied level. Panics if level is outside the gzip range.
func WithGzipLevel(level int) Option {
	enc := NewGzipEncoder(level)
	return func(c *config) {
		c.encoders = replaceOrAppend(c.encoders, "gzip", enc)
	}
}

// WithMinSize overrides [DefaultMinSize]. Panics if size is negative.
func WithMinSize(size int) Option {
	if size < 0 {
		panic("compress: WithMinSize requires a non-negative size")
	}
	return func(c *config) { c.minSize = size }
}

// WithMaxBuffer overrides [MaxBufferSize]. Panics if size is below 1
// KiB (smaller ceilings make every response a copy-then-stream churn).
func WithMaxBuffer(size int) Option {
	if size < 1024 {
		panic("compress: WithMaxBuffer requires at least 1024 bytes")
	}
	return func(c *config) { c.maxBuffer = size }
}

// WithContentTypes replaces [DefaultContentTypes]. Each entry is matched
// as a prefix against the Content-Type header (after stripping
// parameters). Panics on an empty list.
func WithContentTypes(types ...string) Option {
	if len(types) == 0 {
		panic("compress: WithContentTypes requires at least one type")
	}
	owned := append([]string(nil), types...)
	return func(c *config) { c.contentTypes = owned }
}

// WithoutGzip removes the default gzip encoder. Use when callers will
// register only non-gzip encoders (e.g. brotli sub-module).
func WithoutGzip() Option {
	return func(c *config) {
		c.encoders = removeByName(c.encoders, "gzip")
	}
}

// WithLogger sets the slog.Logger held by the middleware. A nil
// argument is accepted; the default is [slog.Default].
//
// Note: the middleware does not currently emit any log records — the
// compression bail-out and finalize paths are silent. This option only
// overrides the stored logger for future use.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// Middleware returns an http.Handler middleware that compresses
// eligible responses based on the request's Accept-Encoding header.
//
// Defaults: gzip enabled at [gzip.DefaultCompression]; min body size
// [DefaultMinSize]; max in-memory buffer [MaxBufferSize]; content-type
// allowlist [DefaultContentTypes].
//
// Eligibility rules (any failure passes the response through untouched):
//   - Request method must not be HEAD.
//   - Response Content-Encoding must be unset.
//   - Response Content-Type prefix must be in the allowlist.
//   - Request If-Range / Range must be absent.
//   - Response Cache-Control "no-transform" is honoured.
//   - WebSocket upgrades (responses to http.Hijacker callers) pass
//     through. The wrapper exposes Hijack so handlers can take over the
//     connection without seeing a compressed writer.
func Middleware(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		encoders:     []Encoder{NewGzipEncoder(gzip.DefaultCompression)},
		minSize:      DefaultMinSize,
		maxBuffer:    MaxBufferSize,
		contentTypes: append([]string(nil), DefaultContentTypes...),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("compress: Middleware option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Vary advertises Accept-Encoding-dependent caching even
			// on pass-through responses; otherwise a downstream cache
			// might collapse "gzip" and "identity" entries.
			addVary(w.Header(), "Accept-Encoding")

			if !eligibleRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			// Accept-Encoding is a list-typed header that may be split across
			// multiple field lines; join them so a client sending e.g.
			// "Accept-Encoding: br" + "Accept-Encoding: gzip" negotiates the
			// full list rather than only the first line.
			enc := selectEncoder(strings.Join(r.Header.Values("Accept-Encoding"), ","), cfg.encoders)
			if enc == nil {
				next.ServeHTTP(w, r)
				return
			}
			cw := &compressWriter{
				ResponseWriter: w,
				encoder:        enc,
				cfg:            &cfg,
				buf:            bytes.NewBuffer(nil),
			}
			defer cw.finalize()
			next.ServeHTTP(cw, r)
			// Mark normal completion. finalize (deferred above) only
			// commits/flushes the response when this flag is set; on a
			// handler panic it stays false so finalize leaves the writer
			// untouched for an outer recover middleware to send a clean
			// 500 instead of a committed 200.
			cw.completed = true
		})
	}
}

func eligibleRequest(r *http.Request) bool {
	if r.Method == http.MethodHead {
		return false
	}
	// Range / If-Range: compression breaks byte offsets the client
	// pre-computed against the uncompressed representation.
	if r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "" {
		return false
	}
	return true
}

// selectEncoder parses the Accept-Encoding header (RFC 9110 §12.5.3)
// and returns the highest-preference encoder both client and server
// support, or nil for identity / no match.
func selectEncoder(acceptEncoding string, encoders []Encoder) Encoder {
	if acceptEncoding == "" || len(encoders) == 0 {
		return nil
	}
	type pref struct {
		token string
		q     float64
	}
	var prefs []pref
	// refused records tokens the client explicitly excluded with q=0 (RFC 9110
	// §12.5.3). The wildcard ('*') must match only codings NOT explicitly
	// mentioned, so a refused token must never be selected via the wildcard.
	refused := make(map[string]bool)
	for _, raw := range strings.Split(acceptEncoding, ",") {
		token, q := parseAcceptEncodingEntry(raw)
		if token == "" {
			continue
		}
		// RFC 9110 §12.5.3: q=0 means "not acceptable". Negative q is
		// non-conformant; treat it as refused rather than as a high
		// preference (ParseFloat would otherwise keep q=-0.5 above zero
		// tokens when sorting descending).
		if q <= 0 {
			refused[strings.ToLower(token)] = true
			continue
		}
		prefs = append(prefs, pref{token: token, q: q})
	}
	if len(prefs) == 0 {
		return nil
	}
	sort.SliceStable(prefs, func(i, j int) bool {
		return prefs[i].q > prefs[j].q
	})
	for _, p := range prefs {
		if p.token == "*" {
			// Wildcard matches codings not explicitly mentioned: pick the
			// first registered encoder the client did not refuse with q=0.
			for _, e := range encoders {
				if !refused[strings.ToLower(e.ContentEncoding())] {
					return e
				}
			}
			continue
		}
		for _, e := range encoders {
			if strings.EqualFold(e.ContentEncoding(), p.token) {
				return e
			}
		}
	}
	return nil
}

func parseAcceptEncodingEntry(raw string) (string, float64) {
	parts := strings.Split(strings.TrimSpace(raw), ";")
	token := strings.ToLower(strings.TrimSpace(parts[0]))
	if token == "" {
		return "", 0
	}
	q := 1.0
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "q=") {
			parsed, err := strconv.ParseFloat(strings.TrimPrefix(p, "q="), 64)
			if err == nil {
				q = parsed
			}
		}
	}
	return token, q
}

func addVary(h http.Header, value string) {
	existing := h.Values("Vary")
	for _, v := range existing {
		for _, token := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	h.Add("Vary", value)
}

func replaceOrAppend(encoders []Encoder, name string, replacement Encoder) []Encoder {
	out := make([]Encoder, 0, len(encoders)+1)
	replaced := false
	for _, e := range encoders {
		if strings.EqualFold(e.ContentEncoding(), name) {
			out = append(out, replacement)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, replacement)
	}
	return out
}

func removeByName(encoders []Encoder, name string) []Encoder {
	out := make([]Encoder, 0, len(encoders))
	for _, e := range encoders {
		if strings.EqualFold(e.ContentEncoding(), name) {
			continue
		}
		out = append(out, e)
	}
	return out
}
