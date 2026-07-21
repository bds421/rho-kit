package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// DeleteMany deletes multiple keys sequentially via [Storage.Delete].
//
// A dedicated BatchDeleter optional interface was removed in v3 (review-18):
// no production backend implemented it, and the discovery path silently
// stripped the capability through opaque decorators while bypassing
// BeforeDelete/AfterDelete hooks on [WithHooks] wrappers. Sequential
// Delete goes through every decorator's Delete path (including hooks).
// Returns a combined error if any deletion failed.
func DeleteMany(ctx context.Context, s Storage, keys []string) error {
	if s == nil {
		return fmt.Errorf("storage.DeleteMany: backend is required")
	}
	if err := validateBatchKeys(keys); err != nil {
		return redact.WrapError("storage.DeleteMany", err)
	}

	var errs []error
	for _, key := range keys {
		if err := s.Delete(ctx, key); err != nil {
			// Length stamp only — never embed the raw key (review-18).
			errs = append(errs, fmt.Errorf("delete object key_len=%d: %w", len(key), err))
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
