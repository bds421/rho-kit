package openapigen

import (
	"errors"
	"fmt"

	jsonschema "github.com/google/jsonschema-go/jsonschema"
)

// RouteOption configures a single operation at registration time.
// Options are evaluated in order; later options override earlier ones
// for scalar fields and append for slice fields.
type RouteOption func(*routeConfig) error

// routeConfig is the working state assembled by RouteOption calls
// before being copied onto the *routeState.
//
// security is a pointer-to-slice to preserve the OAS 3.1 three-state
// distinction (unset / anonymous-override / declared). See
// [Operation] for the rationale.
type routeConfig struct {
	tags        []string
	summary     string
	description string
	operationID string
	parameters  []Parameter
	deprecated  bool
	security    *[]map[string][]string

	// skipParamDiscovery suppresses the wave-162 auto-discovery of
	// path parameters from the OAS template (`{name}` segments). Set
	// via [WithSkipPathParamDiscovery]; the default is false so most
	// callers get the ergonomic shape automatically.
	skipParamDiscovery bool

	// externalDocs is the per-operation externalDocs link added in
	// wave 163. Optional.
	externalDocs *ExternalDocs

	// requestExample is an optional example payload for the request
	// body, keyed by request media type. Lets a generated portal show
	// concrete inputs without consuming the request schema's `example`
	// keyword.
	requestExamples map[string]any

	// responseExamples is an optional example payload per (status,
	// media type). Same rationale as requestExamples.
	responseExamples map[int]map[string]any

	// parameterExamples augments [Parameter.Example] post-hoc so the
	// example can be set after WithParameter without re-declaring the
	// whole Parameter struct.
	parameterExamples map[string]any

	// Request body.
	requestSchema      *jsonschema.Schema
	requestMediaType   string
	requestDescription string
	requestRequired    bool

	// Responses keyed by status code.
	responseDescriptions map[int]string
	responseSchemas      map[int]*jsonschema.Schema
	responseTypes        map[int]string

	// Multi-content registrations: status -> mediaType -> schema.
	// Populated by [WithResponseContent] / [WithResponseContentT]
	// and merged with the singular [WithResponseType] registration
	// at Register time. Both shapes can coexist on the same status.
	responseExtraContent map[int]map[string]*jsonschema.Schema

	// Per-status response headers: status -> header name -> Header.
	// Populated by [WithResponseHeader].
	responseHeaders map[int]map[string]Header
}

// WithTags appends one or more tag names to the operation. Tag names
// are free-form strings; the kit does not require them to be declared
// via [Spec.AddTag], though that is the convention for richer tag
// objects (description, externalDocs, …).
func WithTags(tags ...string) RouteOption {
	return func(c *routeConfig) error {
		for _, t := range tags {
			if t == "" {
				return errors.New("openapigen: WithTags: empty tag name")
			}
		}
		c.tags = append(c.tags, tags...)
		return nil
	}
}

// WithSummary sets the operation summary (short one-line label).
func WithSummary(s string) RouteOption {
	return func(c *routeConfig) error {
		c.summary = s
		return nil
	}
}

// WithDescription sets the operation description (longer prose).
func WithDescription(s string) RouteOption {
	return func(c *routeConfig) error {
		c.description = s
		return nil
	}
}

// WithOperationID sets the operation's `operationId`. Per the OAS 3.1
// spec, operation IDs must be unique across the entire document; the
// kit does NOT validate uniqueness because routes may register
// independently — duplicate IDs surface in spec validators.
func WithOperationID(id string) RouteOption {
	return func(c *routeConfig) error {
		c.operationID = id
		return nil
	}
}

// WithDeprecated marks the operation as deprecated.
func WithDeprecated() RouteOption {
	return func(c *routeConfig) error {
		c.deprecated = true
		return nil
	}
}

// WithExternalDocs attaches an [ExternalDocs] link to the operation
// (OAS 3.1 `Operation Object § externalDocs`). Use to point readers
// at a runbook, design doc, or developer-portal page from the
// generated spec.
//
// URL must be non-empty. Pass an empty description to omit it from
// the rendered document.
func WithExternalDocs(url, description string) RouteOption {
	return func(c *routeConfig) error {
		if url == "" {
			return errors.New("openapigen: WithExternalDocs: empty url")
		}
		c.externalDocs = &ExternalDocs{URL: url, Description: description}
		return nil
	}
}

// WithRequestExample attaches an example request payload at the
// configured request media type (defaults to `application/json` if
// no [WithRequestMediaType] has been set yet). The example is
// emitted under the MediaType `example` field rather than the
// JSON-Schema `example` keyword so it appears in OAS rendering
// tools without contaminating the schema.
//
// Calling twice for the same media type replaces the previous
// example.
func WithRequestExample(example any) RouteOption {
	return func(c *routeConfig) error {
		if c.requestExamples == nil {
			c.requestExamples = map[string]any{}
		}
		mt := c.requestMediaType
		if mt == "" {
			mt = DefaultJSONMediaType
		}
		c.requestExamples[mt] = example
		return nil
	}
}

// WithResponseExample attaches an example response payload at
// (status, mediaType). Pair with [WithResponseType] /
// [WithResponseContentT] to register the schema; this option fills
// in the corresponding MediaType `example` field for portal
// rendering.
func WithResponseExample(status int, mediaType string, example any) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseExample: invalid status %d", status)
		}
		if mediaType == "" {
			return errors.New("openapigen: WithResponseExample: empty media type")
		}
		if c.responseExamples == nil {
			c.responseExamples = map[int]map[string]any{}
		}
		if c.responseExamples[status] == nil {
			c.responseExamples[status] = map[string]any{}
		}
		c.responseExamples[status][mediaType] = example
		return nil
	}
}

// WithParameterExample attaches an example value to a previously-
// declared parameter (by name). Useful when the [WithParameter]
// declaration came from a shared helper that does not know the
// example, or when multiple call sites attach different examples
// without re-declaring the schema.
//
// The option is a no-op if no parameter with the given name has
// been declared. Returning an error would be more strict but the
// kit prefers progressive disclosure: callers can attach examples
// alongside refactors that move parameter declarations into
// shared helpers without coordinating the two changes.
func WithParameterExample(name string, example any) RouteOption {
	return func(c *routeConfig) error {
		if name == "" {
			return errors.New("openapigen: WithParameterExample: empty parameter name")
		}
		if c.parameterExamples == nil {
			c.parameterExamples = map[string]any{}
		}
		c.parameterExamples[name] = example
		return nil
	}
}

// WithSkipPathParamDiscovery suppresses the auto-discovery of path
// parameters from the OAS template added in wave 162. By default the
// Register call extracts `{name}` segments from the path and adds
// them as required string-typed path parameters; callers that have
// already declared every path parameter via [WithParameter] (e.g.
// with richer typing or descriptions) and want to verify their
// declarations are complete can use this option to opt out.
//
// The discovery is also a safe no-op when the caller's
// [WithParameter] declarations cover every path segment — the merge
// suppresses any auto-entry that would shadow a declared one. This
// option is therefore only needed when the caller wants the auto
// path to be visible at a code-review level rather than functionally
// disabled.
func WithSkipPathParamDiscovery() RouteOption {
	return func(c *routeConfig) error {
		c.skipParamDiscovery = true
		return nil
	}
}

// WithParameter appends one parameter to the operation. Path /
// query / header / cookie parameters all go through this option.
//
// As of wave 162, path parameters declared in the OAS template
// (`{name}` segments) are auto-discovered and emitted as required
// string-typed parameters. Declaring the same name with
// WithParameter overrides the auto-entry — the explicit declaration
// wins so callers can attach richer schemas, examples, or
// descriptions. Use [WithSkipPathParamDiscovery] to disable the
// auto-discovery entirely.
func WithParameter(p Parameter) RouteOption {
	return func(c *routeConfig) error {
		if p.Name == "" {
			return errors.New("openapigen: WithParameter requires a non-empty name")
		}
		switch p.In {
		case "query", "header", "path", "cookie":
		default:
			return fmt.Errorf("openapigen: WithParameter requires a valid `in` value (got %q, expected query|header|path|cookie)", p.In)
		}
		if p.In == "path" && !p.Required {
			// Per OAS 3.1, path parameters are always required.
			p.Required = true
		}
		c.parameters = append(c.parameters, p)
		return nil
	}
}

// WithRequestType attaches the request body schema derived from T.
// The schema is generated via [validate.SchemaFor] and reflects the
// `jsonschema:"..."` struct tags.
//
// The body is recorded as required by default — callers that need an
// optional body must pair this with [WithRequestOptional].
func WithRequestType[T any]() RouteOption {
	return func(c *routeConfig) error {
		schema, err := schemaFor[T]()
		if err != nil {
			return err
		}
		c.requestSchema = schema
		c.requestRequired = true
		if c.requestMediaType == "" {
			c.requestMediaType = DefaultJSONMediaType
		}
		return nil
	}
}

// WithRequestSchema attaches an explicit request body schema. Use
// when the kit's [validate.SchemaFor] inference is not appropriate
// (e.g. the body is `application/x-www-form-urlencoded` and the kit
// has no struct tag for that path).
func WithRequestSchema(schema *jsonschema.Schema) RouteOption {
	return func(c *routeConfig) error {
		if schema == nil {
			return errors.New("openapigen: WithRequestSchema: nil schema")
		}
		c.requestSchema = schema
		c.requestRequired = true
		if c.requestMediaType == "" {
			c.requestMediaType = DefaultJSONMediaType
		}
		return nil
	}
}

// WithRequestMediaType overrides the request body media type.
// Defaults to "application/json".
func WithRequestMediaType(mediaType string) RouteOption {
	return func(c *routeConfig) error {
		if mediaType == "" {
			return errors.New("openapigen: WithRequestMediaType: empty media type")
		}
		c.requestMediaType = mediaType
		return nil
	}
}

// WithRequestDescription sets the request body description.
func WithRequestDescription(desc string) RouteOption {
	return func(c *routeConfig) error {
		c.requestDescription = desc
		return nil
	}
}

// WithRequestOptional flips the request body's required flag to
// false. Useful for endpoints where a body is allowed but not
// required (e.g. POST with optional JSON payload).
func WithRequestOptional() RouteOption {
	return func(c *routeConfig) error {
		c.requestRequired = false
		return nil
	}
}

// WithResponseType attaches a response body schema derived from T at
// the given HTTP status code. The schema is generated via
// [validate.SchemaFor].
//
// Calling with the same status twice replaces the previous schema.
func WithResponseType[T any](status int) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseType: invalid status %d", status)
		}
		schema, err := schemaFor[T]()
		if err != nil {
			return err
		}
		c.responseSchemas[status] = schema
		if _, ok := c.responseTypes[status]; !ok {
			c.responseTypes[status] = DefaultJSONMediaType
		}
		return nil
	}
}

// WithResponseSchema attaches an explicit response schema at status.
// Use when the kit's reflection is not appropriate (alternate media
// type, polymorphic response, …).
func WithResponseSchema(status int, schema *jsonschema.Schema) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseSchema: invalid status %d", status)
		}
		if schema == nil {
			return errors.New("openapigen: WithResponseSchema: nil schema")
		}
		c.responseSchemas[status] = schema
		if _, ok := c.responseTypes[status]; !ok {
			c.responseTypes[status] = DefaultJSONMediaType
		}
		return nil
	}
}

// WithResponseMediaType overrides the response media type at status.
// Defaults to "application/json".
func WithResponseMediaType(status int, mediaType string) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseMediaType: invalid status %d", status)
		}
		if mediaType == "" {
			return errors.New("openapigen: WithResponseMediaType: empty media type")
		}
		c.responseTypes[status] = mediaType
		return nil
	}
}

// WithResponseDescription overrides the response description at the
// given status code.
func WithResponseDescription(status int, desc string) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseDescription: invalid status %d", status)
		}
		c.responseDescriptions[status] = desc
		return nil
	}
}

// WithResponseStatus registers a status with a description but no
// body schema. Use for 204 No Content or other empty-body responses.
func WithResponseStatus(status int, desc string) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseStatus: invalid status %d", status)
		}
		c.responseDescriptions[status] = desc
		return nil
	}
}

// WithResponseContentT attaches an additional response body schema
// derived from T at the given status and media type. Unlike
// [WithResponseType] (which sets ONE schema per status), this option
// is additive: a single status may carry multiple content
// representations (e.g. `application/json` AND `application/xml`).
//
// Calling with the same (status, mediaType) replaces only that
// content entry. Mix with [WithResponseType] freely — the singular
// option contributes one entry, this option contributes additional
// entries, and both shapes are merged at render time.
func WithResponseContentT[T any](status int, mediaType string) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseContentT: invalid status %d", status)
		}
		if mediaType == "" {
			return errors.New("openapigen: WithResponseContentT: empty media type")
		}
		schema, err := schemaFor[T]()
		if err != nil {
			return err
		}
		ensureExtraContent(c, status)[mediaType] = schema
		return nil
	}
}

// WithResponseContent is the explicit-schema variant of
// [WithResponseContentT] for callers whose response shape is not
// inferrable via [validate.SchemaFor].
func WithResponseContent(status int, mediaType string, schema *jsonschema.Schema) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseContent: invalid status %d", status)
		}
		if mediaType == "" {
			return errors.New("openapigen: WithResponseContent: empty media type")
		}
		if schema == nil {
			return errors.New("openapigen: WithResponseContent: nil schema")
		}
		ensureExtraContent(c, status)[mediaType] = schema
		return nil
	}
}

// WithResponseHeader attaches a header to the response at the given
// status code. The OAS 3.1 `Response Object` allows arbitrary
// response headers — typical examples: `X-Rate-Limit-Remaining`,
// `Location`, `ETag`.
//
// Calling with the same (status, name) replaces only that header
// entry. Headers are emitted in their declared map without
// alphabetic sorting — operators wanting a stable order should
// register them in the order they want them serialised.
func WithResponseHeader(status int, name string, header Header) RouteOption {
	return func(c *routeConfig) error {
		if !validHTTPStatus(status) {
			return fmt.Errorf("openapigen: WithResponseHeader: invalid status %d", status)
		}
		if name == "" {
			return errors.New("openapigen: WithResponseHeader: empty header name")
		}
		if c.responseHeaders == nil {
			c.responseHeaders = map[int]map[string]Header{}
		}
		if c.responseHeaders[status] == nil {
			c.responseHeaders[status] = map[string]Header{}
		}
		c.responseHeaders[status][name] = header
		return nil
	}
}

func ensureExtraContent(c *routeConfig, status int) map[string]*jsonschema.Schema {
	if c.responseExtraContent == nil {
		c.responseExtraContent = map[int]map[string]*jsonschema.Schema{}
	}
	if c.responseExtraContent[status] == nil {
		c.responseExtraContent[status] = map[string]*jsonschema.Schema{}
	}
	return c.responseExtraContent[status]
}

// WithSecurity sets the per-operation `security` requirement,
// overriding any document-level requirement set via
// [Spec.SetGlobalSecurity]. Each map entry is one alternative; within
// a map, all entries must apply.
//
// Pass no arguments (`WithSecurity()`) to explicitly clear the
// global requirement for this operation (anonymous endpoint); the
// rendered document emits `"security": []` so OAS readers do not
// fall back to the document-level requirement.
func WithSecurity(req ...map[string][]string) RouteOption {
	return func(c *routeConfig) error {
		if len(req) == 0 {
			// Empty slice marks "no security required" — must be a
			// pointer to an empty slice, not nil, so JSON
			// marshalling emits the explicit `[]` rather than
			// omitting the field (which OAS readers treat as
			// "fall back to global").
			empty := []map[string][]string{}
			c.security = &empty
			return nil
		}
		clone := append([]map[string][]string(nil), req...)
		c.security = &clone
		return nil
	}
}

// validHTTPStatus accepts any value in the 100..599 inclusive range.
// OAS 3.1 also allows wildcard ranges ("2XX", "default") via separate
// keys; the kit does not currently surface those.
func validHTTPStatus(status int) bool {
	return status >= 100 && status <= 599
}
