# httpx/middleware/compress

## Purpose

HTTP response-compression middleware (gzip in-tree; external brotli or
zstd encoders can implement the pluggable `Encoder` interface).
Negotiation-driven, content-type allowlist, min-size threshold, Vary
header.

## Public API

- `Middleware(opts ...Option) func(http.Handler) http.Handler`
- `NewGzipEncoder(level int) *GzipEncoder` (pluggable Encoder interface
  also accepts user-supplied encoders via `WithEncoder`)
- Options: `WithGzipLevel`, `WithMinSize`, `WithMaxBuffer`,
  `WithContentTypes`, `WithEncoder`, `WithoutGzip`, `WithLogger`

## Eligibility rules (a single failure passes through)

- Method != HEAD
- No `Range` or `If-Range` request header
- Response `Content-Encoding` unset
- Response `Cache-Control` does not include `no-transform`
- Response `Content-Type` prefix matches the allowlist
- WebSocket upgrade path: `Hijack()` is detected; finalize becomes a no-op

## Operability

- `Vary: Accept-Encoding` is set on every response (including
  pass-through) to keep downstream caches honest.
- Compressed responses have `Content-Length` cleared (length unknown
  until close) and strong ETags downgraded to weak (representation
  changed).
- Buffer ceiling at `MaxBufferSize` (256 KiB default) caps memory under
  hostile handlers.

## Tests

`go test -race ./...` from this directory. Covers: above/below MinSize,
no Accept-Encoding, binary content-type passthrough, Range/HEAD
passthrough, already-encoded passthrough, no-transform passthrough,
q-value parsing, wildcard, ETag weakening, Vary dedup, Hijack path,
ceiling bail, missing-encoding fallback, WithoutGzip, Flush commits.

## Performance

Gzip writers are pooled via `sync.Pool`. Per-response overhead: one
pool Get/Put + 32 KiB gzip window. Below MinSize the middleware does a
single bytes.Buffer write and the original write — measurably cheaper
than pre-allocating a gzip writer to throw away.

## See also

- `httpx/middleware/stack` — opt in via `stack.WithCompress()` (not in
  default chain; compression is opt-in)
- `httpx/websocket` — owns its own per-message-deflate; do not stack
