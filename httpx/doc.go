// Package httpx provides HTTP helpers, safe server defaults, and a small
// vocabulary of typed handler shapes that take the routine boilerplate out
// of service code.
//
// # Server construction
//
//   - [NewServer] returns an [http.Server] with production-grade timeouts
//     (read/header/idle), a bounded MaxHeaderBytes, a slog-backed
//     ErrorLog so connection-level errors land in structured logs, and
//     HTTP/2 frame-size + concurrent-stream limits pinned via
//     `http2.ConfigureServer` to bound per-peer goroutine and buffer
//     footprints (THREAT_MODEL.md §4.2 G-03).
//   - [WithTLSConfig], [WithWriteTimeout], [WithErrorLog] configure the
//     server without re-implementing the timeout matrix.
//
// # Outbound clients
//
//   - [NewHTTPClient] returns an [http.Client] with the kit's transport
//     defaults (idle pool sizing, mTLS support, no redirect following).
//   - [NewTracingHTTPClient] adds OpenTelemetry instrumentation. Use
//     [WithKitOption] / [WithOTel] to mix kit and OTel options.
//   - [NewResilientHTTPClient] layers retry + circuit-breaker semantics on
//     top of either client.
//
// # Handler helpers
//
//   - [JSON], [JSONNoBody], [JSONStatus], [JSONNoBodyStatus] are generic
//     constructors that decode and validate the request, dispatch to a
//     typed business function, and emit a JSON response.
//   - [NoContent] is the corresponding helper for 204 endpoints.
//   - [Handle], [HandleNoBody], [HandleStatus], [HandleNoBodyStatus] are
//     the mux-bound siblings.
//
// # Response writers
//
//   - [WriteJSON] writes a status + JSON body using the request-scoped
//     logger for write-failure reporting.
//   - [WriteError] writes an [APIError] with a machine-readable code.
//   - [WriteServiceError] maps apperror types to HTTP responses with safe,
//     non-leaky messages.
//   - [WriteValidationError] emits the structured field-error shape.
//   - [WriteServiceProblem] writes the RFC 7807 problem+json equivalent.
//
// # Decoders & query parsing
//
//   - [DecodeJSON] enforces Content-Type, body size, and disallows
//     unknown fields.
//   - The query_params helpers parse pagination, filtering, and search
//     parameters without per-handler boilerplate.
//
// # Error mapping
//
//   - [HTTPStatus] returns the canonical HTTP status for an apperror.
//   - [APIError] is the standard error response envelope; codes match the
//     [apperror.Code] alphabet so backend and clients cannot drift.
//
// # Redirects & pagination
//
//   - [SafeRedirect] validates redirect targets to avoid open-redirect
//     foot-guns.
//   - The pagination sub-package emits opaque, signed cursors and ties
//     into [JSONStatus]-shaped list endpoints.
//
// All kit-created clients block redirects by default; callers must opt
// into a bounded redirect chain via [WithFollowRedirects].
package httpx
