package storage

import (
	"context"
	"fmt"
	"io"
	"iter"
	"time"
)

// Hooks defines optional callbacks for storage operations.
// Each hook is called synchronously — keep hooks fast to avoid delaying operations.
// Mirrors the [messaging.ConsumerHooks] pattern.
type Hooks struct {
	// BeforePut is called before Put. Return a non-nil error to abort the operation.
	BeforePut func(ctx context.Context, key string, meta ObjectMeta) error

	// AfterPut is called after a successful Put.
	AfterPut func(ctx context.Context, key string, meta ObjectMeta)

	// BeforeDelete is called before Delete. Return a non-nil error to abort.
	BeforeDelete func(ctx context.Context, key string) error

	// AfterDelete is called after a successful Delete.
	AfterDelete func(ctx context.Context, key string)

	// AfterGet is called after a successful Get (before the caller reads the body).
	AfterGet func(ctx context.Context, key string, meta ObjectMeta)

	// AfterCopy is called after a successful Copy.
	AfterCopy func(ctx context.Context, srcKey, dstKey string)

	// AfterPresignGet is called after generating a presigned GET URL.
	AfterPresignGet func(ctx context.Context, key string)

	// AfterPresignPut is called after generating a presigned PUT URL.
	AfterPresignPut func(ctx context.Context, key string)
}

// WithHooks wraps a Storage backend with lifecycle hooks.
// The returned Storage delegates all operations to the underlying backend,
// calling the corresponding hook before/after each operation.
//
// The wrapper forwards [Lister], [Copier], [PresignedStore], and
// [PublicURLer] capabilities if the underlying backend implements them. It
// also exposes [BatchDeleter] so [DeleteMany] runs BeforeDelete/AfterDelete
// for batch deletions instead of bypassing the hooks via the backend's native
// batch delete.
//
// Panics if backend is nil — a nil backend would only surface as a confusing
// nil-pointer panic on the first storage operation.
func WithHooks(backend Storage, hooks Hooks) Storage {
	if backend == nil {
		panic("storage: WithHooks requires a non-nil backend")
	}
	h := &hookedStorage{
		backend: backend,
		hooks:   hooks,
	}

	_, isLister := AsLister(backend)
	_, isCopier := AsCopier(backend)
	_, isPresigned := AsPresigned(backend)
	_, isURLer := AsPublicURLer(backend)

	switch {
	case isLister && isCopier && isPresigned && isURLer:
		return &hookedListerCopierPresignedURLer{h}
	case isLister && isCopier && isPresigned:
		return &hookedListerCopierPresigned{h}
	case isLister && isCopier && isURLer:
		return &hookedListerCopierURLer{h}
	case isLister && isPresigned && isURLer:
		return &hookedListerPresignedURLer{h}
	case isCopier && isPresigned && isURLer:
		return &hookedCopierPresignedURLer{h}
	case isLister && isCopier:
		return &hookedListerCopier{h}
	case isLister && isPresigned:
		return &hookedListerPresigned{h}
	case isLister && isURLer:
		return &hookedListerURLer{h}
	case isCopier && isPresigned:
		return &hookedCopierPresigned{h}
	case isCopier && isURLer:
		return &hookedCopierURLer{h}
	case isPresigned && isURLer:
		return &hookedPresignedURLer{h}
	case isLister:
		return &hookedLister{h}
	case isCopier:
		return &hookedCopier{h}
	case isPresigned:
		return &hookedPresigner{h}
	case isURLer:
		return &hookedURLer{h}
	default:
		return h
	}
}

type hookedStorage struct {
	backend Storage
	hooks   Hooks
}

// Unwrap returns the underlying Storage, enabling AsLister/AsCopier/AsPresigned
// to traverse through the hooks wrapper when it's an intermediate layer in a
// decorator chain.
func (h *hookedStorage) Unwrap() Storage { return h.backend }

func (h *hookedStorage) Put(ctx context.Context, key string, r io.Reader, meta ObjectMeta) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	opMeta := CloneObjectMeta(meta)
	if h.hooks.BeforePut != nil {
		if err := h.hooks.BeforePut(ctx, key, CloneObjectMeta(opMeta)); err != nil {
			return err
		}
	}

	if err := h.backend.Put(ctx, key, r, opMeta); err != nil {
		return err
	}

	if h.hooks.AfterPut != nil {
		h.hooks.AfterPut(ctx, key, CloneObjectMeta(opMeta))
	}
	return nil
}

func (h *hookedStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error) {
	if err := ValidateKey(key); err != nil {
		return nil, ObjectMeta{}, err
	}
	rc, meta, err := h.backend.Get(ctx, key)
	if err != nil {
		return nil, meta, err
	}

	if h.hooks.AfterGet != nil {
		h.hooks.AfterGet(ctx, key, CloneObjectMeta(meta))
	}
	return rc, meta, nil
}

func (h *hookedStorage) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if h.hooks.BeforeDelete != nil {
		if err := h.hooks.BeforeDelete(ctx, key); err != nil {
			return err
		}
	}

	if err := h.backend.Delete(ctx, key); err != nil {
		return err
	}

	if h.hooks.AfterDelete != nil {
		h.hooks.AfterDelete(ctx, key)
	}
	return nil
}

// DeleteMany runs BeforeDelete/AfterDelete around a bulk delete so batch
// deletions honour the same hooks as single Delete calls. Without it,
// [DeleteMany] would discover the underlying backend's native BatchDeleter
// through Unwrap and bypass the hooks entirely (only the sequential fallback
// fired them). BeforeDelete still acts as a per-key abort; aborted keys are
// reported as failures and never reach the backend. The method is defined on
// *hookedStorage so every WithHooks wrapper variant exposes BatchDeleter.
func (h *hookedStorage) DeleteMany(ctx context.Context, keys []string) map[string]error {
	var failures map[string]error
	fail := func(key string, err error) {
		if failures == nil {
			failures = make(map[string]error)
		}
		failures[key] = err
	}

	toDelete := make([]string, 0, len(keys))
	for _, key := range keys {
		if err := ValidateKey(key); err != nil {
			fail(key, err)
			continue
		}
		if h.hooks.BeforeDelete != nil {
			if err := h.hooks.BeforeDelete(ctx, key); err != nil {
				fail(key, err)
				continue
			}
		}
		toDelete = append(toDelete, key)
	}

	if bd, ok := AsBatchDeleter(h.backend); ok {
		for key, err := range bd.DeleteMany(ctx, toDelete) {
			fail(key, err)
		}
		for _, key := range toDelete {
			if _, failed := failures[key]; failed {
				continue
			}
			if h.hooks.AfterDelete != nil {
				h.hooks.AfterDelete(ctx, key)
			}
		}
		return failures
	}

	// Sequential fallback: no native batch delete on the backend.
	for _, key := range toDelete {
		if err := h.backend.Delete(ctx, key); err != nil {
			fail(key, err)
			continue
		}
		if h.hooks.AfterDelete != nil {
			h.hooks.AfterDelete(ctx, key)
		}
	}
	return failures
}

// Close delegates [storage.Close] to the wrapped backend so a
// hooks-wrapped Storage forwards lifecycle calls correctly. Uses the
// helper so a backend without Close is a no-op.
func (h *hookedStorage) Close() error {
	if h == nil || h.backend == nil {
		return nil
	}
	return Close(h.backend)
}

func (h *hookedStorage) Exists(ctx context.Context, key string) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	return h.backend.Exists(ctx, key)
}

// Compile-time interface compliance check.
var _ Storage = (*hookedStorage)(nil)

// --- Optional interface forwarding methods ---

func (h *hookedStorage) list(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	if err := ValidatePrefix(prefix); err != nil {
		return errorSeq(err)
	}
	if err := ValidateListOptions(opts); err != nil {
		return errorSeq(err)
	}
	lister, ok := AsLister(h.backend)
	if !ok {
		return func(yield func(ObjectInfo, error) bool) {
			yield(ObjectInfo{}, fmt.Errorf("storage: backend does not implement Lister"))
		}
	}
	return lister.List(ctx, prefix, opts)
}

func (h *hookedStorage) copy(ctx context.Context, srcKey, dstKey string) error {
	if err := ValidateKey(srcKey); err != nil {
		return err
	}
	if err := ValidateKey(dstKey); err != nil {
		return err
	}
	copier, ok := AsCopier(h.backend)
	if !ok {
		return fmt.Errorf("storage: backend does not implement Copier")
	}
	if err := copier.Copy(ctx, srcKey, dstKey); err != nil {
		return err
	}
	if h.hooks.AfterCopy != nil {
		h.hooks.AfterCopy(ctx, srcKey, dstKey)
	}
	return nil
}

func (h *hookedStorage) presignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	ps, ok := AsPresigned(h.backend)
	if !ok {
		return "", fmt.Errorf("storage: backend does not implement PresignedStore")
	}
	url, err := ps.PresignGetURL(ctx, key, ttl)
	if err != nil {
		return "", err
	}
	if h.hooks.AfterPresignGet != nil {
		h.hooks.AfterPresignGet(ctx, key)
	}
	return url, nil
}

func (h *hookedStorage) presignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if err := ValidateObjectMeta(meta); err != nil {
		return "", err
	}
	ps, ok := AsPresigned(h.backend)
	if !ok {
		return "", fmt.Errorf("storage: backend does not implement PresignedStore")
	}
	url, err := ps.PresignPutURL(ctx, key, ttl, CloneObjectMeta(meta))
	if err != nil {
		return "", err
	}
	if h.hooks.AfterPresignPut != nil {
		h.hooks.AfterPresignPut(ctx, key)
	}
	return url, nil
}

func (h *hookedStorage) url(ctx context.Context, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	urler, ok := AsPublicURLer(h.backend)
	if !ok {
		return "", fmt.Errorf("storage: backend does not implement PublicURLer")
	}
	return urler.URL(ctx, key)
}

func errorSeq(err error) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {
		yield(ObjectInfo{}, err)
	}
}

// --- Combination wrapper types ---
//
// Go does not support dynamic interface composition, so we enumerate all
// 2^4 = 16 combinations of {Lister, Copier, PresignedStore, PublicURLer}.
// This is verbose but type-safe: callers can assert on e.g. Lister and
// get the correct answer without runtime reflection.
//
// If Go ever adds intersection types or structural typing, this can be
// replaced with a single generic wrapper.

type hookedLister struct{ *hookedStorage }

func (w *hookedLister) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

type hookedCopier struct{ *hookedStorage }

func (w *hookedCopier) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

type hookedPresigner struct{ *hookedStorage }

func (w *hookedPresigner) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedPresigner) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

type hookedURLer struct{ *hookedStorage }

func (w *hookedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedListerCopier struct{ *hookedStorage }

func (w *hookedListerCopier) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerCopier) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

type hookedListerPresigned struct{ *hookedStorage }

func (w *hookedListerPresigned) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedListerPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

type hookedListerURLer struct{ *hookedStorage }

func (w *hookedListerURLer) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedCopierPresigned struct{ *hookedStorage }

func (w *hookedCopierPresigned) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

func (w *hookedCopierPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedCopierPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

type hookedCopierURLer struct{ *hookedStorage }

func (w *hookedCopierURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

func (w *hookedCopierURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedPresignedURLer struct{ *hookedStorage }

func (w *hookedPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

func (w *hookedPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedListerCopierPresigned struct{ *hookedStorage }

func (w *hookedListerCopierPresigned) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerCopierPresigned) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

func (w *hookedListerCopierPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedListerCopierPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

type hookedListerCopierURLer struct{ *hookedStorage }

func (w *hookedListerCopierURLer) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerCopierURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

func (w *hookedListerCopierURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedListerPresignedURLer struct{ *hookedStorage }

func (w *hookedListerPresignedURLer) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedListerPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

func (w *hookedListerPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedCopierPresignedURLer struct{ *hookedStorage }

func (w *hookedCopierPresignedURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

func (w *hookedCopierPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedCopierPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

func (w *hookedCopierPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}

type hookedListerCopierPresignedURLer struct{ *hookedStorage }

func (w *hookedListerCopierPresignedURLer) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

func (w *hookedListerCopierPresignedURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copy(ctx, srcKey, dstKey)
}

func (w *hookedListerCopierPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetURL(ctx, key, ttl)
}

func (w *hookedListerCopierPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.presignPutURL(ctx, key, ttl, meta)
}

func (w *hookedListerCopierPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.url(ctx, key)
}
