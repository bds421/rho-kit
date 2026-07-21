package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/bds421/rho-kit/core/v2/redact"
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
		return redact.WrapError("storage.DeleteMany", err)
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
			errs = append(errs, redact.WrapError("delete object", err))
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
		return redact.WrapError("storage.CopyMany", err)
	}

	var errs []error
	for _, p := range pairs {
		if err := Copy(ctx, s, p.SrcKey, p.DstKey); err != nil {
			errs = append(errs, redact.WrapError("copy object", err))
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
			return redact.WrapError("invalid source key", err)
		}
		if err := ValidateKey(p.DstKey); err != nil {
			return redact.WrapError("invalid destination key", err)
		}
	}
	return nil
}

// batchError converts a map of key→error into a single error.
// Each failure is annotated with its key (length-redacted) so callers can
// tell which of the batch keys still need cleanup without re-probing
// every key (review-18).
func batchError(failures map[string]error) error {
	var errs []error
	// Sort keys for deterministic Join order in tests/logs.
	keys := make([]string, 0, len(failures))
	for k := range failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		err := failures[k]
		// Include a length stamp of the key (not the raw key) to avoid
		// leaking storage paths into error strings while still
		// distinguishing failures. Callers that need exact keys should
		// use a BatchDeleter backend that returns map[string]error.
		errs = append(errs, fmt.Errorf("delete object key_len=%d: %w", len(k), err))
	}
	return errors.Join(errs...)
}
