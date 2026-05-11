// Package cors provides HTTP middleware for Cross-Origin Resource Sharing.
//
// It delegates to [github.com/jub0bs/cors] for Fetch-standard-compliant
// CORS handling while providing a simplified configuration API via [Options].
// Allowed origins are required; omit this middleware when no browser cross-
// origin API should be exposed.
//
// Usage:
//
//	handler := cors.New(cors.Options{
//	    AllowedOrigins: []string{"https://example.com"},
//	})(myHandler)
//
// asvs: V13.2.1
package cors
