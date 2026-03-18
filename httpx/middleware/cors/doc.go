// Package cors provides HTTP middleware for Cross-Origin Resource Sharing.
//
// It delegates to [github.com/jub0bs/cors] for Fetch-standard-compliant
// CORS handling while providing a simplified configuration API via [Options].
//
// Usage:
//
//	handler := cors.New(cors.Options{
//	    AllowedOrigins: []string{"https://example.com"},
//	})(myHandler)
package cors
