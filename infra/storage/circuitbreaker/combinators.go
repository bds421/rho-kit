package circuitbreaker

import (
	"context"
	"iter"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// composeBreaker returns a [storage.Storage] whose dynamic type implements
// the optional interfaces matching the underlying chain's capabilities.
// Method bodies live once on the four forwarder types; combination structs
// only embed those forwarders (mirrors storage.WithHooks / review-18).
func composeBreaker(cb *CircuitBreaker, hasLister, hasCopier, hasPresigned, hasURLer bool) Stater {
	list := cbListFwd{cb}
	copy := cbCopyFwd{cb}
	presign := cbPresignFwd{cb}
	url := cbURLFwd{cb}

	switch {
	case hasLister && hasCopier && hasPresigned && hasURLer:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbCopyFwd
			cbPresignFwd
			cbURLFwd
		}{cb, list, copy, presign, url}
	case hasLister && hasCopier && hasPresigned:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbCopyFwd
			cbPresignFwd
		}{cb, list, copy, presign}
	case hasLister && hasCopier && hasURLer:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbCopyFwd
			cbURLFwd
		}{cb, list, copy, url}
	case hasLister && hasPresigned && hasURLer:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbPresignFwd
			cbURLFwd
		}{cb, list, presign, url}
	case hasCopier && hasPresigned && hasURLer:
		return &struct {
			*CircuitBreaker
			cbCopyFwd
			cbPresignFwd
			cbURLFwd
		}{cb, copy, presign, url}
	case hasLister && hasCopier:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbCopyFwd
		}{cb, list, copy}
	case hasLister && hasPresigned:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbPresignFwd
		}{cb, list, presign}
	case hasLister && hasURLer:
		return &struct {
			*CircuitBreaker
			cbListFwd
			cbURLFwd
		}{cb, list, url}
	case hasCopier && hasPresigned:
		return &struct {
			*CircuitBreaker
			cbCopyFwd
			cbPresignFwd
		}{cb, copy, presign}
	case hasCopier && hasURLer:
		return &struct {
			*CircuitBreaker
			cbCopyFwd
			cbURLFwd
		}{cb, copy, url}
	case hasPresigned && hasURLer:
		return &struct {
			*CircuitBreaker
			cbPresignFwd
			cbURLFwd
		}{cb, presign, url}
	case hasLister:
		return &struct {
			*CircuitBreaker
			cbListFwd
		}{cb, list}
	case hasCopier:
		return &struct {
			*CircuitBreaker
			cbCopyFwd
		}{cb, copy}
	case hasPresigned:
		return &struct {
			*CircuitBreaker
			cbPresignFwd
		}{cb, presign}
	case hasURLer:
		return &struct {
			*CircuitBreaker
			cbURLFwd
		}{cb, url}
	default:
		return cb
	}
}

type cbListFwd struct{ cb *CircuitBreaker }

func (w cbListFwd) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.cb.listImpl(ctx, prefix, opts)
}

type cbCopyFwd struct{ cb *CircuitBreaker }

func (w cbCopyFwd) Copy(ctx context.Context, srcKey, dstKey string) error {
	return w.cb.copyImpl(ctx, srcKey, dstKey)
}

type cbPresignFwd struct{ cb *CircuitBreaker }

func (w cbPresignFwd) PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return w.cb.presignGetImpl(ctx, key, ttl)
}

func (w cbPresignFwd) PresignPutURL(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	return w.cb.presignPutImpl(ctx, key, ttl, meta)
}

type cbURLFwd struct{ cb *CircuitBreaker }

func (w cbURLFwd) URL(ctx context.Context, key string) (string, error) {
	return w.cb.urlImpl(ctx, key)
}
