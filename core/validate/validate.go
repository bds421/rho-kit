// Package validate provides struct validation using go-playground/validator,
// converting validation errors into apperror.ValidationError with field-level
// details. It uses JSON tag names for field references so error messages match
// the API contract.
//
// asvs: V5.1.3
package validate

import (
	"errors"
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
	// regMu serialises RegisterValidation against Struct so the underlying
	// go-playground validator's tag-function map can never be mutated and
	// read concurrently. Without this, the frozen-bool TOCTOU window allows
	// a goroutine that just observed frozen=false to call RegisterValidation
	// in parallel with a concurrent Struct() that already crossed the
	// CompareAndSwap line, producing a data race in the validator's cache.
	regMu sync.Mutex
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
//
// A non-struct or nil-pointer input is a programming bug (the validator
// would otherwise return InvalidValidationError, which the v1 wrapper
// surfaced as a 400 ValidationError to the client). v2 returns
// apperror.NewOperationFailedWithCause for that case so misuse shows up
// as a server error rather than user input being blamed.
func Struct(s any) error {
	// Acquire regMu so a concurrent RegisterValidation cannot mutate the
	// validator's tag-function map while we're reading it. The mutex is
	// only contended on the very first Struct call (registrations should
	// happen during init); after that the atomic frozen flag short-circuits
	// future RegisterValidation attempts with an explicit error.
	regMu.Lock()
	frozen.CompareAndSwap(false, true)
	v := get()
	regMu.Unlock()
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

// New constructs an isolated *validator.Validate for callers that need
// independent instances (e.g. tests with conflicting custom tags). The
// returned validator does not share the singleton's frozen state, but
// it also does not get the singleton's pre-registered JSON tag-name
// hook — wire that up explicitly if needed.
func New() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return fld.Name
		}
		return name
	})
	return v
}

// RegisterValidation registers a custom validation tag on the shared validator.
// The tag is globally available to all subsequent Struct() calls.
//
// Call this during program init only (e.g. in init() or main before serving).
// The underlying go-playground/validator is not safe for concurrent
// registration while Struct() calls are in flight. Returns an error if
// called after the first Struct() call, indicating a data race risk.
func RegisterValidation(tag string, fn validator.Func) error {
	regMu.Lock()
	defer regMu.Unlock()
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
