package validate

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

type basicReq struct {
	Name  string `json:"name" jsonschema:"required,min=2,max=100"`
	Email string `json:"email" jsonschema:"required,email"`
	Age   int    `json:"age" jsonschema:"gte=0,lte=150"`
}

func TestStruct_valid(t *testing.T) {
	req := basicReq{Name: "Alice", Email: "alice@example.com", Age: 30}
	if err := Struct(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStruct_missingRequired(t *testing.T) {
	req := basicReq{}
	err := Struct(req)
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	fieldMap := make(map[string]string)
	for _, f := range ve.Fields {
		fieldMap[f.Field] = f.Message
	}

	if msg, ok := fieldMap["name"]; !ok || msg != "is required" {
		t.Errorf("name: got %q, want 'is required'", msg)
	}
	if msg, ok := fieldMap["email"]; !ok || msg != "is required" {
		t.Errorf("email: got %q, want 'is required'", msg)
	}
}

func TestStruct_invalidEmail(t *testing.T) {
	req := basicReq{Name: "Bob", Email: "not-an-email", Age: 25}
	err := Struct(req)
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	if len(ve.Fields) != 1 {
		t.Fatalf("expected 1 field error, got %d: %v", len(ve.Fields), ve.Fields)
	}
	if ve.Fields[0].Field != "email" {
		t.Errorf("field = %q, want 'email'", ve.Fields[0].Field)
	}
	if ve.Fields[0].Message != "must be a valid email address" {
		t.Errorf("message = %q, want 'must be a valid email address'", ve.Fields[0].Message)
	}
}

func TestStruct_minMaxViolation(t *testing.T) {
	req := basicReq{Name: "A", Email: "a@b.com", Age: 200}
	err := Struct(req)
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	fieldMap := make(map[string]string)
	for _, f := range ve.Fields {
		fieldMap[f.Field] = f.Message
	}

	if msg, ok := fieldMap["name"]; !ok || msg != "must be at least 2 characters" {
		t.Errorf("name: got %q, want 'must be at least 2 characters'", msg)
	}
	if msg, ok := fieldMap["age"]; !ok || msg != "must be less than or equal to 150" {
		t.Errorf("age: got %q, want 'must be less than or equal to 150'", msg)
	}
}

type nestedReq struct {
	User    string  `json:"user" jsonschema:"required"`
	Address address `json:"address" jsonschema:"required"`
}

type address struct {
	City string `json:"city" jsonschema:"required"`
	Zip  string `json:"zip" jsonschema:"required,len=5"`
}

func TestStruct_nestedFields(t *testing.T) {
	req := nestedReq{User: "Alice", Address: address{}}
	err := Struct(req)
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	fieldMap := make(map[string]string)
	for _, f := range ve.Fields {
		fieldMap[f.Field] = f.Message
	}

	// Nested fields should use dot notation with JSON names
	if _, ok := fieldMap["address.city"]; !ok {
		t.Errorf("expected 'address.city' in fields, got %v", ve.Fields)
	}
	if _, ok := fieldMap["address.zip"]; !ok {
		t.Errorf("expected 'address.zip' in fields, got %v", ve.Fields)
	}
}

func TestStruct_usesJSONTags(t *testing.T) {
	type req struct {
		FirstName string `json:"first_name" jsonschema:"required"`
	}
	err := Struct(req{})
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	if len(ve.Fields) != 1 || ve.Fields[0].Field != "first_name" {
		t.Errorf("expected field 'first_name', got %v", ve.Fields)
	}
}

func TestStruct_oneofValidation(t *testing.T) {
	type req struct {
		Role string `json:"role" jsonschema:"required,oneof=admin user viewer"`
	}

	err := Struct(req{Role: "superadmin"})
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	if ve.Fields[0].Message != "must be one of: admin user viewer" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_uuidValidation(t *testing.T) {
	type req struct {
		ID string `json:"id" jsonschema:"required,uuid"`
	}

	err := Struct(req{ID: "not-a-uuid"})
	if err == nil {
		t.Fatal("expected validation error")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}

	if ve.Fields[0].Message != "must be a valid UUID" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_urlValidation(t *testing.T) {
	type req struct {
		Callback string `json:"callback_url" jsonschema:"required,url"`
	}

	if err := Struct(req{Callback: "https://example.com/hook"}); err != nil {
		t.Errorf("valid URL rejected: %v", err)
	}

	err := Struct(req{Callback: "not a url"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestStruct_isValidationError(t *testing.T) {
	req := basicReq{}
	err := Struct(req)
	if !apperror.IsValidation(err) {
		t.Error("expected IsValidation to return true")
	}
}

func TestStruct_errorStringFormat(t *testing.T) {
	req := basicReq{Age: 30}
	err := Struct(req)
	if err == nil {
		t.Fatal("expected error")
	}

	// The error string should contain semicolon-separated field errors
	s := err.Error()
	if s == "" {
		t.Error("error string should not be empty")
	}
}

// Register custom formats before any Struct() call freezes the validator.
func init() {
	if err := RegisterFormat("even", func(v any) error {
		// santhosh-tekuri passes numeric values as json.Number; fall
		// back to float64 (and reject strings) so the format is
		// usable for both integer and number-typed fields.
		switch n := v.(type) {
		case float64:
			if int64(n)%2 != 0 || n != float64(int64(n)) {
				return fmt.Errorf("not even")
			}
			return nil
		case int:
			if n%2 != 0 {
				return fmt.Errorf("not even")
			}
			return nil
		case int64:
			if n%2 != 0 {
				return fmt.Errorf("not even")
			}
			return nil
		}
		// Strings or other shapes — santhosh-tekuri may pass a
		// json.Number depending on the decoder; use Stringer fallback.
		if s, ok := v.(interface{ String() string }); ok {
			if len(s.String()) > 0 && s.String()[len(s.String())-1]%2 != 0 {
				return fmt.Errorf("not even")
			}
			return nil
		}
		return fmt.Errorf("unsupported value for even format")
	}); err != nil {
		panic("register even: " + err.Error())
	}
}

func TestRegisterFormat_custom(t *testing.T) {
	type req struct {
		Count int `json:"count" jsonschema:"format=even"`
	}

	if err := Struct(req{Count: 4}); err != nil {
		t.Errorf("even number should pass: %v", err)
	}

	err := Struct(req{Count: 3})
	if err == nil {
		t.Fatal("odd number should fail")
	}

	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be a valid even" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestRegisterFormat_afterFreeze(t *testing.T) {
	// Ensure Struct() has been called (which freezes registrations).
	_ = Struct(basicReq{Name: "ab", Email: "a@b.co", Age: 1})

	err := RegisterFormat("should_fail_secret_token", func(_ any) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error when registering after freeze")
	}
	if strings.Contains(err.Error(), "should_fail_secret_token") || strings.Contains(err.Error(), "secret_token") {
		t.Fatalf("error leaked validation tag: %v", err)
	}
}

func TestRegisterFormat_rejectsEmptyName(t *testing.T) {
	v := New()
	if err := v.RegisterFormat("", func(_ any) error { return nil }); err == nil {
		t.Fatal("expected error for empty format name")
	}
}

func TestRegisterFormat_rejectsNilFn(t *testing.T) {
	v := New()
	if err := v.RegisterFormat("nilfn", nil); err == nil {
		t.Fatal("expected error for nil format function")
	}
}

func TestStruct_nil(t *testing.T) {
	// Passing nil should not panic
	err := Struct((*basicReq)(nil))
	if err != nil {
		t.Logf("nil pointer returned %v (acceptable)", err)
	}
}

func TestStruct_sliceField(t *testing.T) {
	type req struct {
		Tags []string `json:"tags" jsonschema:"min=1,max=10"`
	}

	if err := Struct(req{Tags: []string{"go"}}); err != nil {
		t.Errorf("valid slice rejected: %v", err)
	}

	err := Struct(req{Tags: nil})
	if err == nil {
		t.Fatal("expected validation error for empty slice")
	}
}

func TestStruct_gteViolation(t *testing.T) {
	type req struct {
		Score int `json:"score" jsonschema:"gte=10"`
	}
	err := Struct(req{Score: 5})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be greater than or equal to 10" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_gtLtViolation(t *testing.T) {
	type req struct {
		Value int `json:"value" jsonschema:"gt=0,lt=100"`
	}
	err := Struct(req{Value: 0})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be greater than 0" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_alphanumViolation(t *testing.T) {
	type req struct {
		Code string `json:"code" jsonschema:"alphanum"`
	}
	err := Struct(req{Code: "abc-123"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must contain only alphanumeric characters" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_containsViolation(t *testing.T) {
	type req struct {
		URL string `json:"url" jsonschema:"contains=https"`
	}
	err := Struct(req{URL: "http://example.com"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must contain https" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_ipViolation(t *testing.T) {
	type req struct {
		IP string `json:"ip" jsonschema:"ip"`
	}
	err := Struct(req{IP: "not-an-ip"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be a valid IP address" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_numericViolation(t *testing.T) {
	type req struct {
		Value string `json:"value" jsonschema:"numeric"`
	}
	err := Struct(req{Value: "abc"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be numeric" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_startsWithViolation(t *testing.T) {
	type req struct {
		Path string `json:"path" jsonschema:"startswith=/api"`
	}
	err := Struct(req{Path: "/web/test"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must start with /api" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_endsWithViolation(t *testing.T) {
	type req struct {
		File string `json:"file" jsonschema:"endswith=.json"`
	}
	err := Struct(req{File: "data.xml"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must end with .json" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_hostnameViolation(t *testing.T) {
	type req struct {
		Host string `json:"host" jsonschema:"hostname"`
	}
	err := Struct(req{Host: "invalid host name!"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be a valid hostname" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_lenSliceViolation(t *testing.T) {
	type req struct {
		Items []string `json:"items" jsonschema:"len=3"`
	}
	err := Struct(req{Items: []string{"a", "b"}})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must have at least 3 items" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_maxStringViolation(t *testing.T) {
	type req struct {
		Name string `json:"name" jsonschema:"max=5"`
	}
	err := Struct(req{Name: "toolongname"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be at most 5 characters" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_maxIntViolation(t *testing.T) {
	type req struct {
		Count int `json:"count" jsonschema:"max=10"`
	}
	err := Struct(req{Count: 20})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be less than or equal to 10" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_minIntViolation(t *testing.T) {
	type req struct {
		Count int `json:"count" jsonschema:"min=5"`
	}
	err := Struct(req{Count: 2})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be greater than or equal to 5" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestSchemaFor_exposesInferredSchema(t *testing.T) {
	type req struct {
		Name string `json:"name" jsonschema:"required,min=2"`
	}
	s, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("Type = %q, want object", s.Type)
	}
	if len(s.Required) != 1 || s.Required[0] != "name" {
		t.Errorf("Required = %v, want [name]", s.Required)
	}
	nameProp, ok := s.Properties["name"]
	if !ok {
		t.Fatalf("missing 'name' in properties: %v", s.Properties)
	}
	if nameProp.MinLength == nil || *nameProp.MinLength != 2 {
		t.Errorf("MinLength = %v, want 2", nameProp.MinLength)
	}
}

// TestSchema_RejectsCyclicStruct verifies the walker's cycle guard
// fires when a struct field recursively references its own type via a
// pointer chain — the kit refuses to emit a schema rather than walking
// forever at validate time.
func TestSchema_RejectsCyclicStruct(t *testing.T) {
	type node struct {
		Next *node `json:"next"`
	}
	_, err := SchemaFor[node]()
	if err == nil {
		t.Fatal("expected ErrCyclicSchema for self-referential struct")
	}
	if !errors.Is(err, ErrCyclicSchema) {
		t.Errorf("error = %v, want ErrCyclicSchema wrap", err)
	}
}

// TestSchema_RejectsCyclicSliceOfSelf verifies cycle detection works
// through a slice of the parent type as well (the cache key for slices
// is distinct from the struct's, so a separate visit guard is needed).
func TestSchema_RejectsCyclicSliceOfSelf(t *testing.T) {
	type tree struct {
		Children []tree `json:"children"`
	}
	_, err := SchemaFor[tree]()
	if err == nil {
		t.Fatal("expected ErrCyclicSchema for slice-of-self")
	}
	if !errors.Is(err, ErrCyclicSchema) {
		t.Errorf("error = %v, want ErrCyclicSchema wrap", err)
	}
}

// TestSchema_RejectsUnsupportedType covers channel / func / complex
// fields, which have no JSON-Schema equivalent.
func TestSchema_RejectsUnsupportedType(t *testing.T) {
	type req struct {
		Ch chan int `json:"ch"`
	}
	_, err := SchemaFor[req]()
	if err == nil {
		t.Fatal("expected ErrUnsupportedType for chan field")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("error = %v, want ErrUnsupportedType wrap", err)
	}
}

// TestSchema_NonStringMapKeyRejected ensures map keys must be strings
// (JSON object keys are always strings).
func TestSchema_NonStringMapKeyRejected(t *testing.T) {
	type req struct {
		M map[int]string `json:"m"`
	}
	_, err := SchemaFor[req]()
	if err == nil {
		t.Fatal("expected ErrUnsupportedType for non-string map key")
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Errorf("error = %v, want ErrUnsupportedType wrap", err)
	}
}

// TestStruct_FieldErrorOrder_IsDeterministic verifies field errors come
// back in struct-declaration order regardless of the underlying
// validator's iteration order over a map. The walker records every
// property's declaration index in a fieldOrder map; the error collector
// sorts on it.
func TestStruct_FieldErrorOrder_IsDeterministic(t *testing.T) {
	type req struct {
		Alpha   string `json:"alpha"   jsonschema:"required"`
		Bravo   string `json:"bravo"   jsonschema:"required"`
		Charlie string `json:"charlie" jsonschema:"required"`
		Delta   string `json:"delta"   jsonschema:"required"`
		Echo    string `json:"echo"    jsonschema:"required"`
	}
	// Repeat to surface any non-determinism that would manifest as map
	// iteration order flapping between runs of the same process.
	const iterations = 20
	for i := 0; i < iterations; i++ {
		err := Struct(req{})
		if err == nil {
			t.Fatalf("iter %d: expected validation error", i)
		}
		ve, ok := apperror.AsValidation(err)
		if !ok {
			t.Fatalf("iter %d: not a ValidationError: %T", i, err)
		}
		got := make([]string, len(ve.Fields))
		for j, f := range ve.Fields {
			got[j] = f.Field
		}
		want := []string{"alpha", "bravo", "charlie", "delta", "echo"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("iter %d: order = %v, want %v", i, got, want)
		}
	}
}

// TestJSONSchemaTag_DescriptionWithCommas confirms the kit-extension
// jsonschema:"required,..." parser strips "required" and re-joins the
// remaining segments so a description that contains an actual comma
// survives.
func TestJSONSchemaTag_DescriptionWithCommas(t *testing.T) {
	type req struct {
		Note string `json:"note" jsonschema:"required,First clause, second clause"`
	}
	s, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	if len(s.Required) != 1 || s.Required[0] != "note" {
		t.Errorf("Required = %v, want [note]", s.Required)
	}
	prop, ok := s.Properties["note"]
	if !ok {
		t.Fatalf("missing 'note' in properties")
	}
	if prop.Description != "First clause, second clause" {
		t.Errorf("Description = %q, want %q", prop.Description, "First clause, second clause")
	}
}

// TestJSONSchemaTag_DescriptionOnlyNoRequired covers the description-only
// form of the jsonschema tag (without the "required" marker).
func TestJSONSchemaTag_DescriptionOnlyNoRequired(t *testing.T) {
	type req struct {
		Note string `json:"note" jsonschema:"Just a description"`
	}
	s, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	if len(s.Required) != 0 {
		t.Errorf("Required = %v, want empty", s.Required)
	}
	if s.Properties["note"].Description != "Just a description" {
		t.Errorf("Description = %q", s.Properties["note"].Description)
	}
}

// TestStruct_RequiredViaJSONSchemaTag confirms the kit extension
// jsonschema:"required" alone marks a field required, producing the
// same "is required" message as a bare required keyword.
func TestStruct_RequiredViaJSONSchemaTag(t *testing.T) {
	type req struct {
		Title string `json:"title" jsonschema:"required,Document title"`
	}
	err := Struct(req{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("not a ValidationError: %T", err)
	}
	if len(ve.Fields) != 1 || ve.Fields[0].Field != "title" || ve.Fields[0].Message != "is required" {
		t.Errorf("fields = %v", ve.Fields)
	}
}

// TestSchemaFor_StableAcrossCalls verifies the per-type cache returns
// the same *Schema instance for repeated calls on the same type — both
// a freshness check on the singleton and a guard against the cache
// silently rebuilding on every Sign hit.
func TestSchemaFor_StableAcrossCalls(t *testing.T) {
	type req struct {
		X string `json:"x"`
	}
	a, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor #1: %v", err)
	}
	b, err := SchemaFor[req]()
	if err != nil {
		t.Fatalf("SchemaFor #2: %v", err)
	}
	if a != b {
		t.Errorf("SchemaFor returned distinct instances across calls: %p vs %p", a, b)
	}
}

// TestStruct_EmbeddedStructFields verifies that embedded struct fields
// flatten into the parent's required list and field order, mirroring
// encoding/json's behaviour.
func TestStruct_EmbeddedStructFields(t *testing.T) {
	type base struct {
		ID string `json:"id" jsonschema:"required"`
	}
	type req struct {
		base
		Name string `json:"name" jsonschema:"required"`
	}
	err := Struct(req{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("not a ValidationError: %T", err)
	}
	got := map[string]string{}
	for _, f := range ve.Fields {
		got[f.Field] = f.Message
	}
	if got["id"] != "is required" || got["name"] != "is required" {
		t.Errorf("fields = %v, want id+name required", ve.Fields)
	}
}
