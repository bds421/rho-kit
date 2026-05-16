// Package validate provides struct validation driven by JSON Schema,
// with field-level error reporting via [core/apperror.ValidationError].
// See [validate.go] for the full design notes (wave 124 migration off
// go-playground/validator onto jsonschema-go + santhosh-tekuri/jsonschema/v6).
package validate
