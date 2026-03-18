// Package validate provides struct validation using go-playground/validator,
// converting validation errors into apperror.ValidationError with field-level
// details. It uses JSON tag names for field references so error messages match
// the API contract.
package validate

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-playground/validator/v10"

	"github.com/bds421/rho-kit/core/apperror"
)

var (
	instance *validator.Validate
	once     sync.Once
	frozen   atomic.Bool // set after first Struct() call to detect late registrations
)

// get returns the singleton validator instance.
func get() *validator.Validate {
	once.Do(func() {
		instance = validator.New()
		// Use JSON tag names for field names in error messages.
		instance.RegisterTagNameFunc(func(fld reflect.StructField) string {
			name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
			if name == "-" || name == "" {
				return fld.Name
			}
			return name
		})
	})
	return instance
}

// Struct validates a struct using go-playground/validator tags.
// Returns nil on success or an *apperror.ValidationError with field-level details.
func Struct(s any) error {
	frozen.CompareAndSwap(false, true)
	err := get().Struct(s)
	if err == nil {
		return nil
	}

	validationErrors, ok := err.(validator.ValidationErrors)
	if !ok {
		return apperror.NewValidation(err.Error())
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

// RegisterValidation registers a custom validation tag on the shared validator.
// The tag is globally available to all subsequent Struct() calls.
//
// Call this during program init only (e.g. in init() or main before serving).
// The underlying go-playground/validator is not safe for concurrent
// registration while Struct() calls are in flight. Returns an error if
// called after the first Struct() call, indicating a data race risk.
func RegisterValidation(tag string, fn validator.Func) error {
	if frozen.Load() {
		return fmt.Errorf("validate: RegisterValidation(%q) called after Struct(); this is not safe for concurrent use — call during init only", tag)
	}
	return get().RegisterValidation(tag, fn)
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
