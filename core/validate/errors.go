package validate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// collectFieldErrors walks a santhosh-tekuri *ValidationError tree
// and returns one apperror.FieldError per offending field path. When
// a single field accumulates multiple errors (an empty string failing
// both `minLength:1` and `format:email`, for example), the renderer
// keeps the most caller-actionable one: "is required" wins over a
// format/length message, since the empty value is the root cause.
//
// requiredNonEmpty is the set of dot-joined field paths whose
// `minLength` / `minItems` violation with a zero `Got` should be
// rendered as "is required" rather than the generic length message.
// These are fields tagged `jsonschema:"required"` whose schema also
// constrains non-empty content.
//
// fieldOrder maps a dotted field path to its declaration index so
// the returned slice can be sorted deterministically in struct field
// order. santhosh-tekuri makes no ordering guarantee on the causes
// it returns; without this sort, Example tests would flake on map
// iteration order.
func collectFieldErrors(ve *jsonschema.ValidationError, requiredNonEmpty map[string]struct{}, fieldOrder map[string]int, collections map[string]int) []apperror.FieldError {
	var raw []apperror.FieldError
	collectFieldErrorsInto(ve, requiredNonEmpty, collections, &raw)
	return sortFieldErrors(dedupeFieldErrors(raw), fieldOrder, collections)
}

// sortFieldErrors orders the slice by the field's declaration index
// in fieldOrder. Unknown fields (paths the walker did not record)
// fall to the end in alphabetical order.
func sortFieldErrors(in []apperror.FieldError, fieldOrder map[string]int, collections map[string]int) []apperror.FieldError {
	if len(in) <= 1 || len(fieldOrder) == 0 {
		return in
	}
	sort.SliceStable(in, func(i, j int) bool {
		ai, aOK := fieldOrder[normalizeInstancePath(in[i].Field, collections)]
		bi, bOK := fieldOrder[normalizeInstancePath(in[j].Field, collections)]
		switch {
		case aOK && bOK:
			return ai < bi
		case aOK:
			return true
		case bOK:
			return false
		default:
			return in[i].Field < in[j].Field
		}
	})
	return in
}

// dedupeFieldErrors collapses multiple errors per field into a
// deterministic single entry, preferring "is required" over any
// other message and preserving the first-seen order of fields.
func dedupeFieldErrors(in []apperror.FieldError) []apperror.FieldError {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]int, len(in))
	out := make([]apperror.FieldError, 0, len(in))
	for _, f := range in {
		if idx, ok := seen[f.Field]; ok {
			// Prefer "is required" over any other message.
			if f.Message == "is required" {
				out[idx].Message = "is required"
			}
			continue
		}
		seen[f.Field] = len(out)
		out = append(out, f)
	}
	return out
}

func collectFieldErrorsInto(ve *jsonschema.ValidationError, requiredNonEmpty map[string]struct{}, collections map[string]int, out *[]apperror.FieldError) {
	if len(ve.Causes) > 0 {
		for _, c := range ve.Causes {
			collectFieldErrorsInto(c, requiredNonEmpty, collections, out)
		}
		// Required violations live at the parent (the object missing
		// the property); emit one FieldError per missing property,
		// attributed to the child field path.
		if req, ok := ve.ErrorKind.(*kind.Required); ok {
			for _, name := range req.Missing {
				field := joinPath(ve.InstanceLocation, name)
				*out = append(*out, apperror.FieldError{Field: field, Message: "is required"})
			}
		}
		return
	}
	if ve.ErrorKind == nil {
		return
	}
	field := fieldPath(ve.InstanceLocation)
	if req, ok := ve.ErrorKind.(*kind.Required); ok {
		for _, name := range req.Missing {
			f := joinPath(ve.InstanceLocation, name)
			*out = append(*out, apperror.FieldError{Field: f, Message: "is required"})
		}
		return
	}
	*out = append(*out, apperror.FieldError{
		Field:   field,
		Message: messageFor(normalizeInstancePath(field, collections), ve.ErrorKind, requiredNonEmpty),
	})
}

// fieldPath converts a santhosh-tekuri InstanceLocation (JSON-pointer
// segments) to the dotted-path form the v1 validator emitted.
func fieldPath(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	return strings.Join(loc, ".")
}

func joinPath(loc []string, leaf string) string {
	if len(loc) == 0 {
		return leaf
	}
	return strings.Join(loc, ".") + "." + leaf
}

// normalizeInstancePath collapses the per-element segments santhosh-
// tekuri injects into an instance path into the schema-side path the
// walker recorded. The walker did not know the concrete indices/keys
// at build time, so a required-non-empty field inside a slice element
// is keyed "items.name" while the violation arrives at "items.0.name";
// likewise a map element arrives at "m.somekey.label" for the
// schema-side "m.label". collections maps a schema path to the number
// of element levels nested directly under it; whenever the schema path
// accumulated so far names a collection, exactly that many following
// instance segments are element indices/keys and are dropped. Nested
// collections that share a path (slice-of-slice, map-of-slice) carry a
// count above one and collapse the same way. When collections is empty
// the input is returned unchanged, so non-collection schemas pay no
// allocation.
func normalizeInstancePath(field string, collections map[string]int) string {
	if field == "" || len(collections) == 0 {
		return field
	}
	segs := strings.Split(field, ".")
	out := make([]string, 0, len(segs))
	var canonical string
	skip := 0
	for _, seg := range segs {
		if skip > 0 {
			// This segment is an element index/key under the most
			// recently seen collection; drop it without extending the
			// canonical path.
			skip--
			continue
		}
		out = append(out, seg)
		if canonical == "" {
			canonical = seg
		} else {
			canonical += "." + seg
		}
		if n, isCollection := collections[canonical]; isCollection {
			skip = n
		}
	}
	return strings.Join(out, ".")
}

// messageFor renders a santhosh-tekuri ErrorKind as the kit's
// human-readable validation message. The phrasing mirrors the v1
// go-playground messages so callers that grep for "must be a valid
// email address" continue to work.
func messageFor(field string, k jsonschema.ErrorKind, requiredNonEmpty map[string]struct{}) string {
	switch e := k.(type) {
	case *kind.MinLength:
		// A required-non-empty field whose actual length was zero
		// renders as "is required" to preserve the v1 message;
		// otherwise fall back to the length-based message.
		if e.Got == 0 {
			if _, ok := requiredNonEmpty[field]; ok {
				return "is required"
			}
		}
		return "must be at least " + pluralize(e.Want, "character")
	case *kind.MaxLength:
		return "must be at most " + pluralize(e.Want, "character")
	case *kind.Minimum:
		return "must be greater than or equal to " + ratString(e.Want)
	case *kind.Maximum:
		return "must be less than or equal to " + ratString(e.Want)
	case *kind.ExclusiveMinimum:
		return "must be greater than " + ratString(e.Want)
	case *kind.ExclusiveMaximum:
		return "must be less than " + ratString(e.Want)
	case *kind.MinItems:
		if e.Got == 0 {
			if _, ok := requiredNonEmpty[field]; ok {
				return "is required"
			}
		}
		return "must have at least " + pluralize(e.Want, "item")
	case *kind.MaxItems:
		return "must have at most " + pluralize(e.Want, "item")
	case *kind.Pattern:
		return "must match pattern " + e.Want
	case *kind.Enum:
		return "must be one of: " + enumValues(e.Want)
	case *kind.Const:
		return "must equal " + fmt.Sprint(e.Want)
	case *kind.Format:
		return formatMessage(e.Want)
	case *kind.Type:
		return "must be " + strings.Join(e.Want, " or ")
	case *kind.MinProperties:
		return "must have at least " + pluralize(e.Want, "property")
	case *kind.MaxProperties:
		return "must have at most " + pluralize(e.Want, "property")
	case *kind.UniqueItems:
		return "must contain unique items"
	case *kind.MultipleOf:
		return "must be a multiple of " + ratString(e.Want)
	case *kind.AdditionalProperties:
		if len(e.Properties) == 0 {
			return "must not contain additional properties"
		}
		return "must not contain additional properties: " + strings.Join(e.Properties, ", ")
	case *kind.FalseSchema:
		return "must not be present"
	case *kind.Contains:
		return "must contain a matching item"
	case *kind.Required:
		return "is required"
	}
	return "failed validation"
}

// pluralize renders "<n> <unit>" with the unit pluralised for any
// count other than one, so a min=1 / len=1 constraint reads "must be at
// least 1 character" rather than the grammatically-wrong "1 characters".
// Plurals follow the simple English rule the kit's units need: a "y"
// terminal becomes "ies" (property -> properties), otherwise "s" is
// appended (character -> characters, item -> items).
func pluralize(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	plural := unit + "s"
	if strings.HasSuffix(unit, "y") {
		plural = strings.TrimSuffix(unit, "y") + "ies"
	}
	return strconv.Itoa(n) + " " + plural
}

// ratString renders a *big.Rat as the JSON-Schema author would have
// typed it: an integer when the denominator is one, otherwise a
// decimal. Avoids stamping "30/1" into a user-facing message.
func ratString(r interface{ FloatString(int) string }) string {
	if r == nil {
		return ""
	}
	s := r.FloatString(6)
	if idx := strings.IndexByte(s, '.'); idx >= 0 {
		end := len(s)
		for end > idx+1 && s[end-1] == '0' {
			end--
		}
		if end == idx+1 {
			end = idx
		}
		s = s[:end]
	}
	return s
}

// enumValues renders an enum constraint's expected values as a
// space-separated list, matching the v1 oneof phrasing
// ("must be one of: admin user viewer").
func enumValues(values []any) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprint(v))
	}
	return strings.Join(parts, " ")
}

// formatMessage renders a kind.Format violation in the same phrasing
// the v1 package used for the common formats. Unknown formats fall
// back to a generic "must be a valid <name>".
func formatMessage(name string) string {
	switch {
	case name == "email" || name == "idn-email":
		return "must be a valid email address"
	case name == "uri" || name == "url" || name == "iri":
		return "must be a valid URL"
	case name == "uuid" || name == "uuid4":
		return "must be a valid UUID"
	case name == "ipv4" || name == "ipv6" || name == "ip" || name == "ipv4-or-ipv6":
		return "must be a valid IP address"
	case name == "hostname" || name == "idn-hostname":
		return "must be a valid hostname"
	case name == "date-time":
		return "must be a valid datetime (RFC 3339)"
	case name == "date":
		return "must be a valid date"
	case name == "time":
		return "must be a valid time"
	case name == "alpha":
		return "must contain only letters"
	case name == "alphanum":
		return "must contain only alphanumeric characters"
	case name == "numeric":
		return "must be numeric"
	case name == "cidr":
		return "must be a valid CIDR notation"
	case strings.HasPrefix(name, "starts-with:"):
		return "must start with " + strings.TrimPrefix(name, "starts-with:")
	case strings.HasPrefix(name, "ends-with:"):
		return "must end with " + strings.TrimPrefix(name, "ends-with:")
	case strings.HasPrefix(name, "contains:"):
		return "must contain " + strings.TrimPrefix(name, "contains:")
	case strings.HasPrefix(name, "excludes-all:"):
		return "contains disallowed characters"
	}
	return "must be a valid " + name
}
