package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	jsonschemago "github.com/google/jsonschema-go/jsonschema"
)

// timeType is the canonical reflect.Type of time.Time, treated as a
// JSON string with `date-time` format by the inferred schema.
var timeType = reflect.TypeFor[time.Time]()

// rawMessageType maps json.RawMessage onto a schema that admits any
// JSON value, matching what the field literally encodes.
var rawMessageType = reflect.TypeFor[json.RawMessage]()

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
	// customFormats are bare format= names that are not kit builtins.
	// schemaForType fail-closes unless each is present in the
	// RegisterFormat registry (unknown names would otherwise be
	// silently ignored by santhosh-tekuri's format assertion).
	customFormats []string
	fieldOrder    map[string]int
	// collections maps a schema-side dotted path that describes a JSON
	// array or object-with-additionalProperties (slice/array or map
	// fields) to the number of element levels nested directly under it.
	// The instance paths santhosh-tekuri reports interpose one element
	// segment per level (a numeric index for arrays, a key for maps)
	// after the path — a slice-of-slice keyed at "items" interposes two
	// ("items.0.1.name"). normalizeInstancePath strips exactly that many
	// segments so a required-non-empty / field-order lookup keyed by the
	// schema path still matches a violation inside a collection element.
	collections map[string]int
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
		customFormats:    map[string]struct{}{},
		fieldOrder:       map[string]int{},
		collections:      map[string]int{},
	}
	s, err := schemaForReflect(ctx, t, "", "")
	if err != nil {
		return nil, err
	}
	out := &builtSchema{
		schema:           s,
		requiredNonEmpty: ctx.requiredNonEmpty,
		fieldOrder:       ctx.fieldOrder,
		collections:      ctx.collections,
	}
	for name := range ctx.parametric {
		out.parametricFormats = append(out.parametricFormats, name)
	}
	for name := range ctx.customFormats {
		out.customFormats = append(out.customFormats, name)
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
	customFormats    map[string]struct{}
	fieldOrder       map[string]int
	collections      map[string]int
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
		return constrainedString(ctx, "date-time", constraintTag)
	}
	if t == rawMessageType {
		return &jsonschemago.Schema{}, nil
	}

	switch t.Kind() {
	case reflect.String:
		return constrainedString(ctx, "", constraintTag)
	case reflect.Bool:
		return &jsonschemago.Schema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return constrainedNumber(ctx, "integer", constraintTag)
	case reflect.Float32, reflect.Float64:
		return constrainedNumber(ctx, "number", constraintTag)
	case reflect.Slice, reflect.Array:
		return arraySchema(ctx, t, constraintTag, path)
	case reflect.Map:
		return mapSchema(ctx, t, path)
	case reflect.Interface:
		return &jsonschemago.Schema{}, nil
	case reflect.Struct:
		return structSchema(ctx, t, path)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, t.Kind())
	}
}

func constrainedString(ctx *buildCtx, format, constraintTag string) (*jsonschemago.Schema, error) {
	s := &jsonschemago.Schema{Type: "string", Format: format}
	if err := applyStringConstraints(ctx, s, constraintTag); err != nil {
		return nil, err
	}
	return s, nil
}

func constrainedNumber(ctx *buildCtx, typ, constraintTag string) (*jsonschemago.Schema, error) {
	s := &jsonschemago.Schema{Type: typ}
	if err := applyNumericConstraints(ctx, s, constraintTag); err != nil {
		return nil, err
	}
	return s, nil
}

func arraySchema(ctx *buildCtx, t reflect.Type, constraintTag string, path string) (*jsonschemago.Schema, error) {
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		// []byte marshals as a base64 string. encoding/json only
		// base64-encodes byte *slices*; a byte *array* ([16]byte
		// UUID, [32]byte hash) marshals as a JSON array of numbers,
		// so it falls through to the array-of-integer schema below.
		//
		// String constraints (min/max/len/pattern/format) apply to
		// the base64-encoded text the field marshals to, NOT the raw
		// byte count: a 3-byte slice encodes to a 4-character base64
		// string, so `min=`/`max=` count base64 characters. Run them
		// here so a constraint on a []byte field is honoured instead
		// of silently dropped.
		return constrainedString(ctx, "", constraintTag)
	}
	if ctx.visiting[t] {
		return nil, fmt.Errorf("%w: recursive array or slice type", ErrCyclicSchema)
	}
	ctx.visiting[t] = true
	defer delete(ctx.visiting, t)
	// Record this path as a collection so the error renderer can
	// strip the per-element index segment santhosh-tekuri injects
	// ("items.0.name" -> "items.name") before a requiredNonEmpty /
	// fieldOrder lookup. A slice-of-slice keyed at the same path
	// nests two element levels, so the count is incremented.
	if path != "" {
		ctx.collections[path]++
	}
	items, err := schemaForReflect(ctx, t.Elem(), "", path)
	if err != nil {
		return nil, err
	}
	s := &jsonschemago.Schema{Type: "array", Items: items}
	if err := applyArrayConstraints(s, constraintTag); err != nil {
		return nil, err
	}
	return s, nil
}

func mapSchema(ctx *buildCtx, t reflect.Type, path string) (*jsonschemago.Schema, error) {
	if t.Key().Kind() != reflect.String {
		return nil, fmt.Errorf("%w: map key must be string", ErrUnsupportedType)
	}
	if ctx.visiting[t] {
		return nil, fmt.Errorf("%w: recursive map type", ErrCyclicSchema)
	}
	ctx.visiting[t] = true
	defer delete(ctx.visiting, t)
	// Record this path as a collection so the error renderer can
	// strip the per-entry key segment santhosh-tekuri injects
	// ("m.somekey.label" -> "m.label") before a requiredNonEmpty /
	// fieldOrder lookup. A map-of-slice (or slice-of-map) keyed at
	// the same path nests more than one element level, so the count
	// is incremented rather than set.
	if path != "" {
		ctx.collections[path]++
	}
	val, err := schemaForReflect(ctx, t.Elem(), "", path)
	if err != nil {
		return nil, err
	}
	return &jsonschemago.Schema{Type: "object", AdditionalProperties: val}, nil
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

	w := &structWalk{
		ctx:  ctx,
		path: path,
		out: &jsonschemago.Schema{
			Type:                 "object",
			Properties:           map[string]*jsonschemago.Schema{},
			AdditionalProperties: falseSchema(),
		},
		seenRequired: map[string]struct{}{},
		directNames:  directFieldNames(t),
	}
	for field := range t.Fields() {
		if err := w.addField(field); err != nil {
			return nil, err
		}
	}
	if len(w.required) > 0 {
		w.out.Required = w.required
	}
	if len(w.order) > 0 {
		w.out.PropertyOrder = w.order
	}
	return w.out, nil
}

// structWalk is the state of one structSchema call.
type structWalk struct {
	ctx      *buildCtx
	path     string
	out      *jsonschemago.Schema
	order    []string
	required []string
	// seenRequired dedupes the required list so a name promoted from two
	// embedded structs (or an embedded plus a direct field) is listed
	// once.
	seenRequired map[string]struct{}
	directNames  map[string]struct{}
}

// directFieldNames is the set of JSON names declared directly on t
// (depth 0). encoding/json's shadowing rule gives a shallower field
// precedence over an embedded (deeper) field with the same name
// regardless of declaration order, so a direct field always wins.
// Precomputing the set lets the embedded merge skip a shadowed sibling —
// including the case where the embedded struct is declared *before* the
// parent field, which would otherwise leave a duplicate PropertyOrder
// entry plus a stale required marker from the losing embedded field.
func directFieldNames(t reflect.Type) map[string]struct{} {
	names := map[string]struct{}{}
	for f := range t.Fields() {
		if isFlattenedEmbed(f) || !f.IsExported() {
			continue
		}
		if name, _, skip := jsonFieldName(f); !skip {
			names[name] = struct{}{}
		}
	}
	return names
}

// isFlattenedEmbed reports an embedded field without a json tag, which
// flattens into the parent.
func isFlattenedEmbed(f reflect.StructField) bool {
	return f.Anonymous && f.Tag.Get("json") == ""
}

func (w *structWalk) childPath(name string) string {
	if w.path == "" {
		return name
	}
	return w.path + "." + name
}

func (w *structWalk) addRequired(name string) {
	if _, ok := w.seenRequired[name]; ok {
		return
	}
	w.seenRequired[name] = struct{}{}
	w.required = append(w.required, name)
}

// addField adds one field of the struct. encoding/json promotes exported
// fields of an embedded unexported-named struct as well (the struct field
// itself can have an unexported name like `base`, but its `ID` member is
// still emitted under the parent), so embedding is checked BEFORE the
// IsExported gate.
func (w *structWalk) addField(f reflect.StructField) error {
	if isFlattenedEmbed(f) {
		return w.mergeEmbedded(f)
	}
	if !f.IsExported() {
		return nil
	}
	name, omitEmpty, skip := jsonFieldName(f)
	if skip {
		return nil
	}
	jsTag := f.Tag.Get("jsonschema")
	childPath := w.childPath(name)
	field, err := schemaForReflect(w.ctx, f.Type, jsTag, childPath)
	if err != nil {
		return err
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
		w.addRequired(name)
		// For string/array required fields, also enforce non-
		// empty. The error renderer maps a minLength / minItems
		// violation on a path in requiredNonEmpty back to
		// "is required" so the same wording survives even when
		// the empty value was rejected by a stricter min rule
		// (e.g. `required,min=2`).
		markRequiredNonEmpty(w.ctx, field, childPath)
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
	w.out.Properties[name] = field
	w.order = append(w.order, name)
	w.ctx.fieldOrder[childPath] = len(w.ctx.fieldOrder)
	return nil
}

// mergeEmbedded flattens an embedded struct's properties into the parent.
// A direct field of the parent shadows the embedded sibling: the embedded
// property, its order slot and any required/required-non-empty marker it
// left are dropped so the shallower (winning) field's optionality stands.
func (w *structWalk) mergeEmbedded(f reflect.StructField) error {
	ft := f.Type
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	if ft.Kind() != reflect.Struct {
		return nil
	}
	emb, err := structSchema(w.ctx, ft, w.path)
	if err != nil {
		return err
	}
	for _, name := range emb.PropertyOrder {
		if _, shadowed := w.directNames[name]; shadowed {
			delete(w.ctx.requiredNonEmpty, w.childPath(name))
			continue
		}
		if _, ok := w.out.Properties[name]; ok {
			continue
		}
		w.out.Properties[name] = emb.Properties[name]
		w.order = append(w.order, name)
	}
	for _, name := range emb.Required {
		if _, shadowed := w.directNames[name]; !shadowed {
			w.addRequired(name)
		}
	}
	return nil
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
		if slices.Contains(s.Types, "null") {
			return
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

// knownBuiltinFormats are format names accepted by format= tags without
// prior RegisterFormat. Unknown names fail schema build (fail-closed).
var knownBuiltinFormats = map[string]struct{}{
	"email": {}, "uri": {}, "url": {}, "uuid": {}, "uuid4": {},
	"ip": {}, "ipv4": {}, "ipv6": {}, "ipv4-or-ipv6": {},
	"hostname": {}, "alpha": {}, "alphanum": {}, "numeric": {},
	"cidr": {}, "datetime": {}, "date-time": {},
}

// eachConstraint calls apply with the key and value of every constraint
// keyword in a `jsonschema:"..."` tag. "required" is handled by the
// walker and free-form text by descriptionFromJSONSchemaTag; custom
// RegisterFormat formats are wired via explicit `format=<name>` rather
// than the bare keyword form to keep that description fallback
// unambiguous.
func eachConstraint(tag string, apply func(key, value string) error) error {
	if tag == "" {
		return nil
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
		if err := apply(key, value); err != nil {
			return err
		}
	}
	return nil
}

// applyStringConstraints maps `jsonschema:"..."` rules that apply to
// a string-typed schema (length, format, pattern). Numeric or
// array-only rules are silently ignored — applyNumericConstraints /
// applyArrayConstraints handle those.
//
// Malformed numeric constraint values and unknown format names return
// an error (programming error at schema build) rather than silently
// dropping the constraint (fail-open).
func applyStringConstraints(ctx *buildCtx, s *jsonschemago.Schema, tag string) error {
	return eachConstraint(tag, func(key, value string) error {
		switch key {
		case "min", "max", "len":
			return applyLengthRule(&s.MinLength, &s.MaxLength, key, value)
		case "oneof":
			s.Enum = parseEnum(s.Type, value)
		case "pattern":
			s.Pattern = value
		case "format":
			return applyFormatRule(ctx, s, value,
				" (expected starts-with:/ends-with:/contains:/excludes-all: or a bare registered name)")
		case "email", "url", "uri", "uuid", "uuid4", "ip", "ipv4", "ipv6",
			"hostname", "alpha", "alphanum", "numeric", "cidr", "datetime":
			s.Format = canonicalFormatName(key)
		case "startswith", "endswith", "contains", "excludesall":
			name := parametricName(key, value)
			s.Format = name
			ctx.parametric[name] = struct{}{}
		}
		return nil
	})
}

// applyLengthRule sets the lower bound (min), the upper bound (max) or
// both (len) of a length or item count.
func applyLengthRule(minimum, maximum **int, key, value string) error {
	n, ok := atoi(value)
	if !ok {
		return fmt.Errorf("validate: invalid %s constraint %q", key, value)
	}
	if key != "max" {
		*minimum = new(n)
	}
	if key != "min" {
		*maximum = new(n)
	}
	return nil
}

// applyFormatRule sets an explicit `format=<name>`. Typo'd parametric
// prefixes (e.g. starts-wtih:/api) contain ':' but are not known
// parametric forms, so they fail closed. Builtin bare names are accepted
// at once; custom names are recorded so schemaForType can fail closed
// when the name was never RegisterFormat'd (santhosh-tekuri would
// otherwise treat unknown formats as always-valid).
func applyFormatRule(ctx *buildCtx, s *jsonschemago.Schema, value, unknownHint string) error {
	if value == "" {
		return errors.New("validate: empty format constraint")
	}
	if isParametricFormatName(value) {
		s.Format = value
		ctx.parametric[value] = struct{}{}
		return nil
	}
	if strings.Contains(value, ":") {
		return fmt.Errorf("validate: unknown format %q%s", value, unknownHint)
	}
	if _, ok := knownBuiltinFormats[value]; !ok {
		ctx.customFormats[value] = struct{}{}
	}
	s.Format = value
	return nil
}

// applyNumericConstraints maps `jsonschema:"..."` rules that apply to
// integer/number-typed schemas (min/max value, gte/lte, gt/lt,
// oneof, format).
func applyNumericConstraints(ctx *buildCtx, s *jsonschemago.Schema, tag string) error {
	return eachConstraint(tag, func(key, value string) error {
		switch key {
		case "min", "gte", "max", "lte", "gt", "lt":
			return applyBoundRule(s, key, value)
		case "oneof":
			s.Enum = parseEnum(s.Type, value)
		case "format":
			return applyFormatRule(ctx, s, value, "")
		}
		return nil
	})
}

func applyBoundRule(s *jsonschemago.Schema, key, value string) error {
	f, ok := atof(value)
	if !ok {
		return fmt.Errorf("validate: invalid %s constraint %q", key, value)
	}
	switch key {
	case "min", "gte":
		s.Minimum = new(f)
	case "max", "lte":
		s.Maximum = new(f)
	case "gt":
		s.ExclusiveMinimum = new(f)
	default:
		s.ExclusiveMaximum = new(f)
	}
	return nil
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
func applyArrayConstraints(s *jsonschemago.Schema, tag string) error {
	return eachConstraint(tag, func(key, value string) error {
		switch key {
		case "min", "max", "len":
			return applyLengthRule(&s.MinItems, &s.MaxItems, key, value)
		case "unique":
			s.UniqueItems = true
		}
		return nil
	})
}

// canonicalFormatName maps the v1 validator tag spellings onto the
// kit's built-in format names (which the messageFor renderer
// recognises).
func canonicalFormatName(key string) string {
	switch key {
	case "url":
		return "uri"
	case "ip":
		return "ipv4-or-ipv6"
	case "datetime":
		return "date-time"
	}
	// `uuid4` maps to its own version-enforcing format (registered in
	// builtinFormats) rather than collapsing to the generic `uuid`, so a
	// migrated v1 `uuid4` tag keeps the version-4 constraint.
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
	if before, after, ok := strings.Cut(rule, "="); ok {
		return before, after
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
