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
// [DeleteMany] runs through sequential [Storage.Delete], so BeforeDelete/
// AfterDelete fire for every key in a batch.
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

	return composeHookCaps(h, isLister, isCopier, isPresigned, isURLer)
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

// --- Capability composition ---
//
// Go does not support dynamic interface composition, so we enumerate all
// 2^4 = 16 combinations of {Lister, Copier, PresignedStore, PublicURLer}.
// Method bodies live once on the four forwarder types below; combination
// structs only embed those forwarders (review-18).

// hookListFwd / hookCopyFwd / hookPresignFwd / hookURLFwd each add a single
// optional capability by promoting one method set. They deliberately do NOT
// embed *hookedStorage so multi-capability wrappers can embed several
// forwarders without ambiguous Storage method promotion.
type hookListFwd struct{ h *hookedStorage }

func (w hookListFwd) List(ctx context.Context, prefix string, opts ListOptions) iter.Seq2[ObjectInfo, error] {
	return w.h.list(ctx, prefix, opts)
}

type hookCopyFwd struct{ h *hookedStorage }

func (w hookCopyFwd) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.h.copy(ctx, srcKey, dstKey)
}

type hookPresignFwd struct{ h *hookedStorage }

func (w hookPresignFwd) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.h.presignGetURL(ctx, key, ttl)
}

func (w hookPresignFwd) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta ObjectMeta) (string, error) {
	return w.h.presignPutURL(ctx, key, ttl, meta)
}

type hookURLFwd struct{ h *hookedStorage }

func (w hookURLFwd) URL(ctx context.Context, key string) (string, error) {
	return w.h.url(ctx, key)
}

func composeHookCaps(h *hookedStorage, isLister, isCopier, isPresigned, isURLer bool) Storage {
	list := hookListFwd{h}
	copy := hookCopyFwd{h}
	presign := hookPresignFwd{h}
	url := hookURLFwd{h}

	switch {
	case isLister && isCopier && isPresigned && isURLer:
		return &struct {
			*hookedStorage
			hookListFwd
			hookCopyFwd
			hookPresignFwd
			hookURLFwd
		}{h, list, copy, presign, url}
	case isLister && isCopier && isPresigned:
		return &struct {
			*hookedStorage
			hookListFwd
			hookCopyFwd
			hookPresignFwd
		}{h, list, copy, presign}
	case isLister && isCopier && isURLer:
		return &struct {
			*hookedStorage
			hookListFwd
			hookCopyFwd
			hookURLFwd
		}{h, list, copy, url}
	case isLister && isPresigned && isURLer:
		return &struct {
			*hookedStorage
			hookListFwd
			hookPresignFwd
			hookURLFwd
		}{h, list, presign, url}
	case isCopier && isPresigned && isURLer:
		return &struct {
			*hookedStorage
			hookCopyFwd
			hookPresignFwd
			hookURLFwd
		}{h, copy, presign, url}
	case isLister && isCopier:
		return &struct {
			*hookedStorage
			hookListFwd
			hookCopyFwd
		}{h, list, copy}
	case isLister && isPresigned:
		return &struct {
			*hookedStorage
			hookListFwd
			hookPresignFwd
		}{h, list, presign}
	case isLister && isURLer:
		return &struct {
			*hookedStorage
			hookListFwd
			hookURLFwd
		}{h, list, url}
	case isCopier && isPresigned:
		return &struct {
			*hookedStorage
			hookCopyFwd
			hookPresignFwd
		}{h, copy, presign}
	case isCopier && isURLer:
		return &struct {
			*hookedStorage
			hookCopyFwd
			hookURLFwd
		}{h, copy, url}
	case isPresigned && isURLer:
		return &struct {
			*hookedStorage
			hookPresignFwd
			hookURLFwd
		}{h, presign, url}
	case isLister:
		return &struct {
			*hookedStorage
			hookListFwd
		}{h, list}
	case isCopier:
		return &struct {
			*hookedStorage
			hookCopyFwd
		}{h, copy}
	case isPresigned:
		return &struct {
			*hookedStorage
			hookPresignFwd
		}{h, presign}
	case isURLer:
		return &struct {
			*hookedStorage
			hookURLFwd
		}{h, url}
	default:
		return h
	}
}
