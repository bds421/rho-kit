// Package validate provides struct validation using go-playground/validator,
// converting validation errors into apperror.ValidationError with field-level
// details. It uses JSON tag names for field references so error messages match
// the API contract.
//
// asvs: V5.1.3
package validate

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-playground/validator/v10"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// Func is the signature for custom validation tags. Wraps the underlying
// validator's FieldLevel so callers don't depend on the third-party type
// in their public API surface.
type Func = validator.Func

// Validator wraps go-playground/validator with kit conventions: JSON tag
// field names, apperror.ValidationError conversion, and concurrency-safe
// custom-tag registration. Construct via [New] for an isolated instance.
type Validator struct {
	v      *validator.Validate
	mu     sync.Mutex // serialises RegisterValidation against Struct
	frozen atomic.Bool
}

// New constructs an isolated [Validator]. Tests and callers that need
// independent validators (e.g. conflicting custom tags) should use this
// rather than the package-level [Struct] / [RegisterValidation].
func New() *Validator {
	inner := validator.New()
	inner.RegisterTagNameFunc(jsonTagNameFunc)
	return &Validator{v: inner}
}

// Struct validates s and returns nil on success or an
// *apperror.ValidationError on failure.
//
// A non-struct or nil-pointer input is a programming bug — the underlying
// validator would return InvalidValidationError, which we wrap as
// [apperror.NewOperationFailedWithCause] so misuse surfaces as a server
// error rather than user input being blamed.
func (V *Validator) Struct(s any) error {
	V.mu.Lock()
	V.frozen.CompareAndSwap(false, true)
	V.mu.Unlock()
	return wrapValidate(V.v, s)
}

// RegisterValidation registers a custom tag. Must be called before the
// first [Validator.Struct] invocation; afterwards it returns an error so
// concurrent Struct calls cannot race the validator's tag-function map.
func (V *Validator) RegisterValidation(tag string, fn Func) error {
	V.mu.Lock()
	defer V.mu.Unlock()
	if V.frozen.Load() {
		return errors.New("validate: RegisterValidation called after Struct(); register custom tags during init")
	}
	return V.v.RegisterValidation(tag, fn)
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

// Struct validates s using the package-level [Validator]. Equivalent to
// `New().Struct(s)` for the singleton instance; the first call freezes
// the singleton's custom-tag registry.
func Struct(s any) error {
	return singleton().Struct(s)
}

// RegisterValidation registers a custom validation tag on the package-level
// validator. Call during init only — see [Validator.RegisterValidation].
func RegisterValidation(tag string, fn Func) error {
	return singleton().RegisterValidation(tag, fn)
}

// --- internals ---

func jsonTagNameFunc(fld reflect.StructField) string {
	name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
	if name == "-" || name == "" {
		return fld.Name
	}
	return name
}

func wrapValidate(v *validator.Validate, s any) error {
	err := v.Struct(s)
	if err == nil {
		return nil
	}

	var invalid *validator.InvalidValidationError
	if errors.As(err, &invalid) {
		return apperror.NewOperationFailedWithCause("validate: invalid input passed to Struct (programming error)", invalid)
	}

	validationErrors, ok := err.(validator.ValidationErrors)
	if !ok {
		return apperror.NewOperationFailedWithCause("validate: validator returned unexpected error", err)
	}

	fields := make([]apperror.FieldError, 0, len(validationErrors))
	for _, ve := range validationErrors {
		fields = append(fields, apperror.FieldError{
			Field:   fieldPath(ve),
			Message: message(ve),
		})
	}
	return apperror.NewFieldValidation(fields...)
}

// fieldPath returns the JSON-tagged field path (e.g. "address.city" for nested structs).
func fieldPath(fe validator.FieldError) string {
	ns := fe.Namespace()
	// Strip the top-level struct name (e.g. "CreateRequest.name" → "name")
	if idx := strings.IndexByte(ns, '.'); idx >= 0 {
		return ns[idx+1:]
	}
	return fe.Field()
}

// message converts a validator.FieldError into a human-readable message,
// matching the project convention of lowercase descriptive messages.
func message(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "url", "uri":
		return "must be a valid URL"
	case "uuid", "uuid4":
		return "must be a valid UUID"
	case "min":
		if fe.Kind() == reflect.String {
			return "must be at least " + fe.Param() + " characters"
		}
		return "must be at least " + fe.Param()
	case "max":
		if fe.Kind() == reflect.String {
			return "must be at most " + fe.Param() + " characters"
		}
		return "must be at most " + fe.Param()
	case "len":
		if fe.Kind() == reflect.String {
			return "must be exactly " + fe.Param() + " characters"
		}
		return "must have exactly " + fe.Param() + " items"
	case "gte":
		return "must be greater than or equal to " + fe.Param()
	case "lte":
		return "must be less than or equal to " + fe.Param()
	case "gt":
		return "must be greater than " + fe.Param()
	case "lt":
		return "must be less than " + fe.Param()
	case "oneof":
		return "must be one of: " + fe.Param()
	case "excludesall":
		return "contains disallowed characters"
	case "alphanum":
		return "must contain only alphanumeric characters"
	case "alpha":
		return "must contain only letters"
	case "numeric":
		return "must be numeric"
	case "boolean":
		return "must be a boolean"
	case "ip":
		return "must be a valid IP address"
	case "cidr":
		return "must be a valid CIDR notation"
	case "hostname":
		return "must be a valid hostname"
	case "startswith":
		return "must start with " + fe.Param()
	case "endswith":
		return "must end with " + fe.Param()
	case "contains":
		return "must contain " + fe.Param()
	case "datetime":
		return "must be a valid datetime (" + fe.Param() + ")"
	default:
		return "failed validation: " + fe.Tag()
	}
}

