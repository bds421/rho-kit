package storage

import (
	"context"
	"fmt"
	"io"
)

// Copier is an optional extension for backends that support native
// server-side copy (e.g. S3 CopyObject, local filesystem copy).
// Check capability via [AsCopier] so decorators with [Unwrapper] support are
// handled consistently:
//
//	if c, ok := storage.AsCopier(backend); ok {
//	    err := c.Copy(ctx, "src.txt", "dst.txt")
//	}
type Copier interface {
	// Copy duplicates an object within the same backend.
	// The destination key is overwritten if it already exists.
	Copy(ctx context.Context, srcKey, dstKey string) error
}

// Copy duplicates an object within the same backend.
// If the backend implements [Copier], the native copy is used.
// Otherwise, it falls back to Get → Put.
func Copy(ctx context.Context, s Storage, srcKey, dstKey string) error {
	if s == nil {
		return fmt.Errorf("storage.Copy: backend is required")
	}
	if err := ValidateKey(srcKey); err != nil {
		return fmt.Errorf("storage.Copy: invalid source key: %w", err)
	}
	if err := ValidateKey(dstKey); err != nil {
		return fmt.Errorf("storage.Copy: invalid destination key: %w", err)
	}

	if c, ok := AsCopier(s); ok {
		return c.Copy(ctx, srcKey, dstKey)
	}

	return genericCopy(ctx, s, srcKey, s, dstKey)
}

// Move relocates an object within the same backend (Copy + Delete source).
//
// This operation is NOT atomic: if Copy succeeds but Delete fails, the object
// will exist at both srcKey and dstKey. Callers should handle this case if
// exactly-once semantics are required.
func Move(ctx context.Context, s Storage, srcKey, dstKey string) error {
	if err := Copy(ctx, s, srcKey, dstKey); err != nil {
		return fmt.Errorf("storage.Move: %w", err)
	}
	if err := s.Delete(ctx, srcKey); err != nil {
		return fmt.Errorf("storage.Move: delete source: %w", err)
	}
	return nil
}

// CopyAcross transfers an object from one backend to another.
// Always uses Get(src) → Put(dst) since the backends may be different types.
func CopyAcross(ctx context.Context, src Storage, srcKey string, dst Storage, dstKey string) error {
	if src == nil {
		return fmt.Errorf("storage.CopyAcross: source backend is required")
	}
	if dst == nil {
		return fmt.Errorf("storage.CopyAcross: destination backend is required")
	}
	if err := ValidateKey(srcKey); err != nil {
		return fmt.Errorf("storage.CopyAcross: invalid source key: %w", err)
	}
	if err := ValidateKey(dstKey); err != nil {
		return fmt.Errorf("storage.CopyAcross: invalid destination key: %w", err)
	}
	return genericCopy(ctx, src, srcKey, dst, dstKey)
}

// genericCopy performs Get from src → Put to dst, passing through ObjectMeta.
func genericCopy(ctx context.Context, src Storage, srcKey string, dst Storage, dstKey string) error {
	if src == nil {
		return fmt.Errorf("source backend is required")
	}
	if dst == nil {
		return fmt.Errorf("destination backend is required")
	}
	rc, meta, err := src.Get(ctx, srcKey)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// Pass through metadata from the source. Size is preserved so backends
	// can set Content-Length on the destination object.
	//
	// FR-081 [LOW]: deep-copy meta.Custom so destination validators or
	// backends mutating the map cannot corrupt the source's view —
	// the same fix already applied in the encryption + migration copy
	// paths, brought to the generic Copy.
	putMeta := CloneObjectMeta(meta)

	if err := dst.Put(ctx, dstKey, io.Reader(rc), putMeta); err != nil {
		return fmt.Errorf("put destination: %w", err)
	}
	return nil
}
