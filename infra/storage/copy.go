package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/bds421/rho-kit/core/v2/redact"
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
		return redact.WrapError("storage.Copy: invalid source key", err)
	}
	if err := ValidateKey(dstKey); err != nil {
		return redact.WrapError("storage.Copy: invalid destination key", err)
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
		return redact.WrapError("storage.Move", err)
	}
	// Guard against srcKey == dstKey: Copy onto the same key succeeds (e.g.
	// localbackend's temp+rename), so the following Delete would remove the
	// only copy and destroy the object. A Move onto itself is a no-op. Keys
	// are validated by Copy above, so an invalid identical key has already
	// been rejected.
	if srcKey == dstKey {
		return nil
	}
	if err := s.Delete(ctx, srcKey); err != nil {
		return redact.WrapError("storage.Move: delete source", err)
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
		return redact.WrapError("storage.CopyAcross: invalid source key", err)
	}
	if err := ValidateKey(dstKey); err != nil {
		return redact.WrapError("storage.CopyAcross: invalid destination key", err)
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
		return redact.WrapError("get source", err)
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
		return redact.WrapError("put destination", err)
	}
	return nil
}
