package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	jsonschemago "github.com/google/jsonschema-go/jsonschema"
)

// timeType is the canonical reflect.Type of time.Time, treated as a
// JSON string with `date-time` format by the inferred schema.
var timeType = reflect.TypeOf(time.Time{})

// rawMessageType maps json.RawMessage onto a schema that admits any
// JSON value, matching what the field literally encodes.
var rawMessageType = reflect.TypeOf(json.RawMessage{})

// builtSchema is the structSchema walker's result: the in-memory
// schema, the set of required-non-empty field paths (used for the
// "is required" message-rewriting), and the set of parametric format
// names that need registering with the compiler. fieldOrder records
// the declaration index of every property path; collectFieldErrors
// uses it to render field errors in struct-declaration order rather
// than the validator's (unspecified) iteration order.
type builtSchema struct {
	schema            *jsonschemago.Schema
	requiredNonEmpty  map[string]struct{}
	parametricFormats []string
	fieldOrder        map[string]int
}

// ErrCyclicSchema is returned when a struct field recursively
// references its own type. JSON-Schema cannot represent an unbounded
// cycle; the kit refuses to emit a schema rather than panic at
// validate time.
var ErrCyclicSchema = errors.New("validate: cyclic type reference")

// ErrUnsupportedType is returned when a Go type has no JSON-Schema
// equivalent (channels, functions, unsafe.Pointer, complex, non-string
// map key).
var ErrUnsupportedType = errors.New("validate: unsupported type")

// buildSchema returns the *builtSchema for t.
func buildSchema(t reflect.Type) (*builtSchema, error) {
	ctx := &buildCtx{
		visiting:         map[reflect.Type]bool{},
		requiredNonEmpty: map[string]struct{}{},
		parametric:       map[string]struct{}{},
		fieldOrder:       map[string]int{},
	}
	s, err := schemaForReflect(ctx, t, "", "")
	if err != nil {
		return nil, err
	}
	out := &builtSchema{
		schema:           s,
		requiredNonEmpty: ctx.requiredNonEmpty,
		fieldOrder:       ctx.fieldOrder,
	}
	for name := range ctx.parametric {
		out.parametricFormats = append(out.parametricFormats, name)
	}
	return out, nil
}

// buildCtx tracks state across the recursive walker: visit set for
// cycle detection, required-non-empty path collection, parametric
// format collection, and per-path declaration order for deterministic
// field-error ordering.
type buildCtx struct {
	visiting         map[reflect.Type]bool
	requiredNonEmpty map[string]struct{}
	parametric       map[string]struct{}
	fieldOrder       map[string]int
}

// schemaForReflect is the main recursive walker. constraintTag carries
// the `jsonschema:` tag inherited from the parent field (or "" for
// slice/map element schemas, which inherit no constraints). path is
// the dotted JSON-pointer path of this node, used to populate
// requiredNonEmpty.
func schemaForReflect(ctx *buildCtx, t reflect.Type, constraintTag string, path string) (*jsonschemago.Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		s := &jsonschemago.Schema{Type: "string", Format: "date-time"}
		applyStringConstraints(ctx, s, constraintTag)
		return s, nil
	}
	if t == rawMessageType {
		return &jsonschemago.Schema{}, nil
	}

	switch t.Kind() {
	case reflect.String:
		s := &jsonschemago.Schema{Type: "string"}
		applyStringConstraints(ctx, s, constraintTag)
		return s, nil
	case reflect.Bool:
		return &jsonschemago.Schema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		s := &jsonschemago.Schema{Type: "integer"}
		applyNumericConstraints(ctx, s, constraintTag)
		return s, nil
	case reflect.Float32, reflect.Float64:
		s := &jsonschemago.Schema{Type: "number"}
		applyNumericConstraints(ctx, s, constraintTag)
		return s, nil
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			// []byte marshals as a base64 string. encoding/json only
			// base64-encodes byte *slices*; a byte *array* ([16]byte
			// UUID, [32]byte hash) marshals as a JSON array of numbers,
			// so it falls through to the array-of-integer schema below.
			return &jsonschemago.Schema{Type: "string"}, nil
		}
		if ctx.visiting[t] {
			return nil, fmt.Errorf("%w: recursive array or slice type", ErrCyclicSchema)
		}
		ctx.visiting[t] = true
		defer delete(ctx.visiting, t)
		items, err := schemaForReflect(ctx, t.Elem(), "", path)
		if err != nil {
			return nil, err
		}
		s := &jsonschemago.Schema{Type: "array", Items: items}
		applyArrayConstraints(s, constraintTag)
		return s, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: map key must be string", ErrUnsupportedType)
		}
		if ctx.visiting[t] {
			return nil, fmt.Errorf("%w: recursive map type", ErrCyclicSchema)
		}
		ctx.visiting[t] = true
		defer delete(ctx.visiting, t)
		val, err := schemaForReflect(ctx, t.Elem(), "", path)
		if err != nil {
			return nil, err
		}
		return &jsonschemago.Schema{Type: "object", AdditionalProperties: val}, nil
	case reflect.Interface:
		return &jsonschemago.Schema{}, nil
	case reflect.Struct:
		return structSchema(ctx, t, path)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, t.Kind())
	}
}

// structSchema walks a struct type and returns the corresponding
// JSON-Schema object. Embedded structs flatten into the parent
// (matching encoding/json). Required fields are derived from the
// `jsonschema:"required"` keyword.
func structSchema(ctx *buildCtx, t reflect.Type, path string) (*jsonschemago.Schema, error) {
	if ctx.visiting[t] {
		return nil, fmt.Errorf("%w: recursive struct type", ErrCyclicSchema)
	}
	ctx.visiting[t] = true
	defer delete(ctx.visiting, t)

	out := &jsonschemago.Schema{
		Type:                 "object",
		Properties:           map[string]*jsonschemago.Schema{},
		AdditionalProperties: falseSchema(),
	}

	var required []string
	var order []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// Embedded (anonymous, no json tag) flattens into parent.
		// encoding/json promotes exported fields of an embedded
		// unexported-named struct as well (the struct field itself can
		// have an unexported name like `base`, but its `ID` member is
		// still emitted under the parent), so we check Anonymous BEFORE
		// the IsExported gate. Skipping unexported anonymous struct
		// fields here would diverge from json's marshalling shape.
		if f.Anonymous && f.Tag.Get("json") == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct {
				continue
			}
			emb, err := structSchema(ctx, ft, path)
			if err != nil {
				return nil, err
			}
			for _, name := range emb.PropertyOrder {
				if _, ok := out.Properties[name]; ok {
					continue
				}
				out.Properties[name] = emb.Properties[name]
				order = append(order, name)
			}
			required = append(required, emb.Required...)
			continue
		}
		if !f.IsExported() {
			continue
		}

		name, omitEmpty, skip := jsonFieldName(f)
		if skip {
			continue
		}
		jsTag := f.Tag.Get("jsonschema")
		childPath := name
		if path != "" {
			childPath = path + "." + name
		}
		field, err := schemaForReflect(ctx, f.Type, jsTag, childPath)
		if err != nil {
			return nil, err
		}
		// Description sources in order: `desc:` tag (kit
		// convention), `jsonschema:` tag's free-form residue (any
		// token that is not a recognised constraint keyword). The
		// latter wins when both are present so callers can migrate
		// from `desc:` to `jsonschema:` field by field.
		if d := f.Tag.Get("desc"); d != "" {
			field.Description = d
		}
		if d := descriptionFromJSONSchemaTag(jsTag); d != "" {
			field.Description = d
		}
		isRequired := jsonschemaTagHasRequired(jsTag)
		if isRequired {
			required = append(required, name)
			// For string/array required fields, also enforce non-
			// empty. The error renderer maps a minLength / minItems
			// violation on a path in requiredNonEmpty back to
			// "is required" so the same wording survives even when
			// the empty value was rejected by a stricter min rule
			// (e.g. `required,min=2`).
			markRequiredNonEmpty(ctx, field, childPath)
		}
		// A nil pointer/slice/map field marshals as JSON `null` when the
		// json tag has no omitempty, so the inferred schema must admit
		// null for those fields — otherwise an absent optional value is
		// rejected with "must be array/object/string". Required fields
		// keep the strict single type so null (the zero value) still
		// fails; omitempty fields never emit null, so they need no
		// widening either. A field that declares a positive lower bound
		// (min/len → minItems/minLength) is treated as wanting a present,
		// non-empty value, so null stays rejected there too.
		if !isRequired && !omitEmpty && marshalsAsJSONNull(f.Type) && !hasPositiveLowerBound(field) {
			admitNull(field)
		}
		out.Properties[name] = field
		order = append(order, name)
		ctx.fieldOrder[childPath] = len(ctx.fieldOrder)
	}

	if len(required) > 0 {
		out.Required = required
	}
	if len(order) > 0 {
		out.PropertyOrder = order
	}
	return out, nil
}

// marshalsAsJSONNull reports whether a nil value of type t encodes to
// JSON `null` under encoding/json. Pointers, slices, and maps all
// marshal their nil value as null; fixed-size arrays cannot be nil and
// always marshal as a JSON array, so they are excluded. Interfaces are
// not considered here: the walker already emits the permissive empty
// schema for them, which admits null.
func marshalsAsJSONNull(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		return true
	default:
		return false
	}
}

// hasPositiveLowerBound reports whether a schema declares a presence
// constraint (minItems / minLength) greater than zero. Such a field is
// treated as wanting a non-empty value, so a nil (null) instance must
// still fail rather than be admitted by null-widening.
func hasPositiveLowerBound(s *jsonschemago.Schema) bool {
	if s.MinItems != nil && *s.MinItems > 0 {
		return true
	}
	if s.MinLength != nil && *s.MinLength > 0 {
		return true
	}
	return false
}

// admitNull widens a property schema so a JSON `null` is accepted in
// addition to the field's inferred type. The type-specific keywords
// (minLength, minItems, properties, ...) only apply to instances of
// their own type under JSON Schema, so adding "null" to the type list
// does not relax the constraints that guard a non-null value. A schema
// with no declared type already admits any value (including null) and
// is left untouched.
func admitNull(s *jsonschemago.Schema) {
	switch {
	case s.Type != "":
		if s.Type == "null" {
			return
		}
		s.Types = []string{s.Type, "null"}
		s.Type = ""
	case len(s.Types) > 0:
		for _, t := range s.Types {
			if t == "null" {
				return
			}
		}
		s.Types = append(s.Types, "null")
	}
}

// markRequiredNonEmpty records the field's path so the error renderer
// can phrase minLength / minItems violations as "is required" when
// the offending value is empty. It also lifts the floor to 1 when no
// explicit min was set, so an empty string on a `required` field
// fails validation in the first place.
func markRequiredNonEmpty(ctx *buildCtx, s *jsonschemago.Schema, path string) {
	switch s.Type {
	case "string":
		if s.MinLength == nil {
			one := 1
			s.MinLength = &one
		}
		ctx.requiredNonEmpty[path] = struct{}{}
	case "array":
		if s.MinItems == nil {
			one := 1
			s.MinItems = &one
		}
		ctx.requiredNonEmpty[path] = struct{}{}
	}
}

// falseSchema returns a schema that admits no value, used as the
// AdditionalProperties bound so unknown properties fail validation.
// Matches the DisallowUnknownFields decoder behaviour used by the
// httpx and MCP transports. The marshalled form is `{"not": {}}`,
// which santhosh-tekuri treats as never-valid — equivalent to
// `"additionalProperties": false` for our purposes.
func falseSchema() *jsonschemago.Schema {
	return &jsonschemago.Schema{Not: &jsonschemago.Schema{}}
}

// jsonFieldName returns (name, omitempty, skip) using the same rules
// as encoding/json. Skip is true for `json:"-"`.
func jsonFieldName(f reflect.StructField) (string, bool, bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	if tag == "" {
		return f.Name, false, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = f.Name
	}
	omitEmpty := false
	for _, p := range parts[1:] {
		if p == "omitempty" || p == "omitzero" {
			omitEmpty = true
		}
	}
	return name, omitEmpty, false
}

// jsonschemaTagHasRequired reports whether a `jsonschema:"..."` tag
// declares the field as required via the explicit keyword form
// (`jsonschema:"required"` or `jsonschema:"required,..."`). This is a
// kit extension on top of jsonschema-go's description-only convention,
// so callers can express requirement directly on the jsonschema tag.
func jsonschemaTagHasRequired(tag string) bool {
	if tag == "" {
		return false
	}
	for _, part := range splitTagFields(tag) {
		if strings.TrimSpace(part) == "required" {
			return true
		}
	}
	return false
}

// constraintKeywords names every `key` (in `key=value` or bare-keyword
// form) that the kit's tag parser recognises as a JSON-Schema
// constraint. Tokens whose key is in this set are stripped from the
// tag before the residue is treated as a free-form description.
var constraintKeywords = map[string]struct{}{
	"required":    {},
	"min":         {},
	"max":         {},
	"len":         {},
	"gte":         {},
	"lte":         {},
	"gt":          {},
	"lt":          {},
	"oneof":       {},
	"pattern":     {},
	"format":      {},
	"email":       {},
	"url":         {},
	"uri":         {},
	"uuid":        {},
	"uuid4":       {},
	"ip":          {},
	"ipv4":        {},
	"ipv6":        {},
	"hostname":    {},
	"alpha":       {},
	"alphanum":    {},
	"numeric":     {},
	"cidr":        {},
	"datetime":    {},
	"startswith":  {},
	"endswith":    {},
	"contains":    {},
	"excludesall": {},
	"unique":      {},
}

// descriptionFromJSONSchemaTag returns the free-form description
// portion of a `jsonschema:"..."` tag. Every comma-separated segment
// whose `key` matches a known constraint keyword is stripped; the
// remaining segments are joined back with commas so a description
// containing a literal comma survives (`jsonschema:"required,One,
// two"` → "One, two"). This makes the description an opt-in fallback
// for any token the constraint parser did not consume.
func descriptionFromJSONSchemaTag(tag string) string {
	if tag == "" {
		return ""
	}
	parts := splitTagFields(tag)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		key, _ := splitRule(strings.TrimSpace(p))
		if _, ok := constraintKeywords[key]; ok {
			continue
		}
		out = append(out, p)
	}
	desc := strings.TrimSpace(strings.Join(out, ","))
	return desc
}

// applyStringConstraints maps `jsonschema:"..."` rules that apply to
// a string-typed schema (length, format, pattern). Numeric or
// array-only rules are silently ignored — applyNumericConstraints /
// applyArrayConstraints handle those.
func applyStringConstraints(ctx *buildCtx, s *jsonschemago.Schema, tag string) {
	if tag == "" {
		return
	}
	for _, raw := range splitTagFields(tag) {
		rule := strings.TrimSpace(raw)
		if rule == "" || rule == "required" {
			continue
		}
		key, value := splitRule(rule)
		if _, ok := constraintKeywords[key]; !ok {
			// Free-form text — handled by descriptionFromJSONSchemaTag.
			continue
		}
		switch key {
		case "min":
			if n, ok := atoi(value); ok {
				s.MinLength = ptrInt(n)
			}
		case "max":
			if n, ok := atoi(value); ok {
				s.MaxLength = ptrInt(n)
			}
		case "len":
			if n, ok := atoi(value); ok {
				s.MinLength = ptrInt(n)
				s.MaxLength = ptrInt(n)
			}
		case "oneof":
			s.Enum = parseEnum(s.Type, value)
		case "pattern":
			s.Pattern = value
		case "format":
			s.Format = value
			if isParametricFormatName(value) {
				ctx.parametric[value] = struct{}{}
			}
		case "email", "url", "uri", "uuid", "uuid4", "ip", "ipv4", "ipv6",
			"hostname", "alpha", "alphanum", "numeric", "cidr", "datetime":
			s.Format = canonicalFormatName(key)
		case "startswith", "endswith", "contains", "excludesall":
			name := parametricName(key, value)
			s.Format = name
			ctx.parametric[name] = struct{}{}
		}
	}
}

// applyNumericConstraints maps `jsonschema:"..."` rules that apply to
// integer/number-typed schemas (min/max value, gte/lte, gt/lt,
// oneof, format).
func applyNumericConstraints(ctx *buildCtx, s *jsonschemago.Schema, tag string) {
	if tag == "" {
		return
	}
	for _, raw := range splitTagFields(tag) {
		rule := strings.TrimSpace(raw)
		if rule == "" || rule == "required" {
			continue
		}
		key, value := splitRule(rule)
		if _, ok := constraintKeywords[key]; !ok {
			// Free-form text — handled by descriptionFromJSONSchemaTag.
			// Custom RegisterFormat formats are wired via explicit
			// `format=<name>` rather than the bare keyword form to keep
			// the description fallback unambiguous.
			continue
		}
		switch key {
		case "min", "gte":
			if f, ok := atof(value); ok {
				s.Minimum = ptrFloat(f)
			}
		case "max", "lte":
			if f, ok := atof(value); ok {
				s.Maximum = ptrFloat(f)
			}
		case "gt":
			if f, ok := atof(value); ok {
				s.ExclusiveMinimum = ptrFloat(f)
			}
		case "lt":
			if f, ok := atof(value); ok {
				s.ExclusiveMaximum = ptrFloat(f)
			}
		case "oneof":
			s.Enum = parseEnum(s.Type, value)
		case "format":
			s.Format = value
			if isParametricFormatName(value) {
				ctx.parametric[value] = struct{}{}
			}
		}
	}
}

// isParametricFormatName reports whether the name follows the kit's
// parametric format convention (`<prefix>:<argument>`). The walker
// uses this to schedule parametric Format registration with the
// compiler.
func isParametricFormatName(name string) bool {
	return strings.HasPrefix(name, "starts-with:") ||
		strings.HasPrefix(name, "ends-with:") ||
		strings.HasPrefix(name, "contains:") ||
		strings.HasPrefix(name, "excludes-all:")
}

// applyArrayConstraints maps slice/array constraints (length and
// uniqueness rules) onto the schema.
func applyArrayConstraints(s *jsonschemago.Schema, tag string) {
	if tag == "" {
		return
	}
	for _, raw := range splitTagFields(tag) {
		rule := strings.TrimSpace(raw)
		if rule == "" || rule == "required" {
			continue
		}
		key, value := splitRule(rule)
		if _, ok := constraintKeywords[key]; !ok {
			continue
		}
		switch key {
		case "min":
			if n, ok := atoi(value); ok {
				s.MinItems = ptrInt(n)
			}
		case "max":
			if n, ok := atoi(value); ok {
				s.MaxItems = ptrInt(n)
			}
		case "len":
			if n, ok := atoi(value); ok {
				s.MinItems = ptrInt(n)
				s.MaxItems = ptrInt(n)
			}
		case "unique":
			s.UniqueItems = true
		}
	}
}

// canonicalFormatName maps the v1 validator tag spellings onto the
// kit's built-in format names (which the messageFor renderer
// recognises).
func canonicalFormatName(key string) string {
	switch key {
	case "url":
		return "uri"
	case "uuid4":
		return "uuid"
	case "ip":
		return "ipv4-or-ipv6"
	case "datetime":
		return "date-time"
	}
	return key
}

// parametricName composes a "key:value" format name, e.g.
// `startswith=/api` → `starts-with:/api`. The leading kit-prefix
// makes the parametric format namespace easy to grep and prevents
// collisions with the JSON-Schema standard format vocabulary.
func parametricName(key, value string) string {
	switch key {
	case "startswith":
		return "starts-with:" + value
	case "endswith":
		return "ends-with:" + value
	case "contains":
		return "contains:" + value
	case "excludesall":
		return "excludes-all:" + value
	}
	return key + ":" + value
}

// splitTagFields splits a `jsonschema:"..."` tag into its
// comma-separated segments, mirroring strings.Split(tag, ",") for the
// common case while preserving commas that belong to a single value.
//
// Two kinds of comma are NOT treated as segment separators:
//
//   - a comma nested inside a `{...}`, `[...]`, or `(...)` group, so a
//     bounded regex quantifier (`pattern=^[a-z]{2,5}$`) or character
//     class (`pattern=[a,b]`) survives intact instead of being
//     truncated into an invalid pattern plus a stray description token;
//   - a backslash-escaped comma (`\,`), for callers that need a literal
//     comma at the top level of a value. The escaping backslash is
//     dropped from the emitted segment.
//
// Backslashes that do not precede a comma are preserved verbatim so
// regex escapes such as `\d` and `\,` outside-of-value text are
// unaffected.
func splitTagFields(tag string) []string {
	var out []string
	var b strings.Builder
	depth := 0
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch c {
		case '\\':
			// Drop the backslash only when it escapes a comma; otherwise
			// keep it so regex escapes pass through unchanged.
			if i+1 < len(tag) && tag[i+1] == ',' {
				b.WriteByte(',')
				i++
				continue
			}
			b.WriteByte(c)
		case '{', '[', '(':
			depth++
			b.WriteByte(c)
		case '}', ']', ')':
			if depth > 0 {
				depth--
			}
			b.WriteByte(c)
		case ',':
			if depth > 0 {
				b.WriteByte(c)
				continue
			}
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	out = append(out, b.String())
	return out
}

// splitRule splits a `key=value` rule into its parts. Bare rules
// (`required`, `email`, `uuid`) return key=rule, value="".
func splitRule(rule string) (string, string) {
	if idx := strings.IndexByte(rule, '='); idx >= 0 {
		return rule[:idx], rule[idx+1:]
	}
	return rule, ""
}

// parseEnum splits a `oneof=` value list. JSON-Schema enum values
// must match the field's type, so integers and floats are parsed
// rather than emitted as strings.
func parseEnum(schemaType, value string) []any {
	parts := strings.Fields(value)
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		switch schemaType {
		case "integer":
			if n, err := strconv.ParseInt(p, 10, 64); err == nil {
				out = append(out, n)
				continue
			}
		case "number":
			if f, err := strconv.ParseFloat(p, 64); err == nil {
				out = append(out, f)
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func atof(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func ptrInt(n int) *int           { return &n }
func ptrFloat(f float64) *float64 { return &f }
