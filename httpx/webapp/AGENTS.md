# httpx/webapp

## Purpose

Serves a built single-page web app from an `fs.FS` (usually `go:embed`):
files by path, `index.html` for every other path, precompressed siblings by
`Accept-Encoding`.

## Public API

- `New(files fs.FS, opts ...Option) (http.Handler, error)` — indexes the
  build once; fails without `index.html`.
- `WithImmutable(prefixes ...string)` — hashed file prefixes: cached for a
  year (`public, max-age=31536000, immutable`); a missing file there is 404.

## Behaviour

- `x.br` / `x.gz` next to `x` are sent with `Content-Encoding` and
  `Vary: Accept-Encoding`; br wins a tie. The siblings are not served on
  their own paths.
- Every file has a strong ETag (per coding); everything outside the
  immutable prefixes, `index.html` included, is `no-cache`.
- GET and HEAD only (405 otherwise); `..` segments answer 400.
- Pair with `stack.WithStaticPaths` for the immutable prefix, so large files
  bypass the timeout middleware's 1 MiB response buffer.

## Tests

`go test -race ./webapp/` from `httpx`.
