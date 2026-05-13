// Package cors provides HTTP middleware for Cross-Origin Resource Sharing.
//
// It delegates to [github.com/jub0bs/cors] for Fetch-standard-compliant
// CORS handling while providing the functional-Option configuration shape
// used elsewhere in the kit. At least one allowed origin must be supplied
// via [WithAllowedOrigins]; omit this middleware when no browser
// cross-origin API should be exposed.
//
// Usage:
//
//	handler := cors.New(
//	    cors.WithAllowedOrigins("https://example.com"),
//	)(myHandler)
//
// asvs: V13.2.1
package cors
