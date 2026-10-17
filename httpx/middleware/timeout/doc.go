// Package timeout provides request timeout middleware.
// Responses exceeding the configured buffer cap fail with a JSON 500; a
// partial successful representation is never flushed, even when Write errors
// are ignored by the handler. The buffer limit is unchanged.
//
// asvs: V14.4.1
package timeout
