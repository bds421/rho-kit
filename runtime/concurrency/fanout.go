package concurrency

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/bds421/rho-kit/core/v2/redact"
	"golang.org/x/sync/errgroup"
)

// ErrNilContext is returned when FanOut or FanOutSettled receives a nil context.
var ErrNilContext = errors.New("concurrency: context must not be nil")

// PanicError indicates a goroutine panicked during execution.
type PanicError struct {
	// Index is the position of the goroutine in the input slice.
	Index int
	// RedactedValue is the sanitized panic marker. The raw recovered
	// panic payload is never exposed because it may contain secrets,
	// request bodies, tokens, or domain objects.
	RedactedValue string
	// Stack is the stack trace captured at the point of the panic.
	Stack string
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("concurrency: goroutine %d panicked: %s", e.Index, e.safeRedactedValue())
}

func (e *PanicError) safeRedactedValue() string {
	if e.RedactedValue != "" {
		return e.RedactedValue
	}
	return redact.PanicValue(nil)
}

// Result holds the outcome of a single function executed by [FanOutSettled].
type Result[T any] struct {
	// Value is the return value on success. Zero value when Err is non-nil.
	Value T
	// Err is non-nil when the function returned an error or panicked.
	Err error
}

// FanOutOption configures [FanOut] and [FanOutSettled].
type FanOutOption func(*config)

type config struct {
	maxGoroutines    int
	maxGoroutinesSet bool
}

// WithMaxGoroutines sets the concurrency limit:
//   - n >= 1: limit to n concurrent goroutines.
//   - n == 0: explicit opt-out — unbounded concurrency (one goroutine per fn).
//   - n < 0: panic at configuration time.
//
// If WithMaxGoroutines is not supplied, [FanOut] and [FanOutSettled] default
// to runtime.GOMAXPROCS(0) * 2 to prevent goroutine exhaustion when fns is
// derived from external input. Pass WithMaxGoroutines(0) only when you know
// fns is bounded and you need every task in flight simultaneously.
func WithMaxGoroutines(n int) FanOutOption {
	if n < 0 {
		panic("concurrency: WithMaxGoroutines requires n >= 0")
	}
	return func(c *config) {
		c.maxGoroutines = n
		c.maxGoroutinesSet = true
	}
}

func buildConfig(opts []FanOutOption) config {
	var cfg config
	for _, opt := range opts {
		if opt == nil {
			panic("concurrency: FanOut option must not be nil")
		}
		opt(&cfg)
	}
	if !cfg.maxGoroutinesSet {
		cfg.maxGoroutines = runtime.GOMAXPROCS(0) * 2
	}
	return cfg
}

// FanOut runs fns concurrently and returns all results in submission order.
// If any function returns an error (or panics), the derived context is
// cancelled, remaining goroutines observe the cancellation, and the first
// error is returned. Only the first error is returned; errors from other
// concurrently-running goroutines are discarded. On success the returned
// slice has the same length and order as fns.
//
// FanOut defaults to runtime.GOMAXPROCS(0)*2 concurrent goroutines. Pass
// [WithMaxGoroutines](n) to override; pass [WithMaxGoroutines](0) for
// unbounded (one goroutine per fn) — only safe when fns is bounded.
//
// Note: when bounded, FanOut delegates limiting to errgroup which may
// briefly block after context cancellation until a running goroutine frees
// a slot. [FanOutSettled] uses a context-aware semaphore that responds
// immediately to cancellation.
//
// A nil or empty fns slice returns an empty (non-nil) slice and no error.
// Individual nil entries in a non-nil slice are silently skipped — the
// corresponding result position holds the zero value of T.
func FanOut[T any](ctx context.Context, fns []func(ctx context.Context) (T, error), opts ...FanOutOption) ([]T, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
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
		if fn == nil {
			// Forgiving policy: a nil entry means "no work for this
			// slot". The result slot keeps the zero value of T.
			continue
		}
		g.Go(func() (retErr error) {
			defer func() {
				if rec := recover(); rec != nil {
					retErr = newPanicError(i, rec)
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
		// On error, partially-written results are discarded. Goroutines that
		// succeeded before the first error did useful work that is lost — this
		// is inherent to fail-fast semantics.
		return nil, err
	}
	return results, nil
}

// FanOutSettled runs all fns concurrently and collects every outcome
// regardless of individual errors. The returned slice has the same length
// and order as fns. Each [Result] contains either a value or an error.
//
// Like [FanOut], FanOutSettled defaults to runtime.GOMAXPROCS(0)*2 concurrent
// goroutines. Override with [WithMaxGoroutines]; pass
// [WithMaxGoroutines](0) for unbounded.
//
// Unlike [FanOut], a failing function does not cancel others — the parent
// ctx is passed through unmodified. Panics are recovered and converted to
// errors in the corresponding Result.
//
// A nil or empty fns slice returns an empty (non-nil) slice.
func FanOutSettled[T any](ctx context.Context, fns []func(ctx context.Context) (T, error), opts ...FanOutOption) []Result[T] {
	if len(fns) == 0 {
		return []Result[T]{}
	}
	if ctx == nil {
		return settledErrorResults[T](len(fns), ErrNilContext)
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
		if fn == nil {
			// Forgiving policy: skip nil entries — the result slot
			// retains the zero value of T and a nil Err.
			continue
		}
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
			// Defers execute LIFO: the last-registered panic recovery runs first,
			// then semaphore release, then wg.Done (registered first, runs last).
			defer wg.Done()
			// If a running goroutine panics, the deferred <-sem releases the slot,
			// allowing the loop to proceed with the next function.
			if sem != nil {
				defer func() { <-sem }()
			}

			defer func() {
				if rec := recover(); rec != nil {
					results[i] = Result[T]{
						Err: newPanicError(i, rec),
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

func settledErrorResults[T any](n int, err error) []Result[T] {
	results := make([]Result[T], n)
	for i := range results {
		results[i].Err = err
	}
	return results
}

func newPanicError(index int, rec any) *PanicError {
	return &PanicError{
		Index:         index,
		RedactedValue: redact.PanicValue(rec),
		Stack:         string(debug.Stack()),
	}
}
