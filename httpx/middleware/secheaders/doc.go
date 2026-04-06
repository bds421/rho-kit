// Package secheaders provides middleware that sets security response headers
// aligned with OWASP recommendations for API services.
//
// By default it adds:
//   - X-Content-Type-Options: nosniff — prevents MIME-sniffing attacks
//   - X-Frame-Options: DENY — prevents clickjacking via iframe embedding
//   - Referrer-Policy: strict-origin-when-cross-origin — limits referrer leakage
//   - Permissions-Policy: geolocation=(), microphone=(), camera=() — disables browser APIs
//   - Strict-Transport-Security: max-age=63072000; includeSubDomains — enforces HTTPS (2 years)
//   - Cache-Control: no-store — prevents caching of API responses
//   - Content-Security-Policy: default-src 'none' — strictest CSP for pure API services
//
// All headers are set on every response before the next handler runs.
// Use [WithoutHSTS] in development environments without TLS.
// Override [WithContentSecurityPolicy] and [WithCacheControl] for services
// that serve HTML content.
package secheaders
