package openapigen

import (
	"encoding/json"

	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

// Document is the on-wire OpenAPI 3.1 root object emitted by
// [Spec.Marshal] and [Spec.Handler]. Only the subset of fields the
// kit actually populates is modelled; unknown fields would otherwise
// hide behind `interface{}` and silently drift between releases.
//
// Field order matches the OpenAPI 3.1 specification's `OpenAPI Object`
// section so the marshalled JSON is human-readable in diff review.
type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Servers    []Server              `json:"servers,omitempty"`
	Paths      map[string]PathItem   `json:"paths,omitempty"`
	Components *Components           `json:"components,omitempty"`
	Tags       []Tag                 `json:"tags,omitempty"`
	Security   []map[string][]string `json:"security,omitempty"`
}

// Info is the `info` object — title + version are required by the
// OpenAPI 3.1 spec; everything else is optional metadata.
type Info struct {
	Title          string   `json:"title"`
	Version        string   `json:"version"`
	Summary        string   `json:"summary,omitempty"`
	Description    string   `json:"description,omitempty"`
	TermsOfService string   `json:"termsOfService,omitempty"`
	Contact        *Contact `json:"contact,omitempty"`
	License        *License `json:"license,omitempty"`
}

// Contact is the `info.contact` object.
type Contact struct {
	Name  string `json:"name,omitempty"`
	URL   string `json:"url,omitempty"`
	Email string `json:"email,omitempty"`
}

// License is the `info.license` object. OAS 3.1 allows `identifier`
// (SPDX) XOR `url`; the kit emits whichever the caller supplied.
type License struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Server is one entry in the top-level `servers` array.
type Server struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// Tag is one entry in the top-level `tags` array. Per-operation tag
// strings reference these entries by name.
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PathItem is the `paths.<path>` object. The kit populates the verb
// fields it sees during registration; unknown verbs are ignored.
type PathItem struct {
	Summary     string      `json:"summary,omitempty"`
	Description string      `json:"description,omitempty"`
	Get         *Operation  `json:"get,omitempty"`
	Put         *Operation  `json:"put,omitempty"`
	Post        *Operation  `json:"post,omitempty"`
	Delete      *Operation  `json:"delete,omitempty"`
	Options     *Operation  `json:"options,omitempty"`
	Head        *Operation  `json:"head,omitempty"`
	Patch       *Operation  `json:"patch,omitempty"`
	Trace       *Operation  `json:"trace,omitempty"`
	Parameters  []Parameter `json:"parameters,omitempty"`
}

// Operation is one HTTP verb under a [PathItem]. Field ordering
// matches the OAS 3.1 `Operation Object` section.
//
// Security is a pointer-to-slice so callers can distinguish three
// states the OAS spec treats differently:
//
//   - nil pointer        → emit nothing (operation inherits the
//     document-level `security` requirement).
//   - pointer to empty   → emit `"security": []` (operation
//     explicitly opts out of the document-level requirement —
//     anonymous endpoint).
//   - pointer to entries → emit the declared requirements.
//
// A bare `[]map[string][]string` with `omitempty` would drop the
// empty-slice case (Go's `json` package treats len==0 as absent),
// silently re-enabling the global requirement.
type Operation struct {
	Tags        []string               `json:"tags,omitempty"`
	Summary     string                 `json:"summary,omitempty"`
	Description string                 `json:"description,omitempty"`
	OperationID string                 `json:"operationId,omitempty"`
	Parameters  []Parameter            `json:"parameters,omitempty"`
	RequestBody *RequestBody           `json:"requestBody,omitempty"`
	Responses   map[string]Response    `json:"responses,omitempty"`
	Security    *[]map[string][]string `json:"security,omitempty"`
	Deprecated  bool                   `json:"deprecated,omitempty"`
}

// Parameter is the `parameter` object. `In` is one of "query", "header",
// "path", "cookie"; the kit does not validate the value beyond the
// spec's enumeration in [WithParameter].
type Parameter struct {
	Name        string             `json:"name"`
	In          string             `json:"in"`
	Description string             `json:"description,omitempty"`
	Required    bool               `json:"required,omitempty"`
	Deprecated  bool               `json:"deprecated,omitempty"`
	Schema      *jsonschema.Schema `json:"schema,omitempty"`
	Example     any                `json:"example,omitempty"`
}

// RequestBody is the `requestBody` object. Content is keyed by media
// type ("application/json", "application/x-www-form-urlencoded", …).
type RequestBody struct {
	Description string               `json:"description,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// Response is one entry in `responses`. The OpenAPI spec requires a
// `description` even for empty bodies; callers that don't supply one
// get a synthesised "Response for HTTP <status>" so the document still
// validates.
type Response struct {
	Description string               `json:"description"`
	Headers     map[string]Header    `json:"headers,omitempty"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// Header is the `header` object (a [Parameter] without name/in).
type Header struct {
	Description string             `json:"description,omitempty"`
	Required    bool               `json:"required,omitempty"`
	Deprecated  bool               `json:"deprecated,omitempty"`
	Schema      *jsonschema.Schema `json:"schema,omitempty"`
}

// MediaType is the `mediaType` object. The kit currently emits the
// `schema` field via the request/response options; the `Example`
// field is reserved for a future option (no [RouteOption] surfaces
// it yet).
type MediaType struct {
	Schema  *jsonschema.Schema `json:"schema,omitempty"`
	Example any                `json:"example,omitempty"`
}

// Components holds reusable bits referenced from operations. The kit
// emits only the subset needed for the security-scheme story (and
// future schema-deduplication); other components remain optional.
type Components struct {
	Schemas         map[string]*jsonschema.Schema `json:"schemas,omitempty"`
	SecuritySchemes map[string]SecurityScheme     `json:"securitySchemes,omitempty"`
}

// SecurityScheme is the `securityScheme` object. Only the fields the
// kit currently supports are modelled; advanced flows (`oauth2.flows`)
// can be wired in by callers that need them (the [json.RawMessage]
// `Extensions` map is the escape hatch).
type SecurityScheme struct {
	Type             string          `json:"type"`
	Description      string          `json:"description,omitempty"`
	Name             string          `json:"name,omitempty"`
	In               string          `json:"in,omitempty"`
	Scheme           string          `json:"scheme,omitempty"`
	BearerFormat     string          `json:"bearerFormat,omitempty"`
	OpenIDConnectURL string          `json:"openIdConnectUrl,omitempty"`
	Extensions       json.RawMessage `json:"-"`
}
