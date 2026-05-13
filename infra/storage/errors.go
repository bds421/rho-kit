package storage

import (
	"errors"
)

// StorageError is a structured error returned by storage backends.
// It classifies errors as transient (retryable) or permanent, allowing
// retry middleware to make intelligent retry decisions.
type StorageError struct {
	// Op is the operation that failed (e.g. "put", "get", "delete").
	Op string

	// Key is the storage key involved, if any.
	Key string

	// Err is the underlying error.
	Err error

	// transient indicates whether the error is likely to succeed on retry.
	transient bool
}

// Error implements the error interface.
func (e *StorageError) Error() string {
	return "storage: operation failed"
}

// Unwrap implements errors.Unwrap for use with errors.Is/As.
func (e *StorageError) Unwrap() error {
	return e.Err
}

// Transient reports whether this error is likely to succeed on retry.
// Examples: network timeouts, rate limits, temporary server errors.
func (e *StorageError) Transient() bool {
	return e.transient
}

// WrapSafe returns an error that renders only message while preserving cause
// for errors.Is/As. The message must be static and safe for public responses,
// logs, and telemetry.
func WrapSafe(message string, cause error) error {
	if cause == nil {
		return errors.New(message)
	}
	return safeCauseError{message: message, cause: cause}
}

// NewTransientError creates a StorageError marked as transient (retryable).
func NewTransientError(op, key string, err error) *StorageError {
	return &StorageError{Op: op, Key: key, Err: err, transient: true}
}

// NewPermanentError creates a StorageError marked as permanent (not retryable).
func NewPermanentError(op, key string, err error) *StorageError {
	return &StorageError{Op: op, Key: key, Err: err, transient: false}
}

type safeCauseError struct {
	message string
	cause   error
}

func (e safeCauseError) Error() string {
	return e.message
}

func (e safeCauseError) Unwrap() error {
	return e.cause
}

// IsTransient reports whether err (or any error in its chain) is a transient
// storage error. Returns false for nil and non-StorageError errors.
func IsTransient(err error) bool {
	var se *StorageError
	if errors.As(err, &se) {
		return se.Transient()
	}
	return false
}

// Common sentinel errors.
var (
	// ErrPermissionDenied is returned when the backend rejects an operation
	// due to insufficient permissions (e.g. S3 403).
	ErrPermissionDenied = errors.New("storage: permission denied")

	// ErrQuotaExceeded is returned when a storage quota or limit is reached.
	ErrQuotaExceeded = errors.New("storage: quota exceeded")

	// ErrBackendUnavailable is returned when the backend is temporarily unreachable.
	// This is always a transient error.
	ErrBackendUnavailable = errors.New("storage: backend unavailable")

	// ErrBackendClosed is returned when a backend operation is invoked after
	// Close. Unlike ErrBackendUnavailable, this is terminal — the backend will
	// not recover, callers must construct a new instance. Backends should
	// return this error rather than silently reconnecting, so use-after-close
	// bugs are detectable in tests and logs.
	ErrBackendClosed = errors.New("storage: backend closed")
)
