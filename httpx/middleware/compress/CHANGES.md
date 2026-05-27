# Changes

## Unreleased — v2.0

- Initial release. Implements `Middleware()` with safe defaults:
  - gzip enabled at `gzip.DefaultCompression`
  - min response size 1 KiB
  - max in-memory buffer 256 KiB
  - content-type allowlist excludes binary types and images (except SVG)
  - `Vary: Accept-Encoding` on every response (including pass-through)
  - Strong ETag → weak ETag rewrite on compressed responses
  - `Content-Length` cleared on compressed responses (chunked transfer)
  - HEAD / Range / If-Range / no-transform / already-encoded pass-through
  - Hijack passes through cleanly for WebSocket upgrade
- Pluggable `Encoder` interface admits brotli and zstd sub-modules.
- Stack opt-in via `stack.WithCompress(...)`.
