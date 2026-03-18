package apperror

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Code identifies the category of an application error.
type Code string

const (
	CodeNotFound        Code = "NOT_FOUND"
	CodeValidation      Code = "VALIDATION"
	CodeConflict        Code = "CONFLICT"
	CodePermanent       Code = "PERMANENT"
	CodeAuthRequired    Code = "AUTH_REQUIRED"
	CodeRateLimit       Code = "RATE_LIMITED"
	CodeOperationFailed Code = "OPERATION_FAILED"
	CodeForbidden       Code = "FORBIDDEN"
)

// FieldError represents a single field-level validation error.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// AppError is the common interface for all application error types.
// Use the Is*/As* functions rather than type-asserting directly.
type AppError interface {
	error
	ErrorCode() Code
}

// --- Concrete error types ---

// NotFoundError indicates a requested entity does not exist.
type NotFoundError struct {
	Entity   string
	EntityID any
	Message  string
}

func (e *NotFoundError) Error() string   { return e.Message }
func (e *NotFoundError) ErrorCode() Code { return CodeNotFound }

// ValidationError indicates invalid input, optionally with field-level details.
type ValidationError struct {
	Message string
	Fields  []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Fields) > 0 {
		msgs := make([]string, len(e.Fields))
		for i, f := range e.Fields {
			msgs[i] = f.Field + ": " + f.Message
		}
		return strings.Join(msgs, "; ")
	}
	return e.Message
}

func (e *ValidationError) ErrorCode() Code { return CodeValidation }

// ConflictError indicates a resource conflict (duplicate, version mismatch).
type ConflictError struct {
	Message string
}

func (e *ConflictError) Error() string   { return e.Message }
func (e *ConflictError) ErrorCode() Code { return CodeConflict }

// PermanentError indicates a non-retryable failure.
type PermanentError struct {
	Message string
	cause   error
}

func (e *PermanentError) Error() string   { return e.Message }
func (e *PermanentError) Unwrap() error   { return e.cause }
func (e *PermanentError) ErrorCode() Code { return CodePermanent }

// AuthRequiredError indicates missing or invalid authentication.
type AuthRequiredError struct {
	Message string
}

func (e *AuthRequiredError) Error() string   { return e.Message }
func (e *AuthRequiredError) ErrorCode() Code { return CodeAuthRequired }

// ForbiddenError indicates the caller is authenticated but lacks permission.
type ForbiddenError struct {
	Message string
}

func (e *ForbiddenError) Error() string   { return e.Message }
func (e *ForbiddenError) ErrorCode() Code { return CodeForbidden }

// RateLimitError indicates a rate limit or quota has been exceeded.
type RateLimitError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string   { return e.Message }
func (e *RateLimitError) ErrorCode() Code { return CodeRateLimit }

// OperationFailedError indicates a server-side failure with a client-safe message.
type OperationFailedError struct {
	Message string
	cause   error
}

func (e *OperationFailedError) Error() string   { return e.Message }
func (e *OperationFailedError) Unwrap() error   { return e.cause }
func (e *OperationFailedError) ErrorCode() Code { return CodeOperationFailed }

// --- Constructors ---

// NewNotFound creates a NotFoundError for the given entity type and identifier.
func NewNotFound(entity string, id any) error {
	return &NotFoundError{
		Entity:   entity,
		EntityID: id,
		Message:  fmt.Sprintf("%s %v not found", entity, id),
	}
}

// NewValidation creates a ValidationError with a simple message (no field details).
func NewValidation(msg string) error {
	return &ValidationError{Message: msg}
}

// NewFieldValidation creates a ValidationError with structured field-level errors.
// Panics if called with zero arguments — use [NewValidation] for message-only errors.
func NewFieldValidation(fields ...FieldError) error {
	if len(fields) == 0 {
		panic("apperror: NewFieldValidation requires at least one FieldError; use NewValidation for message-only errors")
	}
	return &ValidationError{Fields: fields}
}

// NewConflict creates a ConflictError.
func NewConflict(msg string) error {
	return &ConflictError{Message: msg}
}

// NewPermanent creates a non-retryable PermanentError.
func NewPermanent(msg string) error {
	return &PermanentError{Message: msg}
}

// NewPermanentWithCause creates a non-retryable PermanentError that wraps an underlying cause.
func NewPermanentWithCause(msg string, cause error) error {
	return &PermanentError{Message: msg, cause: cause}
}

// NewAuthRequired creates an AuthRequiredError.
func NewAuthRequired(msg string) error {
	return &AuthRequiredError{Message: msg}
}

// NewForbidden creates a ForbiddenError.
func NewForbidden(msg string) error {
	return &ForbiddenError{Message: msg}
}

// NewRateLimit creates a RateLimitError with the given retry-after duration.
// Pass 0 for retryAfter if no specific retry window is known.
func NewRateLimit(msg string, retryAfter time.Duration) error {
	return &RateLimitError{Message: msg, RetryAfter: retryAfter}
}

// NewOperationFailed creates an OperationFailedError with a client-safe message.
func NewOperationFailed(msg string) error {
	return &OperationFailedError{Message: msg}
}

// NewOperationFailedWithCause creates an OperationFailedError that wraps an underlying cause.
func NewOperationFailedWithCause(msg string, cause error) error {
	return &OperationFailedError{Message: msg, cause: cause}
}

// --- Predicates ---

// IsNotFound reports whether err contains a NotFoundError.
func IsNotFound(err error) bool {
	var target *NotFoundError
	return errors.As(err, &target)
}

// IsValidation reports whether err contains a ValidationError.
func IsValidation(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

// IsConflict reports whether err contains a ConflictError.
func IsConflict(err error) bool {
	var target *ConflictError
	return errors.As(err, &target)
}

// IsPermanent reports whether err contains a PermanentError.
func IsPermanent(err error) bool {
	var target *PermanentError
	return errors.As(err, &target)
}

// IsAuthRequired reports whether err contains an AuthRequiredError.
func IsAuthRequired(err error) bool {
	var target *AuthRequiredError
	return errors.As(err, &target)
}

// IsForbidden reports whether err contains a ForbiddenError.
func IsForbidden(err error) bool {
	var target *ForbiddenError
	return errors.As(err, &target)
}

// IsRateLimit reports whether err contains a RateLimitError.
func IsRateLimit(err error) bool {
	var target *RateLimitError
	return errors.As(err, &target)
}

// IsOperationFailed reports whether err contains an OperationFailedError.
func IsOperationFailed(err error) bool {
	var target *OperationFailedError
	return errors.As(err, &target)
}

// --- Extractors ---

// AsValidation extracts the *ValidationError from the error chain.
func AsValidation(err error) (*ValidationError, bool) {
	var target *ValidationError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsRateLimit extracts the *RateLimitError from the error chain.
func AsRateLimit(err error) (*RateLimitError, bool) {
	var target *RateLimitError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsNotFound extracts the *NotFoundError from the error chain.
func AsNotFound(err error) (*NotFoundError, bool) {
	var target *NotFoundError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsConflict extracts the *ConflictError from the error chain.
func AsConflict(err error) (*ConflictError, bool) {
	var target *ConflictError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsPermanent extracts the *PermanentError from the error chain.
func AsPermanent(err error) (*PermanentError, bool) {
	var target *PermanentError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsAuthRequired extracts the *AuthRequiredError from the error chain.
func AsAuthRequired(err error) (*AuthRequiredError, bool) {
	var target *AuthRequiredError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsForbidden extracts the *ForbiddenError from the error chain.
func AsForbidden(err error) (*ForbiddenError, bool) {
	var target *ForbiddenError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// AsOperationFailed extracts the *OperationFailedError from the error chain.
func AsOperationFailed(err error) (*OperationFailedError, bool) {
	var target *OperationFailedError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
