package apperror

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Code identifies the category of an application error.
// Codes are transport-agnostic: each transport adapter (httpx, grpcx) maps
// codes to the appropriate transport status. The error model expresses domain
// intent; transport layers choose the appropriate mapping.
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
	CodeUnavailable     Code = "UNAVAILABLE"
	// CodeStorageFull indicates the backing store cannot accept the write
	// because capacity (disk, quota, partition limit) is exhausted. The
	// error is retryable: capacity may free up after some operator-managed
	// interval. Transport adapters map it to HTTP 507 Insufficient Storage.
	CodeStorageFull Code = "STORAGE_FULL"
)

// AllCodes returns every kit-defined error Code in a stable order.
// Transport adapters (httpx, grpcx) use this in tests to enforce
// that their Code→status maps stay exhaustive as new Codes are
// added here. Wave 144 introduced this single source of truth so
// adding a new Code surfaces the missing mapping at test time
// rather than at first runtime hit.
//
// Order is alphabetical by constant name for deterministic diffs.
func AllCodes() []Code {
	return []Code{
		CodeAuthRequired,
		CodeConflict,
		CodeForbidden,
		CodeNotFound,
		CodeOperationFailed,
		CodePermanent,
		CodeRateLimit,
		CodeStorageFull,
		CodeUnavailable,
		CodeValidation,
	}
}

// FieldError represents a single field-level validation error.
//
// Code uses the typed [Code] alphabet so the JSON shape ("NOT_FOUND",
// "VALIDATION", …) cannot drift between backend and client. Untyped string
// codes (the v1 form) are not accepted — use one of the documented
// [Code] constants or define a new one in this package.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Code    Code   `json:"code,omitempty"`
}

// AppError is the common interface for all application error types.
// This interface is sealed: it is only implemented by types within the
// apperror package. External packages should use the constructor functions
// (NewNotFound, NewValidation, etc.) and predicate functions (IsNotFound,
// IsValidation, etc.) rather than implementing AppError directly.
//
// Retryable reports whether the operation that produced this error can be
// retried with a reasonable expectation of success. Transport layers and
// retry middleware (e.g. resilience/retry) use this to decide whether to
// retry automatically. See [ShouldRetry] for a convenient predicate.
type AppError interface {
	error
	ErrorCode() Code
	Retryable() bool
}

// --- Concrete error types ---

// NotFoundError indicates a requested entity does not exist. The optional
// cause is preserved through [errors.Unwrap] so callers can chain typed
// classification (apperror) with the underlying transport error.
type NotFoundError struct {
	Entity   string
	EntityID any
	Message  string
	cause    error
}

func (e *NotFoundError) Error() string   { return e.Message }
func (e *NotFoundError) Unwrap() error   { return e.cause }
func (e *NotFoundError) ErrorCode() Code { return CodeNotFound }
func (e *NotFoundError) Retryable() bool { return false }

// ValidationError indicates invalid input, optionally with field-level details.
// The optional cause is preserved through [errors.Unwrap].
type ValidationError struct {
	Message string
	Fields  []FieldError
	cause   error
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

func (e *ValidationError) Unwrap() error    { return e.cause }
func (e *ValidationError) ErrorCode() Code  { return CodeValidation }
func (e *ValidationError) Retryable() bool  { return false }

// ConflictError indicates a resource conflict (duplicate, version mismatch).
// Retryable defaults to false: most conflicts (unique-constraint violations,
// state mismatches) will not succeed on retry without caller-side input
// changes. Use [NewConflictRetryable] for optimistic-concurrency cases where
// retrying with fresh state may succeed. The optional cause is preserved
// through [errors.Unwrap].
type ConflictError struct {
	Message string
	// retryable carries the retry hint set by the constructor.
	retryable bool
	cause     error
}

func (e *ConflictError) Error() string   { return e.Message }
func (e *ConflictError) Unwrap() error   { return e.cause }
func (e *ConflictError) ErrorCode() Code { return CodeConflict }
func (e *ConflictError) Retryable() bool { return e.retryable }

// PermanentError indicates a non-retryable failure.
type PermanentError struct {
	Message string
	cause   error
}

func (e *PermanentError) Error() string   { return e.Message }
func (e *PermanentError) Unwrap() error   { return e.cause }
func (e *PermanentError) ErrorCode() Code { return CodePermanent }
func (e *PermanentError) Retryable() bool { return false }

// AuthRequiredError indicates missing or invalid authentication. The
// optional cause is preserved through [errors.Unwrap].
type AuthRequiredError struct {
	Message string
	cause   error
}

func (e *AuthRequiredError) Error() string   { return e.Message }
func (e *AuthRequiredError) Unwrap() error   { return e.cause }
func (e *AuthRequiredError) ErrorCode() Code { return CodeAuthRequired }
func (e *AuthRequiredError) Retryable() bool { return false }

// ForbiddenError indicates the caller is authenticated but lacks permission.
// The optional cause is preserved through [errors.Unwrap].
type ForbiddenError struct {
	Message string
	cause   error
}

func (e *ForbiddenError) Error() string   { return e.Message }
func (e *ForbiddenError) Unwrap() error   { return e.cause }
func (e *ForbiddenError) ErrorCode() Code { return CodeForbidden }
func (e *ForbiddenError) Retryable() bool { return false }

// RateLimitError indicates a rate limit or quota has been exceeded. The
// optional cause is preserved through [errors.Unwrap].
type RateLimitError struct {
	Message    string
	RetryAfter time.Duration
	cause      error
}

func (e *RateLimitError) Error() string   { return e.Message }
func (e *RateLimitError) Unwrap() error   { return e.cause }
func (e *RateLimitError) ErrorCode() Code { return CodeRateLimit }
func (e *RateLimitError) Retryable() bool { return true }

// OperationFailedError indicates a server-side failure that is unlikely to
// resolve on retry. HTTP adapters log its message but return a generic
// "internal error" response body; use validation, conflict, permanent, or
// domain-specific response types for client-correctable failures.
type OperationFailedError struct {
	Message string
	cause   error
}

func (e *OperationFailedError) Error() string   { return e.Message }
func (e *OperationFailedError) Unwrap() error   { return e.cause }
func (e *OperationFailedError) ErrorCode() Code { return CodeOperationFailed }
func (e *OperationFailedError) Retryable() bool { return false }

// StorageFullError indicates a write failed because the backing store is at
// capacity (filesystem ENOSPC, object-store quota, cloud-provider request
// rejected for size). The error is retryable: operators may free space or
// expand quota, and a later retry can succeed without caller-side changes.
//
// Transport adapters map this to HTTP 507 Insufficient Storage.
type StorageFullError struct {
	Message string
	cause   error
}

func (e *StorageFullError) Error() string   { return e.Message }
func (e *StorageFullError) Unwrap() error   { return e.cause }
func (e *StorageFullError) ErrorCode() Code { return CodeStorageFull }
func (e *StorageFullError) Retryable() bool { return true }

// UnavailableError indicates an upstream dependency is unreachable or not ready.
// Use this when a service cannot fulfill a request because a dependency it relies
// on is down, overloaded, or not responding.
//
// When Dependency is set, the error represents an upstream failure.
// When Dependency is empty, the error represents the service itself being unavailable.
type UnavailableError struct {
	Message    string
	Dependency string        // identifies the failed dependency (e.g., "payment-service", "redis")
	RetryAfter time.Duration // suggested retry delay; 0 means no suggestion
	cause      error
}

func (e *UnavailableError) Error() string   { return e.Message }
func (e *UnavailableError) Unwrap() error   { return e.cause }
func (e *UnavailableError) ErrorCode() Code { return CodeUnavailable }
func (e *UnavailableError) Retryable() bool { return true }

// --- Constructors ---

// NewNotFound creates a NotFoundError for the given entity type and identifier.
func NewNotFound(entity string, id any) error {
	return &NotFoundError{
		Entity:   entity,
		EntityID: id,
		Message:  fmt.Sprintf("%s %v not found", entity, id),
	}
}

// NewNotFoundWithCause is [NewNotFound] preserving the underlying cause.
func NewNotFoundWithCause(entity string, id any, cause error) error {
	return &NotFoundError{
		Entity:   entity,
		EntityID: id,
		Message:  fmt.Sprintf("%s %v not found", entity, id),
		cause:    cause,
	}
}

// NewValidation creates a ValidationError with a simple message (no field details).
func NewValidation(msg string) error {
	return &ValidationError{Message: msg}
}

// NewValidationWithCause is [NewValidation] preserving the underlying cause.
func NewValidationWithCause(msg string, cause error) error {
	return &ValidationError{Message: msg, cause: cause}
}

// NewFieldValidation creates a ValidationError with structured field-level errors.
// Panics if called with zero arguments — use [NewValidation] for message-only errors.
func NewFieldValidation(fields ...FieldError) error {
	if len(fields) == 0 {
		panic("apperror: NewFieldValidation requires at least one FieldError; use NewValidation for message-only errors")
	}
	return &ValidationError{Fields: fields}
}

// NewConflict creates a non-retryable ConflictError. Use this for duplicate-key
// violations, immutable-state conflicts, and other failures where a retry
// without input changes is guaranteed to fail again.
func NewConflict(msg string) error {
	return &ConflictError{Message: msg}
}

// NewConflictWithCause is [NewConflict] preserving the underlying cause.
func NewConflictWithCause(msg string, cause error) error {
	return &ConflictError{Message: msg, cause: cause}
}

// NewConflictRetryable creates a retryable ConflictError. Use this for
// optimistic-concurrency conflicts where re-reading state and retrying with
// the fresh version may succeed (e.g. compare-and-swap on a version column).
func NewConflictRetryable(msg string) error {
	return &ConflictError{Message: msg, retryable: true}
}

// NewConflictRetryableWithCause is [NewConflictRetryable] preserving the
// underlying cause.
func NewConflictRetryableWithCause(msg string, cause error) error {
	return &ConflictError{Message: msg, retryable: true, cause: cause}
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

// NewAuthRequiredWithCause is [NewAuthRequired] preserving the underlying cause.
func NewAuthRequiredWithCause(msg string, cause error) error {
	return &AuthRequiredError{Message: msg, cause: cause}
}

// NewForbidden creates a ForbiddenError.
func NewForbidden(msg string) error {
	return &ForbiddenError{Message: msg}
}

// NewForbiddenWithCause is [NewForbidden] preserving the underlying cause.
func NewForbiddenWithCause(msg string, cause error) error {
	return &ForbiddenError{Message: msg, cause: cause}
}

// NewRateLimit creates a RateLimitError without a specific retry-after hint.
// Use this when the rate limit is known but the cooldown window is not.
func NewRateLimit(msg string) error {
	return &RateLimitError{Message: msg}
}

// NewRateLimitWithCause is [NewRateLimit] preserving the underlying cause.
func NewRateLimitWithCause(msg string, cause error) error {
	return &RateLimitError{Message: msg, cause: cause}
}

// NewRateLimitWithRetryAfter creates a RateLimitError that surfaces a
// retry-after hint to the caller. Transports map this to Retry-After-style
// headers; the resilience/retry package honors it via WithDelayOverride.
func NewRateLimitWithRetryAfter(msg string, retryAfter time.Duration) error {
	return &RateLimitError{Message: msg, RetryAfter: retryAfter}
}

// NewRateLimitWithRetryAfterAndCause is [NewRateLimitWithRetryAfter] preserving
// the underlying cause.
func NewRateLimitWithRetryAfterAndCause(msg string, retryAfter time.Duration, cause error) error {
	return &RateLimitError{Message: msg, RetryAfter: retryAfter, cause: cause}
}

// NewOperationFailed creates an OperationFailedError.
func NewOperationFailed(msg string) error {
	return &OperationFailedError{Message: msg}
}

// NewOperationFailedWithCause creates an OperationFailedError that wraps an underlying cause.
func NewOperationFailedWithCause(msg string, cause error) error {
	return &OperationFailedError{Message: msg, cause: cause}
}

// NewStorageFull creates a StorageFullError with a client-safe message.
// Use this when a backend write fails because the underlying medium has
// no remaining capacity (disk full, bucket quota exhausted, partition
// limit reached). The error is retryable.
func NewStorageFull(msg string) error {
	return &StorageFullError{Message: msg}
}

// NewStorageFullWithCause creates a StorageFullError that wraps an
// underlying cause (typically the raw provider/syscall error).
func NewStorageFullWithCause(msg string, cause error) error {
	return &StorageFullError{Message: msg, cause: cause}
}

// NewUnavailable creates an UnavailableError with a client-safe message.
func NewUnavailable(msg string) error {
	return &UnavailableError{Message: msg}
}

// NewUnavailableWithCause creates an UnavailableError that wraps an underlying cause.
func NewUnavailableWithCause(msg string, cause error) error {
	return &UnavailableError{Message: msg, cause: cause}
}

// NewDependencyUnavailable creates an UnavailableError identifying a specific
// upstream dependency that is unreachable. The dependency name is safe to
// include in client responses (it is developer-defined), but the cause may
// contain internal details and must not be exposed.
func NewDependencyUnavailable(dependency, msg string, cause error) error {
	return &UnavailableError{
		Message:    msg,
		Dependency: dependency,
		cause:      cause,
	}
}

// NewUnavailableWithRetryAfter creates an UnavailableError that surfaces a
// retry-after hint to the caller. Use this when the service can estimate
// when it will be ready (config-reload window, scheduled maintenance,
// dependency reconnect timer) — clients (and HTTP middleware) can honor
// the hint via Retry-After / Retry-After-style headers.
func NewUnavailableWithRetryAfter(msg string, retryAfter time.Duration, cause error) error {
	return &UnavailableError{
		Message:    msg,
		RetryAfter: retryAfter,
		cause:      cause,
	}
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

// IsUnavailable reports whether err contains an UnavailableError.
func IsUnavailable(err error) bool {
	var target *UnavailableError
	return errors.As(err, &target)
}

// IsStorageFull reports whether err contains a StorageFullError.
func IsStorageFull(err error) bool {
	var target *StorageFullError
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

// AsUnavailable extracts the *UnavailableError from the error chain.
func AsUnavailable(err error) (*UnavailableError, bool) {
	var target *UnavailableError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
