package storage

import (
	"context"
	"fmt"
	"io"
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
	// It receives the key, whether it was copied or skipped, and any error.
	OnProgress func(key string, copied bool, err error)

	// DryRun simulates the migration without actually copying objects.
	// OnProgress will still be called with what would happen.
	DryRun bool

	// KeyTransform optionally transforms keys during migration.
	// If nil, keys are copied as-is.
	KeyTransform func(srcKey string) string
}

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
	lister, ok := src.(Lister)
	if !ok {
		return MigrateResult{}, fmt.Errorf("storage.Migrate: source backend does not implement Lister")
	}

	if opts.Prefix != "" {
		if err := ValidatePrefix(opts.Prefix); err != nil {
			return MigrateResult{}, fmt.Errorf("storage.Migrate: %w", err)
		}
	}

	var result MigrateResult
	result.Errors = make(map[string]error)

	for info, err := range lister.List(ctx, opts.Prefix, ListOptions{}) {
		if err != nil {
			return result, fmt.Errorf("storage.Migrate: list: %w", err)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		dstKey := info.Key
		if opts.KeyTransform != nil {
			dstKey = opts.KeyTransform(info.Key)
		}
		// Validate the transformed key early to avoid wasting a Get on the
		// source object when the destination would reject the key anyway.
		if err := ValidateKey(dstKey); err != nil {
			result.Failed++
			result.Errors[info.Key] = fmt.Errorf("invalid transformed key %q: %w", dstKey, err)
			if opts.OnProgress != nil {
				opts.OnProgress(info.Key, false, result.Errors[info.Key])
			}
			continue
		}

		if !opts.Overwrite {
			exists, existErr := dst.Exists(ctx, dstKey)
			if existErr != nil {
				result.Failed++
				result.Errors[info.Key] = existErr
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
			result.Skipped++
			if opts.OnProgress != nil {
				opts.OnProgress(info.Key, false, nil)
			}
			continue
		}

		if copyErr := copyObject(ctx, src, info.Key, dst, dstKey); copyErr != nil {
			result.Failed++
			result.Errors[info.Key] = copyErr
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

	return result, nil
}

// MigrateCount counts the number of objects matching the prefix in src.
// Useful for showing a progress bar before starting Migrate.
func MigrateCount(ctx context.Context, src Storage, prefix string) (int64, error) {
	lister, ok := src.(Lister)
	if !ok {
		return 0, fmt.Errorf("storage.MigrateCount: source backend does not implement Lister")
	}

	var count int64
	for _, err := range lister.List(ctx, prefix, ListOptions{}) {
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// copyObject performs a single object transfer from src to dst.
func copyObject(ctx context.Context, src Storage, srcKey string, dst Storage, dstKey string) error {
	rc, meta, err := src.Get(ctx, srcKey)
	if err != nil {
		return fmt.Errorf("get %q: %w", srcKey, err)
	}
	defer func() { _ = rc.Close() }()

	// Deep-copy Custom map to prevent destination backend mutations
	// from corrupting the source metadata.
	var customCopy map[string]string
	if meta.Custom != nil {
		customCopy = make(map[string]string, len(meta.Custom))
		for k, v := range meta.Custom {
			customCopy[k] = v
		}
	}
	putMeta := ObjectMeta{
		ContentType: meta.ContentType,
		Size:        meta.Size,
		Custom:      customCopy,
	}

	if err := dst.Put(ctx, dstKey, io.Reader(rc), putMeta); err != nil {
		return fmt.Errorf("put %q: %w", dstKey, err)
	}
	return nil
}
