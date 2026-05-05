# NEW: httpx/middleware/secheaders/cspnonce

**Phase**: 4 (Tier‑1 missing primitive)
**Module path**: `github.com/bds421/rho-kit/httpx/middleware/secheaders` (extend existing) or new `cspnonce` subpackage.

## Why

`secheaders` today sets a static `Content-Security-Policy` (typically `default-src 'none'`). For services that render HTML (login pages, admin UIs, dashboards), this prevents inline `<script>` and `<style>` blocks — but real apps need at least some inline content. Without per-request nonces, teams either (a) loosen CSP to `'unsafe-inline'`, defeating the purpose, or (b) move all script/style to external files (heavy for small admin pages).

A per-request CSP nonce middleware solves this: generates a random nonce per request, exposes it via context, includes it in the `Content-Security-Policy` header.

## Public API

```go
package secheaders

// WithCSPNonce enables per-request CSP nonces. The nonce is set on the response
// CSP header and made available via NonceFromContext for template rendering.
func WithCSPNonce(opts ...NonceOption) Option

type NonceOption func(*nonceConfig)

// WithNonceLength sets the nonce length in bytes (default 16, base64-encoded).
func WithNonceLength(int) NonceOption

// NonceFromContext returns the per-request CSP nonce. Returns "" if the
// middleware isn't installed.
func NonceFromContext(ctx context.Context) string

// CSPNonceTemplateFunc returns a template helper suitable for html/template:
//
//   funcs := template.FuncMap{"cspnonce": secheaders.CSPNonceTemplateFunc(r)}
//
// The returned function emits the nonce attribute string `nonce="..."`.
func CSPNonceTemplateFunc(r *http.Request) func() template.HTMLAttr
```

Behavior:
- Generate random 16 bytes via `crypto/rand`; base64-encode.
- Inject into the existing `Content-Security-Policy` header by appending `'nonce-<nonce>'` to `script-src` and `style-src` directives.
- Store in request context.
- Document the template helper.

## Definition of done

- [ ] Per-request nonce generation.
- [ ] Nonce injected into CSP header (script-src + style-src).
- [ ] `NonceFromContext` accessor.
- [ ] Template helper.
- [ ] Tests: nonce changes per request; CSP header includes `'nonce-<value>'`; context accessor returns it.
- [ ] Recipe in `docs/ai/http.md`.
