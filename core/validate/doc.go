// Package validate provides struct validation driven by JSON Schema,
// with field-level error reporting via [core/v2/apperror.ValidationError].
//
// The package wires `github.com/google/jsonschema-go` (in-memory
// schema model) together with `github.com/santhosh-tekuri/jsonschema/v6`
// (compilation + validation). Constraints are declared on struct
// fields via the `jsonschema:"..."` tag — the kit's own grammar
// recognises the keywords `required`, `min`, `max`, `len`, `gte`,
// `lte`, `gt`, `lt`, `oneof`, `email`, `url`, `uri`, `uuid`, `ip`,
// `ipv4`, `ipv6`, `cidr`, `hostname`, `alpha`, `alphanum`, `numeric`,
// `datetime`, `startswith=…`, `endswith=…`, `contains=…`,
// `excludesall=…`, `pattern=…`, `format=…`, `unique`. Tokens whose
// key is not in this set are kept as the property's description, so
// `jsonschema:"required,Customer e-mail"` records both the
// requirement and the description.
//
// # Use this when
//
//   - You receive a JSON request body decoded into a Go struct and
//     need apperror-shaped field errors for the HTTP / MCP / gRPC
//     transports.
//   - You want a JSON Schema for an arbitrary kit-typed struct so it
//     can be published in an OpenAPI document (`httpx/openapigen`) or
//     an MCP tool descriptor (`httpx/mcp`). Use [SchemaFor] or
//     [Validator.SchemaForType] — the returned schema is the cached
//     package instance, so clone before mutating.
//   - You want to register a custom format-style validator for a
//     domain vocabulary (e.g. ISO-3166 country codes). Use
//     [RegisterFormat] during init; see "Convention deviations" below
//     for why this returns an error rather than panicking.
//
// # Convention deviations
//
// [RegisterFormat] returns an error instead of panicking on
// misconfiguration. The rest of the kit's option-style helpers panic
// on programmer error at construction time. RegisterFormat keeps the
// error return because (a) it can be called after a Validator has
// already served traffic — a panic there would be a runtime crash
// rather than a startup crash; (b) existing callers branch on the
// error to surface "duplicate format" or "validator already frozen"
// to ops dashboards. The shape is preserved deliberately; do not file
// it as an inconsistency.
//
// # Schema walker limitations
//
// The reflection walker handles the encoding/json shape of struct
// composition: anonymous embedded structs are flattened into the
// parent, exported fields of an embedded unexported-named struct are
// promoted in the schema, and `json:"-"` fields are skipped. When a
// parent field and an embedded field share the same JSON name, the
// schema emits the shallower (parent) field's declaration once — its
// requiredness and constraints win and the embedded sibling is dropped
// — matching encoding/json, which picks the shallower field at
// marshal/unmarshal time. The schema just doesn't reflect that the
// embedded path "would have been there" without the shadow. See
// [structSchema] in `schema.go` for the implementation.
//
// A []byte field marshals to a base64 string, so length and pattern
// constraints (`min`, `max`, `len`, `pattern`) on it are evaluated
// against the base64-encoded text, not the raw byte count (a 3-byte
// slice encodes to a 4-character base64 string). A byte *array*
// ([16]byte, [32]byte) instead marshals as a JSON array of integers
// and takes array constraints.
//
// See also: [github.com/bds421/rho-kit/core/v2/apperror] for the
// kit's error taxonomy and [github.com/bds421/rho-kit/httpx/v2/openapigen]
// for the OpenAPI 3.1 emitter that consumes [SchemaFor].
package validate
