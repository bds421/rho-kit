package concurrency

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"golang.org/x/sync/errgroup"
)

// PanicError indicates a goroutine panicked during execution.
type PanicError struct {
	// Index is the position of the goroutine in the input slice.
	Index int
	// Value is the value passed to panic().
	Value any
	// Stack is the stack trace captured at the point of the panic.
	Stack string
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("concurrency: goroutine %d panicked: %v", e.Index, e.Value)
}

// Result holds the outcome of a single function executed by [FanOutSettled].
type Result[T any] struct {
	// Value is the return value on success. Zero value when Err is non-nil.
	Value T
	// Err is non-nil when the function returned an error or panicked.
	Err error
}

// Option configures [FanOut] and [FanOutSettled].
type Option func(*config)

type config struct {
	maxGoroutines int
}

// WithMaxGoroutines limits the number of goroutines that execute concurrently.
// Values less than 1 are ignored (no limit).
func WithMaxGoroutines(n int) Option {
	return func(c *config) {
		if n >= 1 {
			c.maxGoroutines = n
		}
	}
}

func buildConfig(opts []Option) config {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// FanOut runs fns concurrently and returns all results in submission order.
// If any function returns an error (or panics), the derived context is
// cancelled, remaining goroutines observe the cancellation, and the first
// error is returned. On success the returned slice has the same length and
// order as fns.
//
// A nil or empty fns slice returns an empty (non-nil) slice and no error.
func FanOut[T any](ctx context.Context, fns []func(ctx context.Context) (T, error), opts ...Option) ([]T, error) {
	if len(fns) == 0 {
		return []T{}, nil
	}

	cfg := buildConfig(opts)

	g, gCtx := errgroup.WithContext(ctx)
	if cfg.maxGoroutines >= 1 {
		g.SetLimit(cfg.maxGoroutines)
	}

	results := make([]T, len(fns))

	for i, fn := range fns {
		g.Go(func() (retErr error) {
			defer func() {
				if rec := recover(); rec != nil {
					retErr = &PanicError{
						Index: i,
						Value: rec,
						Stack: string(debug.Stack()),
					}
				}
			}()

			val, err := fn(gCtx)
			if err != nil {
				return err
			}
			results[i] = val
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

// FanOutSettled runs all fns concurrently and collects every outcome
// regardless of individual errors. The returned slice has the same length
// and order as fns. Each [Result] contains either a value or an error.
//
// Unlike [FanOut], a failing function does not cancel others — the parent
// ctx is passed through unmodified. Panics are recovered and converted to
// errors in the corresponding Result.
//
// A nil or empty fns slice returns an empty (non-nil) slice.
func FanOutSettled[T any](ctx context.Context, fns []func(ctx context.Context) (T, error), opts ...Option) []Result[T] {
	if len(fns) == 0 {
		return []Result[T]{}
	}

	cfg := buildConfig(opts)

	results := make([]Result[T], len(fns))
	var wg sync.WaitGroup

	// Semaphore channel for bounded parallelism.
	var sem chan struct{}
	if cfg.maxGoroutines >= 1 {
		sem = make(chan struct{}, cfg.maxGoroutines)
	}

	for i, fn := range fns {
		wg.Add(1)

		if sem != nil {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = Result[T]{Err: ctx.Err()}
				wg.Done()
				continue
			}
		}

		go func() {
			defer wg.Done()
			if sem != nil {
				defer func() { <-sem }()
			}

			defer func() {
				if rec := recover(); rec != nil {
					results[i] = Result[T]{
						Err: &PanicError{
							Index: i,
							Value: rec,
							Stack: string(debug.Stack()),
						},
					}
				}
			}()

			val, err := fn(ctx)
			if err != nil {
				results[i] = Result[T]{Err: err}
			} else {
				results[i] = Result[T]{Value: val}
			}
		}()
	}

	wg.Wait()
	return results
}
