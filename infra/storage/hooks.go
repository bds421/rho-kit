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
// [PublicURLer] capabilities if the underlying backend implements them.
func WithHooks(backend Storage, hooks Hooks) Storage {
	h := &hookedStorage{
		backend: backend,
		hooks:   hooks,
	}

	// Check which optional interfaces the backend implements and return
	// a combined type that preserves all of them through the wrapper.
	_, isLister := backend.(Lister)
	_, isCopier := backend.(Copier)
	_, isPresigned := backend.(PresignedStore)
	_, isURLer := backend.(PublicURLer)

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
	if h.hooks.BeforePut != nil {
		if err := h.hooks.BeforePut(ctx, key, meta); err != nil {
			return err
		}
	}

	if err := h.backend.Put(ctx, key, r, meta); err != nil {
		return err
	}

	if h.hooks.AfterPut != nil {
		h.hooks.AfterPut(ctx, key, meta)
	}
	return nil
}

func (h *hookedStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectMeta, error) {
	rc, meta, err := h.backend.Get(ctx, key)
	if err != nil {
		return nil, meta, err
	}

	if h.hooks.AfterGet != nil {
		h.hooks.AfterGet(ctx, key, meta)
	}
	return rc, meta, nil
}

func (h *hookedStorage) Delete(ctx context.Context, key string) error {
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

func (h *hookedStorage) Exists(ctx context.Context, key string) (bool, error) {
	return h.backend.Exists(ctx, key)
}

// Compile-time interface compliance check.
var _ Storage = (*hookedStorage)(nil)

// --- Optional interface forwarding methods ---

func (h *hookedStorage) list(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	lister, ok := h.backend.(Lister)
	if !ok {
		// Should never happen — WithHooks checks capabilities at construction.
		return func(yield func(ObjectInfo, error) bool) {
			yield(ObjectInfo{}, fmt.Errorf("storage: backend does not implement Lister"))
		}
	}
	return lister.List(ctx, prefix, opts)
}

func (h *hookedStorage) copy(ctx context.Context, srcKey, dstKey string) error {
	copier, ok := h.backend.(Copier)
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
	ps, ok := h.backend.(PresignedStore)
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
	ps, ok := h.backend.(PresignedStore)
	if !ok {
		return "", fmt.Errorf("storage: backend does not implement PresignedStore")
	}
	url, err := ps.PresignPutURL(ctx, key, ttl, meta)
	if err != nil {
		return "", err
	}
	if h.hooks.AfterPresignPut != nil {
		h.hooks.AfterPresignPut(ctx, key)
	}
	return url, nil
}

func (h *hookedStorage) url(ctx context.Context, key string) (string, error) {
	urler, ok := h.backend.(PublicURLer)
	if !ok {
		return "", fmt.Errorf("storage: backend does not implement PublicURLer")
	}
	return urler.URL(ctx, key)
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
