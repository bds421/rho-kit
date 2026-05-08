package circuitbreaker

import (
	"context"
	"iter"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// composeBreaker returns a [storage.Storage] whose dynamic type implements
// the optional interfaces matching the underlying chain's capabilities. Go
// does not support dynamic interface composition, so we enumerate all
// 2^4 = 16 combinations of {Lister, Copier, PresignedStore, PublicURLer}.
// This mirrors the pattern in [storage.WithHooks].
func composeBreaker(cb *CircuitBreaker, hasLister, hasCopier, hasPresigned, hasURLer bool) Stater {
	switch {
	case hasLister && hasCopier && hasPresigned && hasURLer:
		return &cbListerCopierPresignedURLer{cb}
	case hasLister && hasCopier && hasPresigned:
		return &cbListerCopierPresigned{cb}
	case hasLister && hasCopier && hasURLer:
		return &cbListerCopierURLer{cb}
	case hasLister && hasPresigned && hasURLer:
		return &cbListerPresignedURLer{cb}
	case hasCopier && hasPresigned && hasURLer:
		return &cbCopierPresignedURLer{cb}
	case hasLister && hasCopier:
		return &cbListerCopier{cb}
	case hasLister && hasPresigned:
		return &cbListerPresigned{cb}
	case hasLister && hasURLer:
		return &cbListerURLer{cb}
	case hasCopier && hasPresigned:
		return &cbCopierPresigned{cb}
	case hasCopier && hasURLer:
		return &cbCopierURLer{cb}
	case hasPresigned && hasURLer:
		return &cbPresignedURLer{cb}
	case hasLister:
		return &cbLister{cb}
	case hasCopier:
		return &cbCopier{cb}
	case hasPresigned:
		return &cbPresigner{cb}
	case hasURLer:
		return &cbURLer{cb}
	default:
		return cb
	}
}

type cbLister struct{ *CircuitBreaker }

func (w *cbLister) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

type cbCopier struct{ *CircuitBreaker }

func (w *cbCopier) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

type cbPresigner struct{ *CircuitBreaker }

func (w *cbPresigner) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbPresigner) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type cbURLer struct{ *CircuitBreaker }

func (w *cbURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbListerCopier struct{ *CircuitBreaker }

func (w *cbListerCopier) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerCopier) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

type cbListerPresigned struct{ *CircuitBreaker }

func (w *cbListerPresigned) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbListerPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type cbListerURLer struct{ *CircuitBreaker }

func (w *cbListerURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbCopierPresigned struct{ *CircuitBreaker }

func (w *cbCopierPresigned) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *cbCopierPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbCopierPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type cbCopierURLer struct{ *CircuitBreaker }

func (w *cbCopierURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *cbCopierURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbPresignedURLer struct{ *CircuitBreaker }

func (w *cbPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *cbPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbListerCopierPresigned struct{ *CircuitBreaker }

func (w *cbListerCopierPresigned) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerCopierPresigned) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *cbListerCopierPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbListerCopierPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type cbListerCopierURLer struct{ *CircuitBreaker }

func (w *cbListerCopierURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerCopierURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *cbListerCopierURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbListerPresignedURLer struct{ *CircuitBreaker }

func (w *cbListerPresignedURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbListerPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *cbListerPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbCopierPresignedURLer struct{ *CircuitBreaker }

func (w *cbCopierPresignedURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *cbCopierPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbCopierPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *cbCopierPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type cbListerCopierPresignedURLer struct{ *CircuitBreaker }

func (w *cbListerCopierPresignedURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *cbListerCopierPresignedURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *cbListerCopierPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *cbListerCopierPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *cbListerCopierPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}
