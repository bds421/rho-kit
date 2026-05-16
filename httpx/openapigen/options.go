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
type routeConfig struct {
	tags        []string
	summary     string
	description string
	operationID string
	parameters  []Parameter
	deprecated  bool
	security    []map[string][]string

	// Request body.
	requestSchema      *jsonschema.Schema
	requestMediaType   string
	requestDescription string
	requestRequired    bool

	// Responses keyed by status code.
	responseDescriptions map[int]string
	responseSchemas      map[int]*jsonschema.Schema
	responseTypes        map[int]string
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

// WithParameter appends one parameter to the operation. Path / query /
// header / cookie parameters all go through this option; the kit does
// not auto-discover parameters from Go's net/http pattern grammar
// because that grammar does not expose typed parameter metadata.
func WithParameter(p Parameter) RouteOption {
	return func(c *routeConfig) error {
		if p.Name == "" {
			return errors.New("openapigen: WithParameter: name must not be empty")
		}
		switch p.In {
		case "query", "header", "path", "cookie":
		default:
			return fmt.Errorf("openapigen: WithParameter: invalid `in` value %q (expected query|header|path|cookie)", p.In)
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
// `validate:"..."` / `jsonschema:"..."` struct tags.
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

// WithSecurity sets the per-operation `security` requirement,
// overriding any document-level requirement set via
// [Spec.SetGlobalSecurity]. Each map entry is one alternative; within
// a map, all entries must apply.
//
// Pass an empty slice (`WithSecurity()`) to explicitly clear the
// global requirement for this operation (anonymous endpoint).
func WithSecurity(req ...map[string][]string) RouteOption {
	return func(c *routeConfig) error {
		if len(req) == 0 {
			// Empty slice marks "no security required" — must NOT be nil
			// or OAS readers treat it as "fall back to global".
			c.security = []map[string][]string{}
			return nil
		}
		c.security = append([]map[string][]string(nil), req...)
		return nil
	}
}

// validHTTPStatus accepts any value in the 100..599 inclusive range.
// OAS 3.1 also allows wildcard ranges ("2XX", "default") via separate
// keys; the kit does not currently surface those.
func validHTTPStatus(status int) bool {
	return status >= 100 && status <= 599
}
