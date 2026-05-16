// Package openapigen builds an OpenAPI 3.1 document from kit-typed
// HTTP handlers and serves it via a single, opt-in http.Handler.
//
// Wave 128 added the package so services that already lean on the
// kit's typed [httpx.JSON] / [httpx.Handle] helpers can publish a
// machine-readable spec for free. Every typed handler carries a
// statically known request + response type; the kit already turns
// those types into JSON Schemas via [core/v2/validate.SchemaFor]
// (wave 124). Combining the two yields a complete OpenAPI 3.1 path
// item per registered route.
//
// # Design
//
//   - OpenAPI 3.1 is JSON-Schema 2020-12 compatible. The kit's
//     in-memory schema type (jsonschema-go's [jsonschema.Schema]) is
//     therefore embedded directly into operations rather than
//     translated through a separate openapi-schema model — no JSON
//     round-trip, no third-party openapi dep to drag in, no risk of
//     keyword drift between the validator's schema and the spec's
//     schema. The trade-off is an in-package minimal set of OpenAPI
//     structs (Info, PathItem, Operation, RequestBody, Response,
//     MediaType, Components) — see [Spec] for the rendered shape.
//
//   - The Spec is additive: existing [httpx.JSON] / [httpx.Handle]
//     signatures and behaviour are unchanged. Services opt in by
//     constructing a [Spec], registering routes via [Register] (or
//     the mux-integrated [Handle] / [HandleStatus] helpers), and
//     mounting the [Spec.Handler] under whatever URL they prefer
//     (convention: "/openapi.json").
//
//   - Registration is safe for concurrent use. Operations are stored
//     keyed by `<method>::<path>`; duplicate registration returns an
//     error rather than silently overwriting so misconfiguration
//     surfaces at boot rather than after rollout.
//
// # Usage
//
//	spec := openapigen.NewSpec("widgets-api", "v1.0.0")
//
//	mux := http.NewServeMux()
//	openapigen.Handle[CreateReq, CreateResp](mux, spec,
//	    http.MethodPost, "/widgets", logger, createWidget,
//	    openapigen.WithSummary("Create a widget"),
//	    openapigen.WithOperationID("createWidget"),
//	    openapigen.WithTags("widgets"),
//	)
//	mux.Handle("/openapi.json", spec.Handler())
//
// The spec handler returns `application/json` with the OpenAPI 3.1
// document. ETag + Cache-Control are NOT set; the document is built
// once on first request and cached in-memory keyed by the spec's
// registration revision so subsequent calls are O(1).
//
// # Scope limits
//
// The initial wave deliberately ships a narrow surface:
//
//   - Request body schema only for routes registered with a Req type;
//     query / path parameters are NOT auto-discovered (Go's net/http
//     pattern grammar does not expose typed parameters at registration
//     time). Callers can attach parameters explicitly via
//     [WithParameter].
//   - Single response per status code per route. Callers needing
//     multiple statuses register the response per-status via
//     [WithResponseStatus] / [WithResponseStatusSchema].
//   - No security schemes are inferred. Callers attach security
//     schemes via [WithComponentsSecurityScheme] +
//     [WithOperationSecurity].
//   - No tags object is emitted — only per-operation `tags` strings.
//
// These are not architectural limits; future waves can extend the
// surface without breaking the present API.
package openapigen
