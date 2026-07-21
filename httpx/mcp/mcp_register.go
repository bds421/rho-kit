package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bds421/rho-kit/core/v2/validate"
)

// Register adds a [Handler] as an MCP tool with the given name.
//
// Schema generation: the kit derives the input/output JSON-Schema via
// [validate.SchemaFor], which reads `jsonschema:"..."` struct tags.
// The marshalled schema becomes the `inputSchema`/`outputSchema` on
// the SDK [sdkmcp.Tool]. Using the kit's generator (rather than the
// SDK's built-in jsonschema-go inference) keeps the catalog
// consistent with what [validate.Struct] will enforce after decode.
//
// Register returns an error when:
//   - the input or output type contains a cycle (self-reference);
//   - the input or output type cannot be reflected into a schema;
//   - the name is empty, too long, or outside the supported tool-name grammar;
//   - the name has already been registered.
func Register[In any, Out any](s *Server, name string, h Handler[In, Out], opts ...ToolOption) error {
	if s == nil {
		return errors.New("mcp: Register: server must not be nil")
	}
	if h == nil {
		return errors.New("mcp: Register: handler must not be nil")
	}

	cfg := toolConfig{}
	for _, o := range opts {
		if o == nil {
			panic("mcp: Register option must not be nil")
		}
		o(&cfg)
	}

	if err := validateToolName(name); err != nil {
		return err
	}

	var inZero In

	inSchema, err := resolveInputSchema[In](cfg.inputSchema)
	if err != nil {
		return fmt.Errorf("mcp: Register: input schema: %w", err)
	}
	outSchema, err := resolveOutputSchema[Out](cfg.outputSchema)
	if err != nil {
		return fmt.Errorf("mcp: Register: output schema: %w", err)
	}

	desc := cfg.description
	if desc == "" {
		desc = defaultDescription(reflect.TypeOf(inZero), name)
	}

	if cfg.destructive {
		// Vendor-extension: clients that understand the kit's MCP
		// dialect see `x-destructive: true` and can prompt for
		// confirmation. The kit-side gate is the actual enforcement.
		schema, err := withVendorExtension(inSchema, "x-destructive", true)
		if err != nil {
			return fmt.Errorf("mcp: Register: annotate input schema: %w", err)
		}
		inSchema = schema
	}

	// Reserve the registration slot first so concurrent
	// Register(same-name) races are caught before the SDK side-effect.
	s.mu.Lock()
	if _, dup := s.toolMeta[name]; dup {
		s.mu.Unlock()
		return fmt.Errorf("mcp: Register: tool already registered")
	}
	s.toolMeta[name] = &toolMeta{destructive: cfg.destructive}
	s.tools = append(s.tools, Tool{
		Name:         name,
		Description:  desc,
		InputSchema:  inSchema,
		OutputSchema: outSchema,
	})
	s.mu.Unlock()

	// Use the SDK's low-level Server.AddTool with a kit-owned
	// ToolHandler. We avoid sdkmcp.AddTool[In, Out] because that
	// generic helper unmarshals arguments with internaljson and
	// surfaces the raw decode error string back to the caller —
	// breaking the kit's "do not leak caller-controlled bytes" invariant
	// (security review L-4 / decode-failure tests). Owning decode here
	// lets us rewrite the error to a stable "invalid arguments" message
	// while logging the verbose form server-side.
	sdkTool := &sdkmcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: inSchema,
	}
	if len(outSchema) > 0 {
		sdkTool.OutputSchema = outSchema
	}
	if cfg.destructive {
		destHint := true
		sdkTool.Annotations = &sdkmcp.ToolAnnotations{
			DestructiveHint: &destHint,
		}
	}

	// Commit kit catalog only after SDK registration succeeds. AddTool
	// panics on non-object/missing schemas; pre-validation makes that
	// unreachable today, but a future SDK change must not leave the kit
	// catalog advertising a tool the SDK never received.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.mu.Lock()
				delete(s.toolMeta, name)
				// drop the trailing tools entry we just appended
				if n := len(s.tools); n > 0 && s.tools[n-1].Name == name {
					s.tools = s.tools[:n-1]
				}
				s.mu.Unlock()
				panic(rec)
			}
		}()
		s.sdk.AddTool(sdkTool, wrapToolHandler[In, Out](s, name, h, cfg.destructive))
	}()
	return nil
}

// resolveInputSchema returns the JSON-Schema bytes to set on the SDK
// Tool's InputSchema. When the caller supplied an override we validate
// that it's a JSON object and reuse it; otherwise we generate from In
// via the kit's validate package.
func resolveInputSchema[In any](override json.RawMessage) (json.RawMessage, error) {
	if len(override) > 0 {
		return validateSchemaOverride("input", override)
	}
	schema, err := validate.SchemaFor[In]()
	if err != nil {
		return nil, mapSchemaError(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	if err := requireObjectSchema("input", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func resolveOutputSchema[Out any](override json.RawMessage) (json.RawMessage, error) {
	if len(override) > 0 {
		return validateSchemaOverride("output", override)
	}
	schema, err := validate.SchemaFor[Out]()
	if err != nil {
		return nil, mapSchemaError(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	if err := requireObjectSchema("output", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// mapSchemaError translates the validate package's schema-generation
// errors back onto the kit's public sentinels so callers can still
// branch on errors.Is(err, ErrCyclicSchema) / ErrUnsupportedType.
func mapSchemaError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, validate.ErrCyclicSchema) {
		return fmt.Errorf("%w: %v", ErrCyclicSchema, sanitiseSchemaErr(err))
	}
	if errors.Is(err, validate.ErrUnsupportedType) {
		return fmt.Errorf("%w: %v", ErrUnsupportedType, sanitiseSchemaErr(err))
	}
	return err
}

// sanitiseSchemaErr strips any reflect.Type names from the validate
// package's wrapper so the kit's "do not leak caller type names"
// invariant survives even when the validate package learns to embed
// type metadata in errors.
func sanitiseSchemaErr(err error) string {
	switch {
	case errors.Is(err, validate.ErrCyclicSchema):
		return "cyclic type reference"
	case errors.Is(err, validate.ErrUnsupportedType):
		return "unsupported type"
	default:
		return "schema build failed"
	}
}

func validateSchemaOverride(kind string, schema json.RawMessage) (json.RawMessage, error) {
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		return nil, fmt.Errorf("%s schema must be a valid JSON object: %w", kind, err)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s schema must be a JSON object", kind)
	}
	// The SDK's AddTool panics unless the schema's "type" is exactly the
	// string "object". An override that omits the key, or sets it to a
	// non-string value, would otherwise reach AddTool and crash the
	// caller after the registration slot was already reserved — so we
	// require the canonical form up front and return an error instead.
	if typ, ok := obj["type"].(string); !ok || typ != "object" {
		return nil, fmt.Errorf("%s schema must have type \"object\"", kind)
	}
	return append(json.RawMessage(nil), schema...), nil
}

// requireObjectSchema asserts that a marshalled JSON-Schema declares
// `"type": "object"`. The MCP SDK's AddTool panics on any other shape
// (scalar, array, type-less); validate.SchemaFor emits a non-object
// schema for non-struct In/Out types (string, int, slice, time.Time,
// json.RawMessage). Catching it here lets Register honour its
// documented error contract instead of panicking after the catalog
// slot has been reserved.
func requireObjectSchema(kind string, schema json.RawMessage) error {
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		return fmt.Errorf("%s schema must be a valid JSON object: %w", kind, err)
	}
	if typ, ok := obj["type"].(string); !ok || typ != "object" {
		return fmt.Errorf("%s schema must have type \"object\" (only struct, map, and pointer-to-struct %s types are supported)", kind, kind)
	}
	return nil
}

func validActionLogTextField(s string, maxLen int, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// defaultDescription derives a description from the input type's name.
func defaultDescription(in reflect.Type, name string) string {
	if in == nil || in.Name() == "" {
		return "Tool: " + name
	}
	return "Tool " + name + " (input: " + in.Name() + ")"
}

// withVendorExtension parses an existing schema, sets a top-level
// extension key, and re-emits it.
func withVendorExtension(schema json.RawMessage, key string, value any) (json.RawMessage, error) {
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj[key] = value
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return out, nil
}

// sanitiseReason prepares a handler error message for storage in an
// audit entry's Reason field. The action-log signed-store contract
// (data/actionlog) rejects any Reason that contains a NUL byte or
// invalid UTF-8 anywhere — a handler error wrapping raw caller bytes
// would otherwise make the append fail, silently dropping the audit
// entry for an executed tool call (and, in strict sync mode, masking
// the mapped caller message as a bare "internal error"). We replace
// invalid UTF-8 sequences with the Unicode replacement character,
// strip NUL bytes, then cap the length.
func sanitiseReason(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return truncateReason(s)
}

// truncateReason caps an error message at MaxReasonLength bytes.
func truncateReason(s string) string {
	if len(s) <= MaxReasonLength {
		return s
	}
	cut := s[:MaxReasonLength]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut + "..."
}

// validateToolName accepts the same ASCII identifier grammar as the
// pre-SDK kit. The SDK already enforces its own (broader) tool-name
// rule; the kit's stricter rule is preserved so action-log Action
// values continue to round-trip.
var toolNameAllowed = func(name string) error {
	switch {
	case name == "":
		return errors.New("mcp: Register: tool name must not be empty")
	case len(name) > MaxToolNameLen:
		return fmt.Errorf("mcp: Register: invalid tool name (max %d bytes)", MaxToolNameLen)
	case !utf8.ValidString(name):
		return errors.New("mcp: Register: invalid tool name (must be valid UTF-8)")
	}
	if !validToolNameRune(rune(name[0])) || name[0] == '.' || name[0] == '-' || name[0] == '_' || name[0] == '/' {
		return errors.New("mcp: Register: invalid tool name (must start with an alphanumeric)")
	}
	for _, r := range name {
		if !validToolNameRune(r) {
			return errors.New("mcp: Register: invalid tool name (allowed: alphanumeric, '.', '_', '-', '/')")
		}
	}
	return nil
}

func validateToolName(name string) error {
	return toolNameAllowed(name)
}

func validToolNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '-' || r == '/':
		return true
	default:
		return false
	}
}
