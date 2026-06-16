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

// ErrInvalidPath is returned when a non-empty path does not start with
// "/". OAS 3.1 path templates are absolute; a relative path such as
// "widgets" yields an OAS-invalid document and, via [Handle], a mux
// pattern ("METHOD widgets") that net/http rejects with a panic.
// Rejecting it in [Spec.Register] keeps the spec and the mux
// consistent.
var ErrInvalidPath = errors.New("openapigen: path must start with \"/\"")

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
	method string
	path   string
	op     Operation
	// responses is the working map; flushed into op.Responses at render.
	responses map[int]Response
	// requestSchema is held separately so subsequent option calls
	// (e.g. WithRequestRequired) can mutate the same MediaType.
	requestSchema *jsonschema.Schema
	requestType   string
	requestDesc   string
	requestReq    bool
	// requestExamples is keyed by request media type. Folded into
	// the RequestBody's MediaType.Example at render time. Populated
	// from cfg.requestExamples by Register.
	requestExamples map[string]any
}

// NewSpec constructs a new Spec with the supplied title and version.
// Both are required by the OpenAPI 3.1 spec; empty strings are
// rejected at boot.
func NewSpec(title, version string) *Spec {
	if title == "" {
		panic("openapigen: NewSpec requires a non-empty title")
	}
	if version == "" {
		panic("openapigen: NewSpec requires a non-empty version")
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
		panic("openapigen: AddSecurityScheme requires a non-empty name")
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
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidPath, path)
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
	rs.op.Parameters = applyParameterExamples(
		mergePathParameters(path, cfg.parameters, cfg.skipParamDiscovery),
		cfg.parameterExamples,
	)
	rs.op.Deprecated = cfg.deprecated
	rs.op.Security = cfg.security
	rs.op.ExternalDocs = cfg.externalDocs

	if cfg.requestSchema != nil {
		rs.requestSchema = cfg.requestSchema
		rs.requestType = cfg.requestMediaType
		rs.requestDesc = cfg.requestDescription
		rs.requestReq = cfg.requestRequired
		if rs.requestType == "" {
			rs.requestType = DefaultJSONMediaType
		}
	}
	if len(cfg.requestExamples) > 0 {
		rs.requestExamples = make(map[string]any, len(cfg.requestExamples))
		for k, v := range cfg.requestExamples {
			rs.requestExamples[k] = v
		}
	}

	// Collect every status that has any registration contributing
	// to it — singular schema, extra content entries, header
	// declarations, or just a description. We walk every source so
	// the merge below sees the union, regardless of registration
	// order.
	statusSet := map[int]struct{}{}
	for status := range cfg.responseSchemas {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseExtraContent {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseHeaders {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseDescriptions {
		statusSet[status] = struct{}{}
	}
	for status := range cfg.responseExamples {
		statusSet[status] = struct{}{}
	}

	for status := range statusSet {
		desc := cfg.responseDescriptions[status]
		if desc == "" {
			desc = defaultResponseDescription(status)
		}

		var content map[string]MediaType
		examplesForStatus := cfg.responseExamples[status]
		// Singular schema contributes one entry under the configured
		// media type (default application/json).
		if schema, ok := cfg.responseSchemas[status]; ok {
			mediaType := cfg.responseTypes[status]
			if mediaType == "" {
				mediaType = DefaultJSONMediaType
			}
			entry := MediaType{Schema: schema}
			if ex, ok := examplesForStatus[mediaType]; ok {
				entry.Example = ex
			}
			content = map[string]MediaType{
				mediaType: entry,
			}
		}
		// Extra-content entries append (and replace any duplicate
		// media-type entry from the singular shape — last write
		// wins, mirroring how options compose elsewhere).
		if extras, ok := cfg.responseExtraContent[status]; ok && len(extras) > 0 {
			if content == nil {
				content = make(map[string]MediaType, len(extras))
			}
			for mt, schema := range extras {
				entry := MediaType{Schema: schema}
				if ex, ok := examplesForStatus[mt]; ok {
					entry.Example = ex
				}
				content[mt] = entry
			}
		}
		// Examples for media types that have no schema entry still
		// attach to a MediaType node so generated portals see them.
		// This is the "schema-less example" case for callers who
		// document the shape via description alone.
		for mt, ex := range examplesForStatus {
			if _, has := content[mt]; has {
				continue
			}
			if content == nil {
				content = map[string]MediaType{}
			}
			content[mt] = MediaType{Example: ex}
		}

		var headers map[string]Header
		if hs, ok := cfg.responseHeaders[status]; ok && len(hs) > 0 {
			headers = make(map[string]Header, len(hs))
			for name, h := range hs {
				headers[name] = h
			}
		}

		rs.responses[status] = Response{
			Description: desc,
			Headers:     headers,
			Content:     content,
		}
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
	// rs.op is a shallow struct copy; its Tags/Parameters slices and
	// Security pointer still alias routeState. Detach them so a caller
	// that mutates the returned Document (per the "caller owns the
	// result" contract on Document) cannot corrupt the Spec.
	if len(op.Tags) > 0 {
		op.Tags = append([]string(nil), op.Tags...)
	}
	if len(op.Parameters) > 0 {
		op.Parameters = append([]Parameter(nil), op.Parameters...)
	}
	if op.Security != nil {
		clone := cloneSecurity(*op.Security)
		op.Security = &clone
	}
	if rs.requestSchema != nil {
		mt := MediaType{Schema: rs.requestSchema}
		if ex, ok := rs.requestExamples[rs.requestType]; ok {
			mt.Example = ex
		}
		op.RequestBody = &RequestBody{
			Description: rs.requestDesc,
			Required:    rs.requestReq,
			Content: map[string]MediaType{
				rs.requestType: mt,
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
			op.Responses[strconv.Itoa(st)] = cloneResponse(rs.responses[st])
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

// cloneResponse returns a copy of the response with detached Headers
// and Content maps so a caller that mutates the returned Document does
// not reach back into the Spec's routeState. The contained
// *jsonschema.Schema pointers are shared (treated as immutable, the
// same tradeoff cloneComponents makes).
func cloneResponse(in Response) Response {
	out := Response{Description: in.Description}
	if len(in.Headers) > 0 {
		out.Headers = make(map[string]Header, len(in.Headers))
		for k, v := range in.Headers {
			out.Headers[k] = v
		}
	}
	if len(in.Content) > 0 {
		out.Content = make(map[string]MediaType, len(in.Content))
		for k, v := range in.Content {
			out.Content[k] = v
		}
	}
	return out
}

// cloneSecurity returns a deep copy of the security requirement. The
// empty-vs-nil distinction of each scope slice is preserved: a scheme
// registered with an empty (non-nil) scope list must render as
// `"scheme": []`, not `"scheme": null`.
func cloneSecurity(in []map[string][]string) []map[string][]string {
	out := make([]map[string][]string, len(in))
	for i, m := range in {
		clone := make(map[string][]string, len(m))
		for k, v := range m {
			if v == nil {
				clone[k] = nil
				continue
			}
			clone[k] = append([]string{}, v...)
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
// uniformly. The wrapping preserves the underlying error chain so
// `errors.Is(err, validate.ErrCyclicSchema)` continues to work.
func schemaFor[T any]() (*jsonschema.Schema, error) {
	schema, err := validate.SchemaFor[T]()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSchemaGeneration, reflect.TypeOf((*T)(nil)).Elem(), err)
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

// applyParameterExamples writes the wave-163 [WithParameterExample]
// values onto the matching Parameter entries by name. Parameters
// without a matching example pass through unchanged. The function
// allocates a new slice rather than mutating in place so the route
// state never aliases option-layer config.
func applyParameterExamples(params []Parameter, examples map[string]any) []Parameter {
	if len(examples) == 0 || len(params) == 0 {
		return params
	}
	out := make([]Parameter, len(params))
	for i, p := range params {
		if ex, ok := examples[p.Name]; ok {
			p.Example = ex
		}
		out[i] = p
	}
	return out
}

// mergePathParameters auto-discovers path parameters from the OAS
// path template (`{name}` segments) and merges them with caller-
// declared parameters. Caller-declared path parameters take
// precedence: if WithParameter has already declared a richer schema
// for `id`, the auto-discovered entry is suppressed. Non-path
// parameters (query, header, cookie) pass through unchanged.
//
// The discovery exists because Go's net/http pattern grammar uses
// the same `{name}` template the OAS spec does — registering
// `/users/{id}` against the mux and then manually re-declaring `id`
// as a Parameter is rote work that bloats Register call sites
// without adding information.
//
// Skip semantics: callers that want full control (e.g. their path
// uses `{name}` for non-OAS purposes) can pass
// [WithSkipPathParamDiscovery]. The auto-discovery is best-effort —
// malformed templates produce no auto-parameters rather than an
// error so adoption is incremental.
func mergePathParameters(path string, declared []Parameter, skip bool) []Parameter {
	merged := append([]Parameter(nil), declared...)
	if skip {
		return merged
	}
	names := extractPathParamNames(path)
	if len(names) == 0 {
		return merged
	}
	// Build the index of caller-declared path parameter names once
	// to avoid an O(n*m) scan when many parameters are declared.
	declaredPath := map[string]struct{}{}
	for _, p := range declared {
		if p.In == "path" {
			declaredPath[p.Name] = struct{}{}
		}
	}
	for _, name := range names {
		if _, ok := declaredPath[name]; ok {
			continue
		}
		merged = append(merged, Parameter{
			Name:     name,
			In:       "path",
			Required: true,
			Schema:   defaultStringSchema(),
		})
	}
	return merged
}

// extractPathParamNames parses an OAS path template for `{name}`
// segments. Returns names in registration order. Unbalanced braces
// or names containing special tokens (`.`, `:`, `/`) are skipped.
//
// Notable shapes:
//
//   - `/users/{id}` → `["id"]`
//   - `/users/{id}/orders/{orderID}` → `["id", "orderID"]`
//   - `/{$}` → `[]` (Go 1.22 end-of-path marker, not a parameter)
//   - `/static/{path...}` → `["path"]` (catch-all stripped; the
//     trailing `...` is Go's wildcard and is not OAS-spec, but the
//     parameter name itself is well-defined)
func extractPathParamNames(path string) []string {
	var names []string
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			continue
		}
		end := strings.IndexByte(path[i+1:], '}')
		if end < 0 {
			break
		}
		raw := path[i+1 : i+1+end]
		i += 1 + end
		// Skip Go's end-of-path marker.
		if raw == "$" {
			continue
		}
		// Strip Go's catch-all wildcard suffix.
		raw = strings.TrimSuffix(raw, "...")
		if raw == "" {
			continue
		}
		// Reject names with characters that would conflict with
		// path-parameter validation downstream.
		if strings.ContainsAny(raw, ".:/{}") {
			continue
		}
		names = append(names, raw)
	}
	return names
}

// defaultStringSchema returns a fresh `{"type": "string"}` schema
// used as the default for auto-discovered path parameters. Each call
// returns a new pointer so callers that subsequently mutate the
// schema do not affect other parameters.
func defaultStringSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string"}
}
