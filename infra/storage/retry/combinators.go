package retry

import (
	"context"
	"iter"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// composeRetry returns a [storage.Storage] whose dynamic type implements the
// optional interfaces matching the underlying chain's capabilities. Method
// bodies live once on the four forwarder types; combination structs only
// embed those forwarders (mirrors storage.WithHooks / review-18).
func composeRetry(r *RetryStorage, hasLister, hasCopier, hasPresigned, hasURLer bool) storage.Storage {
	list := retryListFwd{r}
	copy := retryCopyFwd{r}
	presign := retryPresignFwd{r}
	url := retryURLFwd{r}

	switch {
	case hasLister && hasCopier && hasPresigned && hasURLer:
		return &struct {
			*RetryStorage
			retryListFwd
			retryCopyFwd
			retryPresignFwd
			retryURLFwd
		}{r, list, copy, presign, url}
	case hasLister && hasCopier && hasPresigned:
		return &struct {
			*RetryStorage
			retryListFwd
			retryCopyFwd
			retryPresignFwd
		}{r, list, copy, presign}
	case hasLister && hasCopier && hasURLer:
		return &struct {
			*RetryStorage
			retryListFwd
			retryCopyFwd
			retryURLFwd
		}{r, list, copy, url}
	case hasLister && hasPresigned && hasURLer:
		return &struct {
			*RetryStorage
			retryListFwd
			retryPresignFwd
			retryURLFwd
		}{r, list, presign, url}
	case hasCopier && hasPresigned && hasURLer:
		return &struct {
			*RetryStorage
			retryCopyFwd
			retryPresignFwd
			retryURLFwd
		}{r, copy, presign, url}
	case hasLister && hasCopier:
		return &struct {
			*RetryStorage
			retryListFwd
			retryCopyFwd
		}{r, list, copy}
	case hasLister && hasPresigned:
		return &struct {
			*RetryStorage
			retryListFwd
			retryPresignFwd
		}{r, list, presign}
	case hasLister && hasURLer:
		return &struct {
			*RetryStorage
			retryListFwd
			retryURLFwd
		}{r, list, url}
	case hasCopier && hasPresigned:
		return &struct {
			*RetryStorage
			retryCopyFwd
			retryPresignFwd
		}{r, copy, presign}
	case hasCopier && hasURLer:
		return &struct {
			*RetryStorage
			retryCopyFwd
			retryURLFwd
		}{r, copy, url}
	case hasPresigned && hasURLer:
		return &struct {
			*RetryStorage
			retryPresignFwd
			retryURLFwd
		}{r, presign, url}
	case hasLister:
		return &struct {
			*RetryStorage
			retryListFwd
		}{r, list}
	case hasCopier:
		return &struct {
			*RetryStorage
			retryCopyFwd
		}{r, copy}
	case hasPresigned:
		return &struct {
			*RetryStorage
			retryPresignFwd
		}{r, presign}
	case hasURLer:
		return &struct {
			*RetryStorage
			retryURLFwd
		}{r, url}
	default:
		return r
	}
}

type retryListFwd struct{ r *RetryStorage }

func (w retryListFwd) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.r.listImpl(ctx, prefix, opts)
}

type retryCopyFwd struct{ r *RetryStorage }

func (w retryCopyFwd) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.r.copyImpl(ctx, srcKey, dstKey)
}

type retryPresignFwd struct{ r *RetryStorage }

func (w retryPresignFwd) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.r.presignGetImpl(ctx, key, ttl)
}

func (w retryPresignFwd) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.r.presignPutImpl(ctx, key, ttl, meta)
}

type retryURLFwd struct{ r *RetryStorage }

func (w retryURLFwd) URL(ctx context.Context, key string) (string, error) {
	return w.r.urlImpl(ctx, key)
}
