package validate

import (
	"testing"

	"github.com/go-playground/validator/v10"

	"github.com/bds421/rho-kit/core/apperror"
)

type basicReq struct {
	Name  string `json:"name" validate:"required,min=2,max=100"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age" validate:"gte=0,lte=150"`
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
	User    string  `json:"user" validate:"required"`
	Address address `json:"address" validate:"required"`
}

type address struct {
	City string `json:"city" validate:"required"`
	Zip  string `json:"zip" validate:"required,len=5"`
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
		FirstName string `json:"first_name" validate:"required"`
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
		Role string `json:"role" validate:"required,oneof=admin user viewer"`
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
		ID string `json:"id" validate:"required,uuid"`
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
		Callback string `json:"callback_url" validate:"required,url"`
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

// Register custom validations before any Struct() call freezes the validator.
func init() {
	if err := RegisterValidation("is_even", func(fl validator.FieldLevel) bool {
		return fl.Field().Int()%2 == 0
	}); err != nil {
		panic("register is_even: " + err.Error())
	}
}

func TestRegisterValidation_custom(t *testing.T) {
	type req struct {
		Count int `json:"count" validate:"is_even"`
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
	if ve.Fields[0].Message != "failed validation: is_even" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestRegisterValidation_afterFreeze(t *testing.T) {
	// Ensure Struct() has been called (which freezes registrations).
	_ = Struct(basicReq{Name: "a", Email: "a@b.c", Age: 1})

	err := RegisterValidation("should_fail", func(fl validator.FieldLevel) bool {
		return true
	})
	if err == nil {
		t.Fatal("expected error when registering after freeze")
	}
}

func TestStruct_nil(t *testing.T) {
	// Passing nil should not panic
	err := Struct((*basicReq)(nil))
	// go-playground/validator returns an error for nil pointers
	if err == nil {
		t.Log("nil passed validation (acceptable)")
	}
}

func TestStruct_sliceField(t *testing.T) {
	type req struct {
		Tags []string `json:"tags" validate:"min=1,max=10"`
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
		Score int `json:"score" validate:"gte=10"`
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
		Value int `json:"value" validate:"gt=0,lt=100"`
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
		Code string `json:"code" validate:"alphanum"`
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
		URL string `json:"url" validate:"contains=https"`
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
		IP string `json:"ip" validate:"ip"`
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
		Value string `json:"value" validate:"numeric"`
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
		Path string `json:"path" validate:"startswith=/api"`
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
		File string `json:"file" validate:"endswith=.json"`
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
		Host string `json:"host" validate:"hostname"`
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
		Items []string `json:"items" validate:"len=3"`
	}
	err := Struct(req{Items: []string{"a", "b"}})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must have exactly 3 items" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_maxStringViolation(t *testing.T) {
	type req struct {
		Name string `json:"name" validate:"max=5"`
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
		Count int `json:"count" validate:"max=10"`
	}
	err := Struct(req{Count: 20})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be at most 10" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestStruct_minIntViolation(t *testing.T) {
	type req struct {
		Count int `json:"count" validate:"min=5"`
	}
	err := Struct(req{Count: 2})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve, ok := apperror.AsValidation(err)
	if !ok {
		t.Fatalf("expected *apperror.ValidationError, got %T", err)
	}
	if ve.Fields[0].Message != "must be at least 5" {
		t.Errorf("message = %q", ve.Fields[0].Message)
	}
}

func TestMessage_coversTags(t *testing.T) {
	// Verify that known tags produce non-empty messages via real validation errors.
	type tagTest struct {
		Required string `validate:"required"`
	}
	err := get().Struct(tagTest{})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, ve := range err.(validator.ValidationErrors) {
		msg := message(ve)
		if msg == "" {
			t.Errorf("empty message for tag %q", ve.Tag())
		}
	}
}
