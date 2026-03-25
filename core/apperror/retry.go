package apperror

import "errors"

// ShouldRetry reports whether the error is retryable.
// Returns false for non-apperror errors (fail-safe: don't retry unknown errors).
//
// This function is designed as a predicate for retry middleware:
//
//	retry.Do(ctx, fn, retry.WithRetryIf(apperror.ShouldRetry))
func ShouldRetry(err error) bool {
	var appErr AppError
	if !errors.As(err, &appErr) {
		return false
	}
	return appErr.Retryable()
}
