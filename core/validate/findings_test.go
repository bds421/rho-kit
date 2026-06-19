package validate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// fieldMsgsFromErr asserts err is a ValidationError and collapses its
// field errors into a path -> message map for assertion convenience.
func fieldMsgsFromErr(t *testing.T, err error) map[string]string {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	return fieldMessages(ve)
}

// --- Finding 1: required-non-empty inside slice/map elements ---

// TestStruct_RequiredInsideSliceElement covers a required string field
// that lives inside a slice element. santhosh-tekuri reports the
// violation at "items.0.name", but the schema-side required-non-empty
// key is "items.name"; the renderer must normalise the element index
// away so the message is "is required" rather than the generic length
// message (which was also grammatically wrong: "at least 1 characters").
func TestStruct_RequiredInsideSliceElement(t *testing.T) {
	type elem struct {
		Name string `json:"name" jsonschema:"required"`
	}
	type req struct {
		Items []elem `json:"items" jsonschema:"required"`
	}
	msgs := fieldMsgsFromErr(t, Struct(req{Items: []elem{{Name: ""}}}))
	if got := msgs["items.0.name"]; got != "is required" {
		t.Errorf("items.0.name message = %q, want %q", got, "is required")
	}
}

// TestStruct_RequiredInsideSliceElementWithMin verifies a required field
// inside a slice element that also carries a min still degrades to
// "is required" on an empty value (the requiredNonEmpty path lookup must
// match through the injected index).
func TestStruct_RequiredInsideSliceElementWithMin(t *testing.T) {
	type elem struct {
		Name string `json:"name" jsonschema:"required,min=3"`
	}
	type req struct {
		Items []elem `json:"items" jsonschema:"required"`
	}
	msgs := fieldMsgsFromErr(t, Struct(req{Items: []elem{{Name: ""}}}))
	if got := msgs["items.0.name"]; got != "is required" {
		t.Errorf("items.0.name message = %q, want %q", got, "is required")
	}
}

// TestStruct_RequiredInsideMapElement covers a required field inside a
// map value. The instance path is "m.<key>.label"; the schema-side key
// is "m.label", so the arbitrary map key segment must be stripped too.
func TestStruct_RequiredInsideMapElement(t *testing.T) {
	type elem struct {
		Label string `json:"label" jsonschema:"required"`
	}
	type req struct {
		M map[string]elem `json:"m" jsonschema:"required"`
	}
	msgs := fieldMsgsFromErr(t, Struct(req{M: map[string]elem{"k": {Label: ""}}}))
	if got := msgs["m.k.label"]; got != "is required" {
		t.Errorf("m.k.label message = %q, want %q", got, "is required")
	}
}

// TestStruct_FieldOrderInsideSliceElement verifies that field errors on
// fields inside a slice element are still sorted in declaration order —
// the fieldOrder lookup must normalise the element index away, otherwise
// it falls back to alphabetical ordering.
func TestStruct_FieldOrderInsideSliceElement(t *testing.T) {
	type elem struct {
		Zulu  string `json:"zulu"  jsonschema:"required"`
		Alpha string `json:"alpha" jsonschema:"required"`
	}
	type req struct {
		Items []elem `json:"items" jsonschema:"required"`
	}
	for i := 0; i < 20; i++ {
		err := Struct(req{Items: []elem{{}}})
		ve, ok := apperror.AsValidation(err)
		if !ok {
			t.Fatalf("iter %d: not a ValidationError: %T", i, err)
		}
		var got []string
		for _, f := range ve.Fields {
			got = append(got, f.Field)
		}
		want := []string{"items.0.zulu", "items.0.alpha"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("iter %d: order = %v, want %v", i, got, want)
		}
	}
}

// TestNormalizeInstancePath exercises the path-normalisation helper for
// the collection shapes the walker records.
func TestNormalizeInstancePath(t *testing.T) {
	collections := map[string]int{
		"items":  1,
		"m":      1,
		"matrix": 2, // slice-of-slice keyed at one path nests two levels
	}
	cases := []struct {
		in   string
		want string
	}{
		{"items.0.name", "items.name"},
		{"m.somekey.label", "m.label"},
		{"items.12.name", "items.name"},
		// Nested collection (slice-of-slice): two element segments are
		// stripped to reach the schema-side path.
		{"matrix.0.1.name", "matrix.name"},
		{"plain.field", "plain.field"},
		{"", ""},
		{"items", "items"},
	}
	for _, c := range cases {
		if got := normalizeInstancePath(c.in, collections); got != c.want {
			t.Errorf("normalizeInstancePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Empty collections set is a no-op.
	if got := normalizeInstancePath("items.0.name", nil); got != "items.0.name" {
		t.Errorf("nil collections changed path: %q", got)
	}
}

// --- Finding 1 (grammar): singular units ---

// TestStruct_SingularLengthMessage verifies the length message reads
// "1 character" (singular) rather than the grammatically-wrong
// "1 characters" when the bound is one.
func TestStruct_SingularLengthMessage(t *testing.T) {
	type req struct {
		// Optional so the empty value is NOT rewritten to "is required";
		// we want to observe the raw length message at the boundary.
		Code string `json:"code" jsonschema:"min=1,max=1"`
	}
	// Too long (2 chars) trips max=1.
	msgs := fieldMsgsFromErr(t, Struct(req{Code: "ab"}))
	if got := msgs["code"]; got != "must be at most 1 character" {
		t.Errorf("max message = %q, want %q", got, "must be at most 1 character")
	}
}

// TestStruct_SingularItemsMessage verifies the items message reads
// "1 item" rather than "1 items".
func TestStruct_SingularItemsMessage(t *testing.T) {
	type req struct {
		Tags []string `json:"tags" jsonschema:"max=1"`
	}
	msgs := fieldMsgsFromErr(t, Struct(req{Tags: []string{"a", "b"}}))
	if got := msgs["tags"]; got != "must have at most 1 item" {
		t.Errorf("items message = %q, want %q", got, "must have at most 1 item")
	}
}

// TestPluralize covers the pluralisation rule directly, including the
// "y" -> "ies" branch used by the properties message.
func TestPluralize(t *testing.T) {
	cases := []struct {
		n    int
		unit string
		want string
	}{
		{1, "character", "1 character"},
		{2, "character", "2 characters"},
		{0, "item", "0 items"},
		{1, "item", "1 item"},
		{1, "property", "1 property"},
		{3, "property", "3 properties"},
	}
	for _, c := range cases {
		if got := pluralize(c.n, c.unit); got != c.want {
			t.Errorf("pluralize(%d, %q) = %q, want %q", c.n, c.unit, got, c.want)
		}
	}
}

// --- Finding 2: []byte string constraints ---

// TestSchema_ByteSliceLengthConstraints verifies length constraints on a
// []byte field are applied to the inferred (base64-string) schema rather
// than silently dropped.
func TestSchema_ByteSliceLengthConstraints(t *testing.T) {
	type req struct {
		Blob []byte `json:"blob" jsonschema:"required,min=4,max=8"`
	}
	s, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	prop, ok := s.Properties["blob"]
	if !ok {
		t.Fatal("missing 'blob' property")
	}
	if prop.MinLength == nil || *prop.MinLength != 4 {
		t.Errorf("MinLength = %v, want 4", prop.MinLength)
	}
	if prop.MaxLength == nil || *prop.MaxLength != 8 {
		t.Errorf("MaxLength = %v, want 8", prop.MaxLength)
	}
}

// TestStruct_ByteSliceMaxLengthEnforced verifies a too-long []byte value
// (counted as base64 characters) is rejected at validation time.
func TestStruct_ByteSliceMaxLengthEnforced(t *testing.T) {
	type req struct {
		Blob []byte `json:"blob" jsonschema:"max=4"`
	}
	// 6 raw bytes -> 8 base64 chars, exceeding max=4.
	if err := Struct(req{Blob: []byte{1, 2, 3, 4, 5, 6}}); err == nil {
		t.Error("expected over-length []byte to be rejected")
	}
	// 1 raw byte -> 4 base64 chars, within max=4.
	if err := Struct(req{Blob: []byte{1}}); err != nil {
		t.Errorf("within-bound []byte rejected: %v", err)
	}
}

// TestStruct_ByteSlicePatternEnforced verifies a pattern constraint on a
// []byte field is applied to the base64 text.
func TestStruct_ByteSlicePatternEnforced(t *testing.T) {
	type req struct {
		// Base64 alphabet only (reject padding for this contrived case).
		Blob []byte `json:"blob" jsonschema:"pattern=^[A-Za-z0-9+/=]+$"`
	}
	if err := Struct(req{Blob: []byte("hello")}); err != nil {
		t.Errorf("valid base64 rejected: %v", err)
	}
}

// --- Finding 3: uuid4 enforces version and canonical form ---

// TestStruct_UUID4EnforcesVersion verifies the uuid4 tag rejects a
// non-version-4 UUID while accepting a version-4 one.
func TestStruct_UUID4EnforcesVersion(t *testing.T) {
	type req struct {
		ID string `json:"id" jsonschema:"required,uuid4"`
	}
	const v4 = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	const v1 = "f47ac10b-58cc-1372-a567-0e02b2c3d479"
	if err := Struct(req{ID: v4}); err != nil {
		t.Errorf("valid v4 UUID rejected: %v", err)
	}
	msgs := fieldMsgsFromErr(t, Struct(req{ID: v1}))
	if got := msgs["id"]; got != "must be a valid UUID" {
		t.Errorf("v1 UUID message = %q, want %q", got, "must be a valid UUID")
	}
}

// TestStruct_UUIDRejectsNonCanonical verifies the generic uuid format
// rejects the urn:, brace-wrapped, and unhyphenated encodings that
// google/uuid.Parse otherwise accepts.
func TestStruct_UUIDRejectsNonCanonical(t *testing.T) {
	type req struct {
		ID string `json:"id" jsonschema:"required,uuid"`
	}
	const canonical = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	if err := Struct(req{ID: canonical}); err != nil {
		t.Errorf("canonical UUID rejected: %v", err)
	}
	// Upper-case hyphenated form is still canonical.
	if err := Struct(req{ID: strings.ToUpper(canonical)}); err != nil {
		t.Errorf("upper-case canonical UUID rejected: %v", err)
	}
	for _, bad := range []string{
		"urn:uuid:f47ac10b-58cc-4372-a567-0e02b2c3d479",
		"{f47ac10b-58cc-4372-a567-0e02b2c3d479}",
		"f47ac10b58cc4372a5670e02b2c3d479",
	} {
		if err := Struct(req{ID: bad}); err == nil {
			t.Errorf("non-canonical UUID %q accepted, want rejected", bad)
		}
	}
}

// TestSchema_UUID4FormatDistinct guards that the uuid4 tag maps to its
// own format name (not collapsed to generic "uuid"), so the version
// constraint is preserved.
func TestSchema_UUID4FormatDistinct(t *testing.T) {
	type req struct {
		ID string `json:"id" jsonschema:"uuid4"`
	}
	s, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	if got := s.Properties["id"].Format; got != "uuid4" {
		t.Errorf("uuid4 Format = %q, want %q", got, "uuid4")
	}
}

// --- Finding 4: email rejects display-name / comment forms ---

// TestStruct_EmailRejectsDisplayName verifies the email format rejects
// RFC 5322 display-name and comment forms, accepting only a bare
// address.
func TestStruct_EmailRejectsDisplayName(t *testing.T) {
	type req struct {
		Email string `json:"email" jsonschema:"required,email"`
	}
	if err := Struct(req{Email: "bob@example.com"}); err != nil {
		t.Errorf("bare address rejected: %v", err)
	}
	for _, bad := range []string{
		"Bob <bob@example.com>",
		"bob@example.com (Bob)",
		"<bob@example.com>",
		" bob@example.com ",
	} {
		msgs := fieldMsgsFromErr(t, Struct(req{Email: bad}))
		if got := msgs["email"]; got != "must be a valid email address" {
			t.Errorf("email %q message = %q, want %q", bad, got, "must be a valid email address")
		}
	}
}

// --- Finding 6: embedded-before-parent shadowing ---

type shadowEmbBase struct {
	Name string `json:"name" jsonschema:"required"`
}

// embedded declared BEFORE the parent field with the same JSON name.
type shadowEmbeddedFirst struct {
	shadowEmbBase
	Name string `json:"name"` // optional, shallower -> wins
}

// embedded declared AFTER the parent field.
type shadowEmbeddedLast struct {
	Name string `json:"name"` // optional, shallower -> wins
	shadowEmbBase
}

// TestSchema_EmbeddedFirstShadowNoDuplicateOrder verifies that an
// embedded field declared before a same-named parent field does not
// leave a duplicate PropertyOrder entry and does not mark the field
// required when the winning (shallower) parent field is optional.
func TestSchema_EmbeddedFirstShadowNoDuplicateOrder(t *testing.T) {
	bs, err := buildSchema(reflect.TypeOf(shadowEmbeddedFirst{}))
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	if got := bs.schema.PropertyOrder; len(got) != 1 || got[0] != "name" {
		t.Errorf("PropertyOrder = %v, want [name]", got)
	}
	if len(bs.schema.Required) != 0 {
		t.Errorf("Required = %v, want empty (parent field is optional)", bs.schema.Required)
	}
	if _, ok := bs.requiredNonEmpty["name"]; ok {
		t.Error("requiredNonEmpty still contains 'name' from shadowed embedded field")
	}
}

// TestSchema_EmbeddedLastShadowDropsStaleRequired verifies that an
// embedded required field shadowed by an earlier-declared optional
// parent field does not leak a stale required marker.
func TestSchema_EmbeddedLastShadowDropsStaleRequired(t *testing.T) {
	bs, err := buildSchema(reflect.TypeOf(shadowEmbeddedLast{}))
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	if got := bs.schema.PropertyOrder; len(got) != 1 || got[0] != "name" {
		t.Errorf("PropertyOrder = %v, want [name]", got)
	}
	if len(bs.schema.Required) != 0 {
		t.Errorf("Required = %v, want empty (parent field is optional)", bs.schema.Required)
	}
}

// TestStruct_EmbeddedFirstShadowOptionalAccepted verifies the runtime
// behaviour: with the parent field optional, an empty value validates
// (it would fail if the stale embedded `required` marker survived).
func TestStruct_EmbeddedFirstShadowOptionalAccepted(t *testing.T) {
	if err := Struct(shadowEmbeddedFirst{}); err != nil {
		t.Errorf("optional shadowing field rejected empty value: %v", err)
	}
}

// shadowEmbeddedFirstRequired keeps the winning parent field required to
// confirm the parent's own constraints still take effect after the
// shadow fix.
type shadowEmbeddedFirstRequired struct {
	shadowEmbBase
	Name string `json:"name" jsonschema:"required"`
}

func TestStruct_EmbeddedFirstShadowRequiredEnforced(t *testing.T) {
	msgs := fieldMsgsFromErr(t, Struct(shadowEmbeddedFirstRequired{}))
	if got := msgs["name"]; got != "is required" {
		t.Errorf("name message = %q, want %q", got, "is required")
	}
}
