package retry

import (
	"context"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
	kitretry "github.com/bds421/rho-kit/resilience/v2/retry"
)

// ShouldRetryFunc determines whether an error is retryable.
// The default uses [storage.IsTransient].
type ShouldRetryFunc func(err error) bool

// Config controls retry behavior.
type Config struct {
	// MaxAttempts is the total number of attempts (1 means no retry).
	MaxAttempts int

	// BaseDelay is the initial delay before the first retry.
	BaseDelay time.Duration

	// MaxDelay caps the backoff delay.
	MaxDelay time.Duration

	// ShouldRetry decides if an error is retryable.
	// Defaults to storage.IsTransient.
	ShouldRetry ShouldRetryFunc
}

// Option configures retry behavior.
type Option func(*Config)

// WithMaxAttempts sets the total number of attempts. Default is 3.
// Panics if n < 1 — there is no sensible fallback for an attempt count of
// zero, and silently ignoring the value masks misconfiguration.
func WithMaxAttempts(n int) Option {
	if n < 1 {
		panic("storage/retry: WithMaxAttempts max attempts must be >= 1")
	}
	return func(c *Config) { c.MaxAttempts = n }
}

// WithBaseDelay sets the initial retry delay. Default is 100ms.
// Panics if d is zero or negative — a zero delay creates a tight retry loop.
func WithBaseDelay(d time.Duration) Option {
	if d <= 0 {
		panic("storage/retry: WithBaseDelay base delay must be positive")
	}
	return func(c *Config) { c.BaseDelay = d }
}

// WithMaxDelay caps the maximum delay between retries. Default is 5s.
// Panics if d is zero or negative — a zero/negative cap collapses backoff.
func WithMaxDelay(d time.Duration) Option {
	if d <= 0 {
		panic("storage/retry: WithMaxDelay max delay must be positive")
	}
	return func(c *Config) { c.MaxDelay = d }
}

// WithShouldRetry sets a custom retry predicate.
// A nil fn is a no-op that preserves the default predicate.
func WithShouldRetry(fn ShouldRetryFunc) Option {
	return func(c *Config) {
		if fn == nil {
			return
		}
		c.ShouldRetry = fn
	}
}

// RetryStorage wraps a [storage.Storage] with retry logic backed by
// [kitretry.DoWith] from the kit/retry package.
//
// Optional capabilities (Copier, PresignedStore, PublicURLer) are forwarded
// through the retry policy when the underlying backend supports them. Lister
// is forwarded too, but List is NOT retried — restarting iteration after a
// mid-stream failure would re-yield already-emitted items, so List delegates
// straight to the backend. The [New] factory returns a [storage.Storage]
// whose dynamic type
// implements the same subset of optional interfaces as the underlying
// backend; callers should detect support via [storage.AsLister] etc. Direct
// type assertions to *RetryStorage still work for callers that hold the
// concrete value, but they will not see optional methods.
type RetryStorage struct {
	backend storage.Storage
	cfg     Config
}

// Unwrap returns the underlying storage backend.
func (r *RetryStorage) Unwrap() storage.Storage { return r.backend }

// OpaqueStorageDecorator marks RetryStorage as an [storage.OpaqueDecorator]
// so capability discovery via storage.As* cannot bypass the retry policy by
// reaching the underlying backend's optional interfaces directly.
func (r *RetryStorage) OpaqueStorageDecorator() {}

// New wraps backend with retry logic using exponential backoff + jitter.
//
// The returned value's dynamic type forwards the optional interfaces the
// underlying chain exposes (via [storage.AsLister] etc, which honor opaque
// decorators). For example, wrapping a backend that implements
// [storage.Lister] returns a value that also implements Lister. Note that
// List itself is forwarded WITHOUT retry — restarting iteration after a
// mid-stream failure would duplicate already-yielded items — so only the
// other operations (Put, Get, Delete, Exists, Copy, presign, URL) run under
// the retry policy.
//
// Panics if backend is nil. A nil backend would otherwise only surface as a
// confusing nil-pointer panic on the first storage operation.
func New(backend storage.Storage, opts ...Option) storage.Storage {
	if backend == nil {
		panic("storage/retry: New backend must not be nil")
	}
	cfg := Config{
		MaxAttempts: 3,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		ShouldRetry: storage.IsTransient,
	}
	for _, o := range opts {
		if o == nil {
			panic("storage/retry: New option must not be nil")
		}
		o(&cfg)
	}
	if cfg.ShouldRetry == nil {
		panic("storage/retry: New ShouldRetry must not be nil")
	}
	if cfg.MaxAttempts < 1 {
		panic("storage/retry: New MaxAttempts must be >= 1")
	}
	if cfg.BaseDelay <= 0 {
		panic("storage/retry: New BaseDelay must be positive")
	}
	if cfg.MaxDelay <= 0 {
		panic("storage/retry: New MaxDelay must be positive")
	}
	r := &RetryStorage{backend: backend, cfg: cfg}

	// Detect which optional interfaces the underlying chain exposes. The
	// As* helpers honor [storage.OpaqueDecorator], so a deeper opaque
	// decorator (e.g. encryption blocking presigned) correctly hides that
	// capability from the retry layer too.
	_, hasLister := storage.AsLister(backend)
	_, hasCopier := storage.AsCopier(backend)
	_, hasPresigned := storage.AsPresigned(backend)
	_, hasURLer := storage.AsPublicURLer(backend)

	return composeRetry(r, hasLister, hasCopier, hasPresigned, hasURLer)
}

// policy converts the storage Config to a kit/retry Policy.
func (r *RetryStorage) policy() kitretry.Policy {
	return kitretry.Policy{
		MaxRetries: r.cfg.MaxAttempts - 1, // kit counts retries, we count total attempts
		BaseDelay:  r.cfg.BaseDelay,
		MaxDelay:   r.cfg.MaxDelay,
		Factor:     2.0,
		Jitter:     0.25,
		RetryIf:    r.cfg.ShouldRetry,
	}
}

// Put retries on transient errors only if the reader implements [io.Seeker]
// (e.g. *bytes.Reader, *os.File). After a failed attempt, the reader is
// rewound to the position it had when Put was called before retrying, so a
// reader handed to Put mid-stream (e.g. an *os.File after a header was read)
// uploads the same content on every attempt. If the reader is not seekable,
// Put does NOT retry — the first error is returned immediately because
// the reader has been consumed and retrying would upload empty content.
func (r *RetryStorage) Put(ctx context.Context, key string, reader io.Reader, meta storage.ObjectMeta) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	seeker, seekable := reader.(io.Seeker)

	if !seekable {
		return r.backend.Put(ctx, key, reader, storage.CloneObjectMeta(meta))
	}

	// Capture the reader's initial offset so retries rewind to where the
	// caller left it, not to byte 0.
	start, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil {
		// A reader that cannot report its position cannot be safely rewound,
		// so make a single non-retried attempt rather than risk re-uploading
		// from an unknown offset.
		return r.backend.Put(ctx, key, reader, storage.CloneObjectMeta(meta))
	}

	first := true
	return kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		if !first {
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				// Seek errors are not storage-transient, so RetryIf stops retrying.
				return err
			}
		}
		first = false
		attemptMeta := storage.CloneObjectMeta(meta)
		return r.backend.Put(ctx, key, reader, attemptMeta)
	})
}

// Get retries on transient errors. Any ReadCloser from a failed attempt is
// closed before retrying to prevent resource leaks.
func (r *RetryStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	var (
		rc   io.ReadCloser
		meta storage.ObjectMeta
	)
	err := kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		// Close any ReadCloser from a previous failed attempt.
		if rc != nil {
			_ = rc.Close()
			rc = nil
		}
		var getErr error
		rc, meta, getErr = r.backend.Get(ctx, key)
		return getErr
	})
	if err != nil {
		if rc != nil {
			_ = rc.Close()
		}
		return nil, storage.ObjectMeta{}, err
	}
	return rc, meta, nil
}

// Delete retries on transient errors.
func (r *RetryStorage) Delete(ctx context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	return kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		return r.backend.Delete(ctx, key)
	})
}

// Close delegates [storage.Close] to the wrapped backend so a
// retry-wrapped Storage forwards lifecycle calls correctly.
func (r *RetryStorage) Close() error {
	if r == nil || r.backend == nil {
		return nil
	}
	return storage.Close(r.backend)
}

// Exists retries on transient errors.
func (r *RetryStorage) Exists(ctx context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}
	var ok bool
	err := kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		var existsErr error
		ok, existsErr = r.backend.Exists(ctx, key)
		return existsErr
	})
	if err != nil {
		return false, err
	}
	return ok, nil
}

// --- Optional capability forwarding helpers ---
//
// Each helper resolves the capability through the underlying chain (via
// storage.As*, which honors opaque-decorator markers) and applies the retry
// policy to the call. Mid-iteration retries are deliberately not attempted
// for List — restarting iteration would duplicate already-yielded items.

func (r *RetryStorage) listImpl(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return retryErrorSeq(err)
	}
	if err := storage.ValidateListOptions(opts); err != nil {
		return retryErrorSeq(err)
	}
	lister, ok := storage.AsLister(r.backend)
	if !ok {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, fmt.Errorf("storage/retry: underlying backend does not implement storage.Lister"))
		}
	}
	return lister.List(ctx, prefix, opts)
}

func (r *RetryStorage) copyImpl(ctx context.Context, srcKey, dstKey string) error {
	if err := storage.ValidateKey(srcKey); err != nil {
		return err
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return err
	}
	copier, ok := storage.AsCopier(r.backend)
	if !ok {
		return fmt.Errorf("storage/retry: underlying backend does not implement storage.Copier")
	}
	return kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		return copier.Copy(ctx, srcKey, dstKey)
	})
}

func (r *RetryStorage) presignGetImpl(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	ps, ok := storage.AsPresigned(r.backend)
	if !ok {
		return "", fmt.Errorf("storage/retry: underlying backend does not implement storage.PresignedStore")
	}
	var url string
	err := kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		var perr error
		url, perr = ps.PresignGetURL(ctx, key, ttl)
		return perr
	})
	return url, err
}

func (r *RetryStorage) presignPutImpl(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return "", err
	}
	ps, ok := storage.AsPresigned(r.backend)
	if !ok {
		return "", fmt.Errorf("storage/retry: underlying backend does not implement storage.PresignedStore")
	}
	var url string
	err := kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		var perr error
		url, perr = ps.PresignPutURL(ctx, key, ttl, storage.CloneObjectMeta(meta))
		return perr
	})
	return url, err
}

func (r *RetryStorage) urlImpl(ctx context.Context, key string) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	urler, ok := storage.AsPublicURLer(r.backend)
	if !ok {
		return "", fmt.Errorf("storage/retry: underlying backend does not implement storage.PublicURLer")
	}
	var url string
	err := kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		var uerr error
		url, uerr = urler.URL(ctx, key)
		return uerr
	})
	return url, err
}

func retryErrorSeq(err error) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		yield(storage.ObjectInfo{}, err)
	}
}

// Compile-time interface compliance check.
var (
	_ storage.Storage         = (*RetryStorage)(nil)
	_ storage.OpaqueDecorator = (*RetryStorage)(nil)
)
