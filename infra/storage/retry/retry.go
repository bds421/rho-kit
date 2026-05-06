package retry

import (
	"context"
	"io"
	"time"

	"github.com/bds421/rho-kit/infra/storage"
	kitretry "github.com/bds421/rho-kit/resilience/retry"
)

// Compile-time interface compliance check.
var _ storage.Storage = (*RetryStorage)(nil)

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
func WithMaxAttempts(n int) Option {
	return func(c *Config) {
		if n > 0 {
			c.MaxAttempts = n
		}
	}
}

// WithBaseDelay sets the initial retry delay. Default is 100ms.
// Panics if d is zero or negative — a zero delay creates a tight retry loop.
func WithBaseDelay(d time.Duration) Option {
	if d <= 0 {
		panic("storage/retry: base delay must be positive")
	}
	return func(c *Config) { c.BaseDelay = d }
}

// WithMaxDelay caps the maximum delay between retries. Default is 5s.
func WithMaxDelay(d time.Duration) Option {
	return func(c *Config) { c.MaxDelay = d }
}

// WithShouldRetry sets a custom retry predicate.
func WithShouldRetry(fn ShouldRetryFunc) Option {
	return func(c *Config) { c.ShouldRetry = fn }
}

// RetryStorage wraps a [storage.Storage] with retry logic backed by
// [kitretry.DoWith] from the kit/retry package.
type RetryStorage struct {
	backend storage.Storage
	cfg     Config
}

// Unwrap returns the underlying storage backend.
func (r *RetryStorage) Unwrap() storage.Storage { return r.backend }

// New wraps backend with retry logic using exponential backoff + jitter.
func New(backend storage.Storage, opts ...Option) *RetryStorage {
	cfg := Config{
		MaxAttempts: 3,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		ShouldRetry: storage.IsTransient,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &RetryStorage{backend: backend, cfg: cfg}
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
// rewound to the start before retrying. If the reader is not seekable,
// Put does NOT retry — the first error is returned immediately because
// the reader has been consumed and retrying would upload empty content.
func (r *RetryStorage) Put(ctx context.Context, key string, reader io.Reader, meta storage.ObjectMeta) error {
	seeker, seekable := reader.(io.Seeker)

	if !seekable {
		return r.backend.Put(ctx, key, reader, meta)
	}

	first := true
	return kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		if !first {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				// Seek errors are not storage-transient, so RetryIf stops retrying.
				return err
			}
		}
		first = false
		// Copy meta for each attempt. Value fields (ContentType, Size) are
		// copied by struct assignment. Custom (map) must be deep-copied to
		// prevent validator mutations from persisting across retries.
		attemptMeta := meta
		if len(meta.Custom) > 0 {
			attemptMeta.Custom = make(map[string]string, len(meta.Custom))
			for k, v := range meta.Custom {
				attemptMeta.Custom[k] = v
			}
		}
		return r.backend.Put(ctx, key, reader, attemptMeta)
	})
}

// Get retries on transient errors. Any ReadCloser from a failed attempt is
// closed before retrying to prevent resource leaks.
func (r *RetryStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
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
	return kitretry.DoWith(ctx, r.policy(), func(ctx context.Context) error {
		return r.backend.Delete(ctx, key)
	})
}

// Exists retries on transient errors.
func (r *RetryStorage) Exists(ctx context.Context, key string) (bool, error) {
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

// Note: Optional storage interfaces (Lister, Copier, PresignedStore, PublicURLer)
// are intentionally NOT implemented on RetryStorage. When callers use
// storage.AsLister(rs), the Unwrap chain resolves to the underlying backend,
// bypassing retry logic for List/Copy/Presign/URL operations.
//
// This is a deliberate trade-off to avoid the 2^4 = 16 combinatorial wrapper
// types. The core Storage operations (Put, Get, Delete, Exists) are the most
// likely to encounter transient failures that benefit from retry. If retry
// for List is needed, wrap the List call site with kit/retry.DoWith directly.
