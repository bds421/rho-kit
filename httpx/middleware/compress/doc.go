// Package compress provides HTTP response-compression middleware with
// safe defaults: gzip enabled in-tree, an [Encoder] interface that lets
// callers plug in additional algorithms (brotli, zstd) without pulling
// the dependency into every httpx consumer.
//
// # Use this package when
//
//   - You serve text-shaped responses (JSON, HTML, JS, CSS, XML, plain
//     text) and want negotiation-driven gzip/brotli on responses that
//     exceed a size threshold.
//   - You want a Vary: Accept-Encoding header on every response (set
//     even on pass-through responses) so caches downstream don't fuse
//     compressed and uncompressed copies.
//
// # Do NOT use this package for
//
//   - Compressing binary content (images, video, archives). The default
//     content-type allowlist excludes those for that reason.
//   - WebSocket frames. The middleware passes through
//     [http.Hijacker]-capable responses untouched so the
//     [httpx/websocket] handler can upgrade cleanly. WebSocket
//     per-message-deflate is configured on the websocket handler
//     itself.
//   - Range requests (If-Range / Range headers). Compression breaks the
//     byte-offset semantics of Range; the middleware passes those
//     through.
//
// # Sibling packages
//
//   - [httpx/middleware/stack]   — opt the middleware into the canonical
//     stack via [stack.WithCompress] (off by default; compression is a
//     surprise if mandatory).
//   - [httpx/websocket]          — owns its own permessage-deflate; do
//     not stack the two.
//
// # Quick start
//
//	mux := http.NewServeMux()
//	handler := compress.Middleware()(mux)
//
//	// or, in the canonical stack:
//	handler := stack.Default(mux, logger, stack.WithCompress())
//
// # Adding brotli
//
// Import the brotli sub-module (separate Go module so the dep stays out
// of the default httpx closure):
//
//	import _ "github.com/bds421/rho-kit/httpx/v2/middleware/compress/brotli"
//
// (brotli sub-module ships in a follow-up wave; the Encoder interface
// below is the contract it will satisfy.)
package compress
