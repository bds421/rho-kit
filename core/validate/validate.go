// Package validate provides struct validation driven by JSON Schema.
//
// The package wires two upstream libraries together:
//
//   - github.com/google/jsonschema-go is the in-memory schema model. We
//     walk struct types ourselves to read the kit's `jsonschema:"..."`
//     constraint tag and emit a jsonschema-go [jsonschema.Schema]
//     populated with the corresponding JSON-Schema keywords (minLength,
//     maximum, pattern, format, enum, ...).
//   - github.com/santhosh-tekuri/jsonschema/v6 compiles the marshalled
//     schema and runs the actual validation. Typed [kind.*] errors are
//     mapped back into human-readable messages ("must be a valid email
//     address", "must be at least N characters", ...).
//
// Schemas are built once per reflect.Type and cached in a sync.Map so
// the same handler signature pays the reflection cost only at startup.
// The cached [jsonschema.Schema] is also exposed via [SchemaFor] and
// [SchemaForType] for callers that need to publish the schema (MCP
// tool catalog, OpenAPI export, served /schema endpoints).
//
// Custom validation is registered via [RegisterFormat]: the function
// receives the decoded JSON value (string, number, bool, map, slice)
// for fields whose schema sets `"format": "<name>"`, and returns an
// error if the value is invalid. The closure signature is decoupled
// from any third-party library type.
//
// asvs: V5.1.3
package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	jsonschemago "github.com/google/jsonschema-go/jsonschema"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// FormatFunc is the signature for a custom format validator. The
// argument is the decoded JSON value at the field — typically a string
// or number; the function is also free to receive maps, slices, or
// bools when the schema is permissive enough to admit them. A nil
// return signals "valid"; any other error becomes a validation failure.
type FormatFunc = func(v any) error

// Validator wraps a private santhosh-tekuri compiler and a per-type
// schema cache. The validator is safe for concurrent use; format
// registration is serialised against schema compilation so the first
// call to [Validator.Struct] freezes the format registry.
type Validator struct {
	mu      sync.Mutex // serialises RegisterFormat against schema compilation
	frozen  atomic.Bool
	schemas sync.Map // reflect.Type -> *compiledSchema
	formats map[string]*jsonschema.Format
}

// compiledSchema bundles the inferred jsonschema-go schema (for
// callers that want to publish or introspect) with the compiled
// santhosh-tekuri schema (used at validation time) and the per-type
// "required-non-empty" set used to render `is required` for
// minLength:1 / minItems:1 violations on fields tagged `required`.
// fieldOrder records the declared property order at every nesting
// depth so collectFieldErrors can return field errors in a
// deterministic, struct-declaration order — santhosh-tekuri itself
// makes no ordering guarantee.
type compiledSchema struct {
	inferred         *jsonschemago.Schema
	compiled         *jsonschema.Schema
	requiredNonEmpty map[string]struct{} // JSON-pointer paths (dot-joined) of required-non-empty fields
	fieldOrder       map[string]int      // dotted field path -> declaration index
	collections      map[string]int      // schema path of array/map field -> nested element levels (stripped before lookup)
}

// New constructs an isolated Validator. Tests and callers that need
// independent format registries should use this rather than the
// package-level [Struct] / [RegisterFormat].
func New() *Validator {
	return &Validator{
		formats: make(map[string]*jsonschema.Format),
	}
}

// Struct validates s and returns nil on success or an
// *apperror.ValidationError on failure.
//
// Inputs that cannot be marshalled to JSON, or whose Go type cannot be
// reflected into a schema, are programming bugs — those surface as
// [apperror.NewOperationFailedWithCause] so misuse is not blamed on
// the caller's input.
func (v *Validator) Struct(s any) error {
	// Freeze the format registry on the first Struct call so a later
	// RegisterFormat fails rather than racing concurrent validation.
	// After the first transition the registry is permanently frozen, so
	// the steady-state path only does a lock-free atomic load — avoiding
	// a process-wide mutex acquisition on every request for the
	// package-level singleton shared by httpx/typed and MCP handlers.
	if !v.frozen.Load() {
		v.mu.Lock()
		v.frozen.Store(true)
		v.mu.Unlock()
	}

	if s == nil {
		return nil
	}
	t := reflect.TypeOf(s)
	rv := reflect.ValueOf(s)
	for t != nil && t.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		t = t.Elem()
		rv = rv.Elem()
	}
	if t == nil {
		return nil
	}

	cs, err := v.schemaForType(t)
	if err != nil {
		return apperror.NewOperationFailedWithCause("validate: schema generation failed (programming error)", err)
	}

	// Marshal the input to JSON and back so the validator sees the
	// same shape an HTTP/MCP transport would. Avoids quirks like
	// channels-on-structs accidentally validating.
	buf, err := json.Marshal(s)
	if err != nil {
		return apperror.NewOperationFailedWithCause("validate: marshal input for validation", err)
	}
	var doc any
	if err := json.Unmarshal(buf, &doc); err != nil {
		return apperror.NewOperationFailedWithCause("validate: re-decode input for validation", err)
	}

	verr := cs.compiled.Validate(doc)
	if verr == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return apperror.NewOperationFailedWithCause("validate: unexpected validator error", verr)
	}
	fields := collectFieldErrors(ve, cs.requiredNonEmpty, cs.fieldOrder, cs.collections)
	if len(fields) == 0 {
		// Defensive: santhosh-tekuri always returns at least one
		// leaf error, but if the shape ever changes we surface the
		// top-level error rather than swallow it.
		return apperror.NewValidation(ve.Error())
	}
	return apperror.NewFieldValidation(fields...)
}

// RegisterFormat registers a custom JSON-Schema `format` validator.
// Use the format name in a field's `jsonschema:"format=<name>"`
// constraint to trigger it. RegisterFormat must be called before the
// first Struct() call freezes the validator; afterwards it returns an
// error so concurrent Struct calls cannot race the format registry.
//
// The format function receives the decoded JSON value verbatim — a
// string field appears as a Go string, an integer as a json.Number /
// float64 depending on the unmarshal path. Functions should accept
// the broader shape rather than assert a Go type, since santhosh-
// tekuri may pass either depending on the schema.
//
// Convention deviation: most of the kit's option-style helpers panic
// on programmer error at construction time. RegisterFormat returns
// error instead because (a) it can be called after a Validator has
// already served traffic, so a panic here would be a runtime crash
// rather than a startup crash; (b) existing callers branch on the
// error to surface "validator already frozen" (and empty-name /
// nil-fn misconfig) through ops dashboards. Re-registration of an
// existing name overwrites the previous FormatFunc, including
// built-ins — this is intentional so tests and startup code can
// replace a format without a freeze/unfreeze cycle. See
// `core/validate/doc.go`.
func (v *Validator) RegisterFormat(name string, fn FormatFunc) error {
	if name == "" {
		return errors.New("validate: RegisterFormat name must not be empty")
	}
	if fn == nil {
		return errors.New("validate: RegisterFormat function must not be nil")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.frozen.Load() {
		return errors.New("validate: RegisterFormat called after Struct(); register custom formats during init")
	}
	v.formats[name] = &jsonschema.Format{Name: name, Validate: fn}
	return nil
}

// SchemaFor returns the inferred jsonschema-go [jsonschema.Schema] for
// type T. The returned schema is the package's cached instance — do
// not mutate; clone first if a caller needs a modifiable copy.
func SchemaFor[T any]() (*jsonschemago.Schema, error) {
	var zero T
	t := reflect.TypeOf(zero)
	return singleton().SchemaForType(t)
}

// SchemaForType returns the inferred jsonschema-go schema for the
// supplied reflect.Type. Pointer types are unwrapped before inference,
// matching the v1 convention that schemas describe the JSON shape and
// pointer-ness is a Go-side concern.
func (v *Validator) SchemaForType(t reflect.Type) (*jsonschemago.Schema, error) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return nil, errors.New("validate: SchemaForType: nil type")
	}
	cs, err := v.schemaForType(t)
	if err != nil {
		return nil, err
	}
	return cs.inferred, nil
}

// schemaForType returns the cached *compiledSchema for t, building and
// compiling it on first use. The cache key is the unwrapped element
// type — pointer wrappers collapse to a single entry.
func (v *Validator) schemaForType(t reflect.Type) (*compiledSchema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if cached, ok := v.schemas.Load(t); ok {
		return cached.(*compiledSchema), nil
	}
	bs, err := buildSchema(t)
	if err != nil {
		return nil, err
	}
	// Fail closed on bare format= names that are neither kit builtins
	// nor RegisterFormat'd. santhosh-tekuri treats unknown format
	// assertions as always-valid, so an unregistered typo would
	// silently accept every value at validation time.
	if len(bs.customFormats) > 0 {
		registered := map[string]struct{}{}
		for _, f := range v.snapshotFormats() {
			registered[f.Name] = struct{}{}
		}
		for _, name := range bs.customFormats {
			if _, ok := registered[name]; !ok {
				return nil, fmt.Errorf("validate: unknown format %q (register via RegisterFormat or use a builtin)", name)
			}
		}
	}
	raw, err := json.Marshal(bs.schema)
	if err != nil {
		return nil, fmt.Errorf("validate: marshal inferred schema: %w", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("validate: re-decode inferred schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.DefaultDraft(jsonschema.Draft2020)
	// Built-in formats first, then user-registered (so users can
	// override a built-in by re-registering the same name).
	for _, f := range builtinFormats(bs.parametricFormats) {
		compiler.RegisterFormat(f)
	}
	// Freezing here (not only in Struct) is what makes the freeze
	// invariant hold for SchemaFor / SchemaForType too: those paths also
	// compile-and-cache a schema against the current format snapshot, so
	// a RegisterFormat that lands afterwards would otherwise succeed yet
	// never run for this already-cached type. Freeze atomically with the
	// snapshot so no registration can slip between the two.
	for _, f := range v.freezeAndSnapshotFormats() {
		compiler.RegisterFormat(f)
	}
	const resourceURL = "schema://kit/validate"
	if err := compiler.AddResource(resourceURL, doc); err != nil {
		return nil, fmt.Errorf("validate: register schema resource: %w", err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("validate: compile schema: %w", err)
	}
	cs := &compiledSchema{
		inferred:         bs.schema,
		compiled:         compiled,
		requiredNonEmpty: bs.requiredNonEmpty,
		fieldOrder:       bs.fieldOrder,
		collections:      bs.collections,
	}
	actual, _ := v.schemas.LoadOrStore(t, cs)
	return actual.(*compiledSchema), nil
}

// freezeAndSnapshotFormats freezes the format registry and returns a
// stable copy of the registered formats so the compiler call does not
// hold the validator's mutex. The freeze and the snapshot happen under
// the same lock as RegisterFormat's frozen check, so once a schema is
// compiled (via Struct, SchemaFor, or SchemaForType) any later
// RegisterFormat fails rather than being silently dropped for the
// schemas already cached.
func (v *Validator) freezeAndSnapshotFormats() []*jsonschema.Format {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.frozen.Store(true)
	return v.copyFormatsLocked()
}

// snapshotFormats returns a stable copy of currently registered
// formats without freezing the registry. Used to fail-closed-check
// bare format= names before compile.
func (v *Validator) snapshotFormats() []*jsonschema.Format {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.copyFormatsLocked()
}

func (v *Validator) copyFormatsLocked() []*jsonschema.Format {
	out := make([]*jsonschema.Format, 0, len(v.formats))
	for _, f := range v.formats {
		out = append(out, f)
	}
	return out
}

// --- Package-level singleton (legacy / convenience API) ---

var (
	defaultValidator     *Validator
	defaultValidatorOnce sync.Once
)

func singleton() *Validator {
	defaultValidatorOnce.Do(func() {
		defaultValidator = New()
	})
	return defaultValidator
}

// Struct validates s using the package-level Validator. The first call
// freezes the package-level format registry; further [RegisterFormat]
// calls fail with an error.
func Struct(s any) error {
	return singleton().Struct(s)
}

// RegisterFormat registers a custom format on the package-level
// Validator. Call during init only — see [Validator.RegisterFormat].
func RegisterFormat(name string, fn FormatFunc) error {
	return singleton().RegisterFormat(name, fn)
}
