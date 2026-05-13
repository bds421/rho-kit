// Package maxbody provides request body size limiting middleware.
//
// HTTP handlers that read the request body without an explicit byte cap
// are a classic memory-exhaustion vector: a single client can send a
// multi-gigabyte upload to an endpoint that expected a JSON payload and
// pin the server until the OOM killer arrives. The kit's golden path
// installs [MaxBodySize] on every public mux so the body is wrapped in
// [http.MaxBytesReader] before the handler ever touches it; exceeding
// the cap returns HTTP 413 the first time the handler calls Read.
//
// Key entry points:
//
//   - [MaxBodySize] — middleware constructor; pick a per-route or
//     per-mux byte cap (1 MiB is the kit's default for JSON APIs).
//     Endpoints that legitimately accept large uploads should mount a
//     separate sub-mux with a higher cap so the smaller default still
//     protects every other route.
//
// Routes that stream a body (uploads to object storage) should still
// install a higher [MaxBodySize] rather than removing it: the cap
// caps total bytes per request, not bytes per Read.
package maxbody
