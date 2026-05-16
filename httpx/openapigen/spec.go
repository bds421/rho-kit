package openapigen

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	jsonschema "github.com/google/jsonschema-go/jsonschema"

	"github.com/bds421/rho-kit/core/v2/validate"
)

// OpenAPIVersion is the constant emitted as the `openapi` field of
// every document. Bump in lockstep with the kit-supported schema
// vocabulary.
const OpenAPIVersion = "3.1.0"

// DefaultJSONMediaType is the media type used for [WithRequestType] /
// [WithResponseType] schema attachments. The kit's typed handlers
// always read and write JSON.
const DefaultJSONMediaType = "application/json"

// ErrRouteAlreadyRegistered is returned by [Spec.Register] when a
// `<method>::<path>` pair is registered twice. Mux double-registration
// is the usual cause; surfaces at boot rather than after rollout.
var ErrRouteAlreadyRegistered = errors.New("openapigen: route already registered")

// ErrInvalidMethod is returned when the HTTP verb passed to a
// registration call is not a recognised OpenAPI verb.
var ErrInvalidMethod = errors.New("openapigen: invalid HTTP method")

// ErrEmptyPath is returned when an empty path string is passed to a
// registration call. The kit does not silently substitute "/".
var ErrEmptyPath = errors.New("openapigen: empty path")

// ErrSchemaGeneration wraps any error returned by [validate.SchemaFor]
// during registration so callers can [errors.Is] against it.
var ErrSchemaGeneration = errors.New("openapigen: schema generation failed")

// Spec accumulates registered routes and renders them as an OpenAPI
// 3.1 document.
//
// Construct via [NewSpec]. Methods are safe for concurrent use. The
// rendered document is cached in-memory and invalidated whenever the
// spec is mutated (registration, server/tag updates, …).
type Spec struct {
	mu         sync.RWMutex
	info       Info
	servers    []Server
	tags       []Tag
	security   []map[string][]string
	components *Components
	// routes is keyed by "<METHOD> <path>". Each entry contains the
	// full operation builder state.
	routes map[string]*routeState

	// cache stores the marshalled document; cleared on every mutation.
	cache       []byte
	cacheLoaded bool
}

// routeState captures the operation under construction for a single
// `<method, path>` pair. Stored in a map keyed by "<METHOD> <path>"
// so duplicate registration surfaces ErrRouteAlreadyRegistered.
type routeState struct {
	method      string
	path        string
	op          Operation
	// responses is the working map; flushed into op.Responses at render.
	responses map[int]Response
	// requestSchema is held separately so subsequent option calls
	// (e.g. WithRequestRequired) can mutate the same MediaType.
	requestSchema *jsonschema.Schema
	requestType   string
	requestDesc   string
	requestReq    bool
}

// NewSpec constructs a new Spec with the supplied title and version.
// Both are required by the OpenAPI 3.1 spec; empty strings are
// rejected at boot.
func NewSpec(title, version string) *Spec {
	if title == "" {
		panic("openapigen: NewSpec: title must not be empty")
	}
	if version == "" {
		panic("openapigen: NewSpec: version must not be empty")
	}
	return &Spec{
		info:   Info{Title: title, Version: version},
		routes: make(map[string]*routeState),
	}
}

// SetInfoDescription replaces the document description. Returns the
// spec for chainability.
func (s *Spec) SetInfoDescription(desc string) *Spec {
	s.mu.Lock()
	s.info.Description = desc
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// SetInfoSummary replaces the document summary. Returns the spec for
// chainability.
func (s *Spec) SetInfoSummary(summary string) *Spec {
	s.mu.Lock()
	s.info.Summary = summary
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// SetContact replaces the document contact metadata.
func (s *Spec) SetContact(c Contact) *Spec {
	s.mu.Lock()
	s.info.Contact = &c
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// SetLicense replaces the document license metadata.
func (s *Spec) SetLicense(l License) *Spec {
	s.mu.Lock()
	s.info.License = &l
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// AddServer appends a server entry.
func (s *Spec) AddServer(srv Server) *Spec {
	s.mu.Lock()
	s.servers = append(s.servers, srv)
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// AddTag appends a tag definition. Tag strings on operations
// reference these by name.
func (s *Spec) AddTag(tag Tag) *Spec {
	s.mu.Lock()
	s.tags = append(s.tags, tag)
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// AddSecurityScheme registers a reusable security scheme on the
// document `components.securitySchemes`. Operations reference these
// by name via [WithSecurity].
func (s *Spec) AddSecurityScheme(name string, scheme SecurityScheme) *Spec {
	if name == "" {
		panic("openapigen: AddSecurityScheme: name must not be empty")
	}
	s.mu.Lock()
	if s.components == nil {
		s.components = &Components{}
	}
	if s.components.SecuritySchemes == nil {
		s.components.SecuritySchemes = make(map[string]SecurityScheme)
	}
	s.components.SecuritySchemes[name] = scheme
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// SetGlobalSecurity sets the document-level `security` requirement.
// Operations may override per-operation via [WithSecurity].
func (s *Spec) SetGlobalSecurity(req []map[string][]string) *Spec {
	s.mu.Lock()
	s.security = req
	s.cacheLoaded = false
	s.mu.Unlock()
	return s
}

// Register adds an operation to the spec.
//
// method is the HTTP verb (case-insensitive — normalised internally).
// path is the OpenAPI path string (must start with "/").
//
// Register is the lowest-level entry point; [Handle] /
// [HandleStatus] / [HandleNoBody] / [HandleNoBodyStatus] call it
// internally with the appropriate generic schema lookups.
func (s *Spec) Register(method, path string, opts ...RouteOption) error {
	if path == "" {
		return ErrEmptyPath
	}
	normMethod, ok := normaliseMethod(method)
	if !ok {
		return fmt.Errorf("%w: %q", ErrInvalidMethod, method)
	}

	cfg := routeConfig{
		responseDescriptions: map[int]string{},
		responseSchemas:      map[int]*jsonschema.Schema{},
		responseTypes:        map[int]string{},
	}
	for _, o := range opts {
		if o == nil {
			return errors.New("openapigen: Register: option must not be nil")
		}
		if err := o(&cfg); err != nil {
			return err
		}
	}

	key := normMethod + " " + path

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.routes[key]; dup {
		return fmt.Errorf("%w: %s %s", ErrRouteAlreadyRegistered, normMethod, path)
	}

	rs := &routeState{
		method:    normMethod,
		path:      path,
		responses: map[int]Response{},
	}

	rs.op.Tags = append([]string(nil), cfg.tags...)
	rs.op.Summary = cfg.summary
	rs.op.Description = cfg.description
	rs.op.OperationID = cfg.operationID
	rs.op.Parameters = append([]Parameter(nil), cfg.parameters...)
	rs.op.Deprecated = cfg.deprecated
	rs.op.Security = cfg.security

	if cfg.requestSchema != nil {
		rs.requestSchema = cfg.requestSchema
		rs.requestType = cfg.requestMediaType
		rs.requestDesc = cfg.requestDescription
		rs.requestReq = cfg.requestRequired
		if rs.requestType == "" {
			rs.requestType = DefaultJSONMediaType
		}
	}

	for status, schema := range cfg.responseSchemas {
		mediaType := cfg.responseTypes[status]
		if mediaType == "" {
			mediaType = DefaultJSONMediaType
		}
		desc := cfg.responseDescriptions[status]
		if desc == "" {
			desc = defaultResponseDescription(status)
		}
		rs.responses[status] = Response{
			Description: desc,
			Content: map[string]MediaType{
				mediaType: {Schema: schema},
			},
		}
	}
	// Also pick up statuses that only have a description (e.g. 204).
	for status, desc := range cfg.responseDescriptions {
		if _, ok := rs.responses[status]; ok {
			continue
		}
		if desc == "" {
			desc = defaultResponseDescription(status)
		}
		rs.responses[status] = Response{Description: desc}
	}

	s.routes[key] = rs
	s.cacheLoaded = false

	return nil
}

// Marshal renders the OpenAPI 3.1 document as JSON. The result is
// cached in-memory; subsequent calls return the same bytes until the
// spec is mutated.
func (s *Spec) Marshal() ([]byte, error) {
	s.mu.RLock()
	if s.cacheLoaded {
		buf := append([]byte(nil), s.cache...)
		s.mu.RUnlock()
		return buf, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cacheLoaded {
		return append([]byte(nil), s.cache...), nil
	}

	doc := s.build()
	buf, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("openapigen: marshal document: %w", err)
	}
	s.cache = buf
	s.cacheLoaded = true
	return append([]byte(nil), buf...), nil
}

// Document returns a freshly-constructed [Document] representing the
// current spec state. The caller owns the result; subsequent
// mutations on the [Spec] are NOT reflected in the returned value.
func (s *Spec) Document() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.build()
}

// build constructs the on-wire Document under the spec's lock. The
// caller must hold s.mu.
func (s *Spec) build() Document {
	doc := Document{
		OpenAPI: OpenAPIVersion,
		Info:    s.info,
	}
	if len(s.servers) > 0 {
		doc.Servers = append([]Server(nil), s.servers...)
	}
	if len(s.tags) > 0 {
		doc.Tags = append([]Tag(nil), s.tags...)
	}
	if len(s.security) > 0 {
		doc.Security = cloneSecurity(s.security)
	}
	if s.components != nil {
		doc.Components = cloneComponents(s.components)
	}
	if len(s.routes) > 0 {
		doc.Paths = make(map[string]PathItem, len(s.routes))
		for _, rs := range s.routes {
			item := doc.Paths[rs.path]
			applyOperation(&item, rs)
			doc.Paths[rs.path] = item
		}
	}
	return doc
}

// applyOperation copies the routeState's accumulated operation onto
// the supplied PathItem, picking the verb slot by method.
func applyOperation(item *PathItem, rs *routeState) {
	op := rs.op
	if rs.requestSchema != nil {
		op.RequestBody = &RequestBody{
			Description: rs.requestDesc,
			Required:    rs.requestReq,
			Content: map[string]MediaType{
				rs.requestType: {Schema: rs.requestSchema},
			},
		}
	}
	if len(rs.responses) > 0 {
		op.Responses = make(map[string]Response, len(rs.responses))
		statuses := make([]int, 0, len(rs.responses))
		for st := range rs.responses {
			statuses = append(statuses, st)
		}
		sort.Ints(statuses)
		for _, st := range statuses {
			op.Responses[strconv.Itoa(st)] = rs.responses[st]
		}
	}
	switch rs.method {
	case http.MethodGet:
		item.Get = &op
	case http.MethodPut:
		item.Put = &op
	case http.MethodPost:
		item.Post = &op
	case http.MethodDelete:
		item.Delete = &op
	case http.MethodOptions:
		item.Options = &op
	case http.MethodHead:
		item.Head = &op
	case http.MethodPatch:
		item.Patch = &op
	case http.MethodTrace:
		item.Trace = &op
	}
}

// cloneSecurity returns a deep copy of the security requirement.
func cloneSecurity(in []map[string][]string) []map[string][]string {
	out := make([]map[string][]string, len(in))
	for i, m := range in {
		clone := make(map[string][]string, len(m))
		for k, v := range m {
			clone[k] = append([]string(nil), v...)
		}
		out[i] = clone
	}
	return out
}

// cloneComponents returns a deep copy of the components block.
func cloneComponents(in *Components) *Components {
	if in == nil {
		return nil
	}
	out := &Components{}
	if len(in.Schemas) > 0 {
		out.Schemas = make(map[string]*jsonschema.Schema, len(in.Schemas))
		for k, v := range in.Schemas {
			out.Schemas[k] = v
		}
	}
	if len(in.SecuritySchemes) > 0 {
		out.SecuritySchemes = make(map[string]SecurityScheme, len(in.SecuritySchemes))
		for k, v := range in.SecuritySchemes {
			out.SecuritySchemes[k] = v
		}
	}
	return out
}

// Handler returns an http.Handler that serves the rendered OpenAPI
// 3.1 JSON document. The handler only accepts GET / HEAD; other
// methods return 405.
//
// Services mount the handler explicitly:
//
//	mux.Handle("/openapi.json", spec.Handler())
//
// The kit does NOT mount it for you — exposing the spec is a service
// policy decision (some teams hide it behind auth; others expose
// publicly).
func (s *Spec) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		buf, err := s.Marshal()
		if err != nil {
			http.Error(w, "openapi: internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf)
	})
}

// schemaFor is a thin wrapper around [validate.SchemaFor] that wraps
// the returned error with [ErrSchemaGeneration] so callers can branch
// uniformly.
func schemaFor[T any]() (*jsonschema.Schema, error) {
	schema, err := validate.SchemaFor[T]()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrSchemaGeneration, reflect.TypeOf((*T)(nil)).Elem(), err)
	}
	return schema, nil
}

// normaliseMethod uppercases the verb and confirms it is in the OAS
// 3.1 supported set.
func normaliseMethod(method string) (string, bool) {
	up := strings.ToUpper(strings.TrimSpace(method))
	switch up {
	case http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete,
		http.MethodOptions, http.MethodHead, http.MethodPatch, http.MethodTrace:
		return up, true
	}
	return "", false
}

// defaultResponseDescription produces a non-empty description for a
// status code so the rendered document satisfies the OAS 3.1 spec's
// "responses object MUST have a description" rule.
func defaultResponseDescription(status int) string {
	if t := http.StatusText(status); t != "" {
		return t
	}
	return "Response for HTTP " + strconv.Itoa(status)
}
