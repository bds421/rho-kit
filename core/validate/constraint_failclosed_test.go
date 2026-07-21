package validate_test

import (
	"strings"
	"testing"

	"github.com/bds421/rho-kit/core/v2/validate"
)

// TestSchemaFor_RejectsUnknownFormat is the regression pin for
// review-01: typo'd format= names must fail schema generation, not
// silently accept every value.
func TestSchemaFor_RejectsUnknownFormat(t *testing.T) {
	type bad struct {
		Path string `json:"path" jsonschema:"format=starts-wtih:/api"`
	}
	_, err := validate.SchemaFor[bad]()
	if err == nil {
		t.Fatal("expected schema build error for unknown format")
	}
	if !strings.Contains(err.Error(), "unknown format") && !strings.Contains(err.Error(), "format") {
		t.Fatalf("error should mention format: %v", err)
	}
}

// TestSchemaFor_RejectsMalformedMaxConstraint pins fail-closed parse
// of max= values (letter O typo must not drop the bound).
func TestSchemaFor_RejectsMalformedMaxConstraint(t *testing.T) {
	type bad struct {
		Name string `json:"name" jsonschema:"max=1O0"`
	}
	_, err := validate.SchemaFor[bad]()
	if err == nil {
		t.Fatal("expected schema build error for malformed max")
	}
	if !strings.Contains(err.Error(), "max") && !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error should mention invalid max: %v", err)
	}
}

// TestSchemaFor_AcceptsKnownFormat still builds for builtins.
func TestSchemaFor_AcceptsKnownFormat(t *testing.T) {
	type good struct {
		Email string `json:"email" jsonschema:"format=email"`
	}
	if _, err := validate.SchemaFor[good](); err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
}

// TestSchemaFor_RejectsBareUnknownFormat pins fail-closed schema
// generation for typo'd bare format= names (not only parametric
// prefixes). santhosh-tekuri treats unknown formats as always-valid,
// so schema build must reject them.
func TestSchemaFor_RejectsBareUnknownFormat(t *testing.T) {
	type bad struct {
		Token string `json:"token" jsonschema:"format=not-a-real-format"`
	}
	_, err := validate.SchemaFor[bad]()
	if err == nil {
		t.Fatal("expected schema build error for bare unknown format")
	}
	if !strings.Contains(err.Error(), "unknown format") && !strings.Contains(err.Error(), "not-a-real-format") {
		t.Fatalf("error should mention unknown format: %v", err)
	}
}
