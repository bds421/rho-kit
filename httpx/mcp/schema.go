package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ErrCyclicSchema is returned by [GenerateSchema] when the supplied
// type contains a cycle (a struct that recursively references its
// own type). MCP clients cannot consume an unbounded JSON-Schema —
// surfacing the cycle at registration time prevents emitting a
// schema that would render the tool unusable.
var ErrCyclicSchema = errors.New("mcp: schema generation: cyclic type reference")

// ErrUnsupportedType is returned by [GenerateSchema] when a Go type
// has no JSON-Schema mapping (e.g. function types, channels,
// unsafe.Pointer). Callers should narrow the type or supply an
// explicit schema via [WithInputSchema].
var ErrUnsupportedType = errors.New("mcp: schema generation: unsupported type")

// GenerateSchema produces a JSON-Schema for the given Go type. The
// generator follows the rules:
//
//   - string types → {"type": "string"}.
//   - signed/unsigned integer types → {"type": "integer"}.
//   - floating-point types → {"type": "number"}.
//   - bool → {"type": "boolean"}.
//   - []T (and arrays) → {"type": "array", "items": <T schema>}.
//   - map[string]T → {"type": "object", "additionalProperties":
//     <T schema>}.
//   - struct → {"type": "object", "properties": ...,
//     "required": [...]} with required fields drawn from
//     `validate:"required"` tags.
//   - time.Time → {"type": "string", "format": "date-time"}.
//   - *T → schema of T (the schema does not encode pointer-ness;
//     nil values are simply absent).
//   - json.RawMessage / []byte → {"type": "string"} unless the
//     field is explicitly typed as a structured object via tag.
//
// `omitempty` and `validate:"required"` interact predictably:
// `validate:"required"` emits the field in the `required` array
// even if `omitempty` is also present. The author's intent on
// `validate:"required"` is unambiguous; `omitempty` is a JSON
// shaping concern, not a schema-required statement.
//
// Cycles are detected via a visit set; the first re-entry into a
// type currently being walked returns [ErrCyclicSchema].
func GenerateSchema(t reflect.Type) (json.RawMessage, error) {
	if t == nil {
		return json.RawMessage(`{"type":"object"}`), nil
	}
	visiting := make(map[reflect.Type]bool)
	obj, err := schemaFor(t, visiting)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal schema: %w", err)
	}
	return out, nil
}

// timeType is the canonical reflect.Type of time.Time, cached so the
// generator can short-circuit before walking the (recursive) struct.
var timeType = reflect.TypeOf(time.Time{})

// rawMessageType is encoding/json.RawMessage. We treat raw bytes as
// a free-form value — emitting a schema with `type: string` is the
// closest non-lying mapping; callers that want a richer schema can
// supply their own.
var rawMessageType = reflect.TypeOf(json.RawMessage{})

// schemaFor recursively walks t and returns its schema as a Go map
// (so callers can compose with vendor extensions before marshalling).
func schemaFor(t reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	// Unwrap pointer types up front. JSON-Schema doesn't model
	// pointer indirection; an absent field is simply absent.
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// time.Time is a struct under the hood; treat it as a string
	// before the struct walker tries to enumerate its private
	// fields.
	if t == timeType {
		return map[string]any{
			"type":   "string",
			"format": "date-time",
		}, nil
	}
	// json.RawMessage is []byte — surface as string by default.
	if t == rawMessageType {
		return map[string]any{"type": "string"}, nil
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil

	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil

	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil

	case reflect.Slice, reflect.Array:
		// []byte is a special case: JSON marshals it as a base64
		// string, so the schema should reflect that.
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"}, nil
		}
		items, err := schemaFor(t.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type":  "array",
			"items": items,
		}, nil

	case reflect.Map:
		// JSON-Schema only models string-keyed maps.
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: map with non-string key %s", ErrUnsupportedType, t.Key())
		}
		valSchema, err := schemaFor(t.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type":                 "object",
			"additionalProperties": valSchema,
		}, nil

	case reflect.Interface:
		// `any` / `interface{}` — emit an empty schema so any value
		// validates. The kit prefers concrete types in handler
		// inputs, but tool authors occasionally need the escape
		// hatch for free-form metadata bags.
		return map[string]any{}, nil

	case reflect.Struct:
		return structSchema(t, visiting)

	default:
		return nil, fmt.Errorf("%w: %s (kind %s)", ErrUnsupportedType, t.String(), t.Kind())
	}
}

// structSchema walks a struct type, producing a JSON-Schema object
// with `properties` and (optionally) `required`. Cycles are detected
// here because structs are the only kind that can recursively
// reference themselves.
func structSchema(t reflect.Type, visiting map[reflect.Type]bool) (map[string]any, error) {
	if visiting[t] {
		return nil, fmt.Errorf("%w: %s", ErrCyclicSchema, t.String())
	}
	visiting[t] = true
	// Defer-revert so siblings of the cycling type still parse.
	defer delete(visiting, t)

	props := map[string]any{}
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		// Embedded structs flatten into the parent — same as
		// encoding/json. For `struct { *Embedded }` the field type is
		// a pointer; unwrap before recursing so structSchema sees the
		// underlying struct (calling NumField on a pointer panics).
		if f.Anonymous && f.Tag.Get("json") == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct {
				continue
			}
			embedded, err := structSchema(ft, visiting)
			if err != nil {
				return nil, err
			}
			if eprops, ok := embedded["properties"].(map[string]any); ok {
				for k, v := range eprops {
					props[k] = v
				}
			}
			if ereq, ok := embedded["required"].([]string); ok {
				required = append(required, ereq...)
			}
			continue
		}

		name, omitEmpty, skip := jsonFieldName(f)
		if skip {
			continue
		}

		fieldSchema, err := schemaFor(f.Type, visiting)
		if err != nil {
			return nil, err
		}

		// Carry over a description from a `desc` tag if present,
		// without forcing every kit handler to invent a tag for it.
		// A future revision may parse leading-comment doc strings
		// from the field; for now the explicit tag is the only
		// supported source so behaviour is reflective and
		// self-documenting.
		if d := f.Tag.Get("desc"); d != "" {
			fieldSchema["description"] = d
		}

		if isRequired(f) {
			required = append(required, name)
		} else if !omitEmpty && f.Type.Kind() != reflect.Pointer {
			// Non-pointer, non-omitempty fields without an explicit
			// `validate:"required"` tag are still effectively
			// required from a JSON serialisation standpoint —
			// encoding/json will always emit them. Don't infer
			// `required` from this, however: the validate package
			// is the source of truth for "must-be-present" so we
			// don't accidentally make optional fields required.
			_ = fieldSchema
		}

		props[name] = fieldSchema
	}

	// additionalProperties:false matches the runtime decoder, which
	// uses DisallowUnknownFields. Without this, schema-validating
	// clients can craft requests that look valid against the schema
	// but fail at runtime — a contract drift the audit flagged.
	out := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

// jsonFieldName returns (name, omitempty, skip) for a struct field
// using the same rules as encoding/json. Skip is true for
// `json:"-"`.
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
		if p == "omitempty" {
			omitEmpty = true
		}
	}
	return name, omitEmpty, false
}

// isRequired reports whether a struct field is tagged
// `validate:"required"`. We only check for the bare "required" rule
// — composite tags like `validate:"required,email"` also count.
func isRequired(f reflect.StructField) bool {
	tag := f.Tag.Get("validate")
	if tag == "" {
		return false
	}
	for _, rule := range strings.Split(tag, ",") {
		if rule == "required" {
			return true
		}
	}
	return false
}
