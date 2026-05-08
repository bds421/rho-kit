package retry

import (
	"context"
	"iter"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// composeRetry returns a [storage.Storage] whose dynamic type implements the
// optional interfaces matching the underlying chain's capabilities. Go does
// not support dynamic interface composition, so we enumerate all 2^4 = 16
// combinations of {Lister, Copier, PresignedStore, PublicURLer}. This
// mirrors the pattern in [storage.WithHooks].
func composeRetry(r *RetryStorage, hasLister, hasCopier, hasPresigned, hasURLer bool) storage.Storage {
	switch {
	case hasLister && hasCopier && hasPresigned && hasURLer:
		return &retryListerCopierPresignedURLer{r}
	case hasLister && hasCopier && hasPresigned:
		return &retryListerCopierPresigned{r}
	case hasLister && hasCopier && hasURLer:
		return &retryListerCopierURLer{r}
	case hasLister && hasPresigned && hasURLer:
		return &retryListerPresignedURLer{r}
	case hasCopier && hasPresigned && hasURLer:
		return &retryCopierPresignedURLer{r}
	case hasLister && hasCopier:
		return &retryListerCopier{r}
	case hasLister && hasPresigned:
		return &retryListerPresigned{r}
	case hasLister && hasURLer:
		return &retryListerURLer{r}
	case hasCopier && hasPresigned:
		return &retryCopierPresigned{r}
	case hasCopier && hasURLer:
		return &retryCopierURLer{r}
	case hasPresigned && hasURLer:
		return &retryPresignedURLer{r}
	case hasLister:
		return &retryLister{r}
	case hasCopier:
		return &retryCopier{r}
	case hasPresigned:
		return &retryPresigner{r}
	case hasURLer:
		return &retryURLer{r}
	default:
		return r
	}
}

type retryLister struct{ *RetryStorage }

func (w *retryLister) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

type retryCopier struct{ *RetryStorage }

func (w *retryCopier) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

type retryPresigner struct{ *RetryStorage }

func (w *retryPresigner) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryPresigner) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type retryURLer struct{ *RetryStorage }

func (w *retryURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryListerCopier struct{ *RetryStorage }

func (w *retryListerCopier) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerCopier) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

type retryListerPresigned struct{ *RetryStorage }

func (w *retryListerPresigned) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryListerPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type retryListerURLer struct{ *RetryStorage }

func (w *retryListerURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryCopierPresigned struct{ *RetryStorage }

func (w *retryCopierPresigned) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *retryCopierPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryCopierPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type retryCopierURLer struct{ *RetryStorage }

func (w *retryCopierURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *retryCopierURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryPresignedURLer struct{ *RetryStorage }

func (w *retryPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *retryPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryListerCopierPresigned struct{ *RetryStorage }

func (w *retryListerCopierPresigned) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerCopierPresigned) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *retryListerCopierPresigned) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryListerCopierPresigned) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

type retryListerCopierURLer struct{ *RetryStorage }

func (w *retryListerCopierURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerCopierURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *retryListerCopierURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryListerPresignedURLer struct{ *RetryStorage }

func (w *retryListerPresignedURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryListerPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *retryListerPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryCopierPresignedURLer struct{ *RetryStorage }

func (w *retryCopierPresignedURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *retryCopierPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryCopierPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *retryCopierPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}

type retryListerCopierPresignedURLer struct{ *RetryStorage }

func (w *retryListerCopierPresignedURLer) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.listImpl(ctx, prefix, opts)
}

func (w *retryListerCopierPresignedURLer) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.copyImpl(ctx, srcKey, dstKey)
}

func (w *retryListerCopierPresignedURLer) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.presignGetImpl(ctx, key, ttl)
}

func (w *retryListerCopierPresignedURLer) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.presignPutImpl(ctx, key, ttl, meta)
}

func (w *retryListerCopierPresignedURLer) URL(ctx context.Context, key string) (string, error) {
	return w.urlImpl(ctx, key)
}
