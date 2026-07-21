package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// MigrateOptions configures a migration between backends.
type MigrateOptions struct {
	// Prefix limits migration to objects matching this prefix.
	// Empty means all objects.
	Prefix string

	// Overwrite controls whether existing objects in the destination
	// are overwritten. Default is false (skip existing).
	Overwrite bool

	// OnProgress is called after each object is processed.
	// It receives the key, whether it was (or, under DryRun, would be)
	// copied, and any error. Objects skipped because they already exist in
	// the destination report copied=false.
	OnProgress func(key string, copied bool, err error)

	// DryRun simulates the migration without actually copying objects.
	// OnProgress is still called with what would happen: objects that would
	// be copied report copied=true (and count toward [MigrateResult.Copied]),
	// while objects skipped for already existing report copied=false — so a
	// dry run previews the exact copy set.
	DryRun bool

	// KeyTransform optionally transforms keys during migration.
	// If nil, keys are copied as-is.
	KeyTransform func(srcKey string) string
}

// MaxMigrationErrors caps the number of per-key errors retained in
// [MigrateResult.Errors]. The Failed counter and OnProgress callback still see
// every failure.
const MaxMigrationErrors = 1024

// MigrateResult summarizes a completed migration.
type MigrateResult struct {
	// Copied is the number of objects successfully copied.
	Copied int64

	// Skipped is the number of objects skipped (already exist, dry run, etc.).
	Skipped int64

	// Failed is the number of objects that failed to copy.
	Failed int64

	// Errors contains per-key errors for failed objects.
	Errors map[string]error

	// ErrorsTruncated is true when more than [MaxMigrationErrors] objects
	// failed and only the first errors were retained.
	ErrorsTruncated bool
}

// Migrate copies all objects matching the prefix from src to dst.
// The source backend must implement [Lister]. Objects are streamed
// one at a time to keep memory usage constant.
//
// Note: Migrate does not support checkpoint/resume. If interrupted,
// re-running with Overwrite=false efficiently skips already-copied objects.
// For large migrations, consider running with OnProgress to track the last
// successfully copied key.
func Migrate(ctx context.Context, src, dst Storage, opts MigrateOptions) (MigrateResult, error) {
	if src == nil {
		return MigrateResult{}, fmt.Errorf("storage.Migrate: source backend is required")
	}
	if dst == nil {
		return MigrateResult{}, fmt.Errorf("storage.Migrate: destination backend is required")
	}
	if err := ValidatePrefix(opts.Prefix); err != nil {
		return MigrateResult{}, redact.WrapError("storage.Migrate", err)
	}

	// Use AsLister so decorated backends (encryption,
	// metrics, retry) that wrap a Lister-implementing inner expose
	// the capability via Unwrap. The pre-fix `src.(Lister)` cast
	// failed for all decorators and caused Migrate to refuse
	// otherwise-supported sources.
	lister, ok := AsLister(src)
	if !ok {
		return MigrateResult{}, fmt.Errorf("storage.Migrate: source backend does not implement Lister (even via Unwrap)")
	}

	var result MigrateResult
	result.Errors = make(map[string]error)

	for info, err := range lister.List(ctx, opts.Prefix, ListOptions{}) {
		if err != nil {
			return result, redact.WrapError("storage.Migrate: list", err)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		if err := ValidateKey(info.Key); err != nil {
			keyErr := redact.WrapError("invalid source key", err)
			result.recordError(info.Key, keyErr)
			if opts.OnProgress != nil {
				opts.OnProgress(info.Key, false, keyErr)
			}
			continue
		}

		dstKey := info.Key
		if opts.KeyTransform != nil {
			dstKey = opts.KeyTransform(info.Key)
		}
		// Validate the transformed key early to avoid wasting a Get on the
		// source object when the destination would reject the key anyway.
		if err := ValidateKey(dstKey); err != nil {
			keyErr := redact.WrapError("invalid transformed key", err)
			result.recordError(info.Key, keyErr)
			if opts.OnProgress != nil {
				opts.OnProgress(info.Key, false, keyErr)
			}
			continue
		}

		if !opts.Overwrite {
			exists, existErr := dst.Exists(ctx, dstKey)
			if existErr != nil {
				result.recordError(info.Key, existErr)
				if opts.OnProgress != nil {
					opts.OnProgress(info.Key, false, existErr)
				}
				continue
			}
			if exists {
				result.Skipped++
				if opts.OnProgress != nil {
					opts.OnProgress(info.Key, false, nil)
				}
				continue
			}
		}

		if opts.DryRun {
			// This object passed the existence check, so it WOULD be copied.
			// Report it as a would-copy (copied=true) and count it toward
			// Copied so a dry run previews the real copy set, distinct from
			// objects skipped for already existing (handled above as Skipped
			// with copied=false). The object is not actually transferred.
			result.Copied++
			if opts.OnProgress != nil {
				opts.OnProgress(info.Key, true, nil)
			}
			continue
		}

		if copyErr := copyObject(ctx, src, info.Key, dst, dstKey); copyErr != nil {
			result.recordError(info.Key, copyErr)
			if opts.OnProgress != nil {
				opts.OnProgress(info.Key, false, copyErr)
			}
			continue
		}

		result.Copied++
		if opts.OnProgress != nil {
			opts.OnProgress(info.Key, true, nil)
		}
	}

	// Aggregate per-object failures into the returned error so callers
	// that check `err != nil` cannot silently treat a partial-failure
	// run as success. Wave 69 closed a hostile-review finding that
	// Migrate returned nil when every object failed individually.
	if result.Failed > 0 {
		return result, fmt.Errorf("storage.Migrate: %d object(s) failed", result.Failed)
	}
	return result, nil
}

func (r *MigrateResult) recordError(key string, err error) {
	r.Failed++
	if len(r.Errors) < MaxMigrationErrors {
		r.Errors[key] = err
		return
	}
	r.ErrorsTruncated = true
}

// MigrateCount counts the number of objects matching the prefix in src.
// Useful for showing a progress bar before starting Migrate.
//
// Uses AsLister so decorated backends are supported.
func MigrateCount(ctx context.Context, src Storage, prefix string) (int64, error) {
	if src == nil {
		return 0, fmt.Errorf("storage.MigrateCount: source backend is required")
	}
	if err := ValidatePrefix(prefix); err != nil {
		return 0, redact.WrapError("storage.MigrateCount", err)
	}

	lister, ok := AsLister(src)
	if !ok {
		return 0, fmt.Errorf("storage.MigrateCount: source backend does not implement Lister (even via Unwrap)")
	}

	var count int64
	for info, err := range lister.List(ctx, prefix, ListOptions{}) {
		if err != nil {
			return count, err
		}
		if err := ValidateKey(info.Key); err != nil {
			return count, redact.WrapError("storage.MigrateCount: invalid source key", err)
		}
		count++
	}
	return count, nil
}

// copyObject performs a single object transfer from src to dst.
func copyObject(ctx context.Context, src Storage, srcKey string, dst Storage, dstKey string) error {
	if err := ValidateKey(srcKey); err != nil {
		return redact.WrapError("invalid source key", err)
	}
	if err := ValidateKey(dstKey); err != nil {
		return redact.WrapError("invalid destination key", err)
	}

	rc, meta, err := src.Get(ctx, srcKey)
	if err != nil {
		return redact.WrapError("get source", err)
	}
	defer func() { _ = rc.Close() }()

	putMeta := CloneObjectMeta(meta)

	if err := dst.Put(ctx, dstKey, io.Reader(rc), putMeta); err != nil {
		return redact.WrapError("put destination", err)
	}
	return nil
}
