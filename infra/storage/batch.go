package storage

import (
	"context"
	"errors"
	"fmt"
)

// BatchDeleter is an optional interface for backends that support
// efficient bulk deletion (e.g. S3 DeleteObjects).
// Check capability via [AsBatchDeleter] so decorators with [Unwrapper] support
// are handled consistently:
//
//	if bd, ok := storage.AsBatchDeleter(backend); ok {
//	    errs := bd.DeleteMany(ctx, keys)
//	}
type BatchDeleter interface {
	// DeleteMany removes multiple objects in a single batch request.
	// Returns a map of key→error for keys that failed. Keys that succeeded
	// are not present in the map. Returns nil if all deletions succeeded.
	DeleteMany(ctx context.Context, keys []string) map[string]error
}

// DeleteMany deletes multiple keys. If the backend implements [BatchDeleter],
// the native batch operation is used. Otherwise, keys are deleted sequentially.
// Returns a combined error if any deletion failed.
func DeleteMany(ctx context.Context, s Storage, keys []string) error {
	if s == nil {
		return fmt.Errorf("storage.DeleteMany: backend is required")
	}
	if err := validateBatchKeys(keys); err != nil {
		return fmt.Errorf("storage.DeleteMany: %w", err)
	}

	if bd, ok := AsBatchDeleter(s); ok {
		failures := bd.DeleteMany(ctx, keys)
		if len(failures) > 0 {
			return batchError(failures)
		}
		return nil
	}

	// Sequential fallback.
	var errs []error
	for _, key := range keys {
		if err := s.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("delete object: %w", err))
		}
	}
	return errors.Join(errs...)
}

// CopyMany copies multiple objects. If source and destination are both the
// same [Copier], native copy is used per-key. Otherwise, falls back to Get→Put.
func CopyMany(ctx context.Context, s Storage, pairs []CopyPair) error {
	if s == nil {
		return fmt.Errorf("storage.CopyMany: backend is required")
	}
	if err := validateCopyPairs(pairs); err != nil {
		return fmt.Errorf("storage.CopyMany: %w", err)
	}

	var errs []error
	for _, p := range pairs {
		if err := Copy(ctx, s, p.SrcKey, p.DstKey); err != nil {
			errs = append(errs, fmt.Errorf("copy object: %w", err))
		}
	}
	return errors.Join(errs...)
}

// CopyPair defines a source→destination key mapping for batch copy.
type CopyPair struct {
	SrcKey string
	DstKey string
}

func validateBatchKeys(keys []string) error {
	if len(keys) > MaxBatchKeys {
		return fmt.Errorf("%w: %w", ErrValidation, ErrBatchTooLarge)
	}
	for _, key := range keys {
		if err := ValidateKey(key); err != nil {
			return err
		}
	}
	return nil
}

func validateCopyPairs(pairs []CopyPair) error {
	if len(pairs) > MaxBatchKeys {
		return fmt.Errorf("%w: %w", ErrValidation, ErrBatchTooLarge)
	}
	for _, p := range pairs {
		if err := ValidateKey(p.SrcKey); err != nil {
			return fmt.Errorf("invalid source key: %w", err)
		}
		if err := ValidateKey(p.DstKey); err != nil {
			return fmt.Errorf("invalid destination key: %w", err)
		}
	}
	return nil
}

// batchError converts a map of key→error into a single error.
func batchError(failures map[string]error) error {
	var errs []error
	for _, err := range failures {
		errs = append(errs, fmt.Errorf("delete object: %w", err))
	}
	return errors.Join(errs...)
}
