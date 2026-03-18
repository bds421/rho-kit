package storage

import (
	"errors"
	"fmt"
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
	if e.Key != "" {
		return fmt.Sprintf("storage.%s %q: %v", e.Op, e.Key, e.Err)
	}
	return fmt.Sprintf("storage.%s: %v", e.Op, e.Err)
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

// NewTransientError creates a StorageError marked as transient (retryable).
func NewTransientError(op, key string, err error) *StorageError {
	return &StorageError{Op: op, Key: key, Err: err, transient: true}
}

// NewPermanentError creates a StorageError marked as permanent (not retryable).
func NewPermanentError(op, key string, err error) *StorageError {
	return &StorageError{Op: op, Key: key, Err: err, transient: false}
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
)
