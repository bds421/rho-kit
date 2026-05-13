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
//   - Cross-Origin-Opener-Policy: same-origin — severs cross-origin window.opener access
//   - Cross-Origin-Embedder-Policy: require-corp — every embedded resource must opt in
//   - Cross-Origin-Resource-Policy: same-origin — this service's responses can only be loaded by same-origin documents
//
// All headers are set on every response before the next handler runs.
// Use [WithoutHSTS] in development environments without TLS.
// Override [WithContentSecurityPolicy] and [WithCacheControl] for services
// that serve HTML content.
//
// # COOP / COEP / CORP trade-offs
//
// The Cross-Origin-* trio defaults to the cross-origin-isolation set
// (COOP=same-origin, COEP=require-corp, CORP=same-origin). This is
// the right baseline for API services and admin dashboards — it
// closes the Spectre / window-reference leak primitive — but breaks
// embed contracts that legitimately depend on cross-origin resources:
//
//   - COEP=require-corp blocks any cross-origin subresource (JS SDK
//     from a CDN, web font, analytics tag, image host) that does
//     not respond with its own Cross-Origin-Resource-Policy header
//     or a CORS allowance. Audit every third-party origin you embed
//     before deploying; if even one cannot be fixed, opt out with
//     [WithoutCrossOriginEmbedder].
//   - COOP=same-origin breaks OAuth popup flows that rely on
//     window.opener to coordinate between the auth provider's tab
//     and yours. Opt out with [WithoutCrossOriginOpener] (or set
//     COOP=same-origin-allow-popups via [WithCrossOriginOpenerPolicy])
//     in services that integrate with such flows.
//   - CORP=same-origin prevents cross-origin documents from loading
//     this service's responses at all (image hotlink, public asset
//     embed). CDN origins and public-asset hosts should opt out via
//     [WithoutCrossOriginResource].
//
// For iframe-heavy services where all three policies are too strict,
// [WithoutCrossOriginPolicies] disables the entire trio in a single
// opt-out.
package secheaders
