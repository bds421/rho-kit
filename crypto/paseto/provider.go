package paseto

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// PublicKeySource returns the current set of trusted Ed25519 public
// keys. The Provider invokes it once at construction (synchronously)
// and again on every refresh tick.
//
// Implementations typically read from a KMS, a config file, or a
// JWKS-like endpoint. Source errors are surfaced via the
// [WithOnRefreshError] callback after the initial load — the Provider
// keeps serving the previous successful key set rather than going
// dark on a transient backend blip.
type PublicKeySource func(ctx context.Context) ([]ed25519.PublicKey, error)

// Provider wraps a [V4Public] verifier and refreshes its trusted-key
// set on a schedule. Use it when keys rotate without a service
// restart — typical for multi-tenant deployments or KMS-managed
// signing identities.
//
// The Provider is safe for concurrent Verify calls. The hot path is
// an atomic load of the current verifier; refreshes swap a new
// verifier in atomically so callers never observe a torn state.
type Provider struct {
	src          PublicKeySource
	interval     time.Duration
	verifyOpts   []Option
	onRefreshErr func(error)

	current atomic.Pointer[V4Public]
	stop    chan struct{}
	done    chan struct{}
}

// ProviderOption configures a [Provider].
type ProviderOption func(*Provider)

// WithVerifyOptions passes Verify-time options through to each
// rebuilt [V4Public]. Typical use: pin issuer/audience, set clock
// skew tolerance.
func WithVerifyOptions(opts ...Option) ProviderOption {
	return func(p *Provider) { p.verifyOpts = opts }
}

// WithOnRefreshError installs a callback for refresh failures (the
// initial load is reported via the constructor's error return, not
// this callback). The Provider keeps serving the previous key set
// when refreshes fail, so the callback is the only signal that
// rotation has stalled — wire it to a metric or alert.
func WithOnRefreshError(fn func(error)) ProviderOption {
	return func(p *Provider) { p.onRefreshErr = fn }
}

// NewProvider performs the initial key load synchronously, then
// starts a background goroutine that refreshes every `interval`. The
// initial load failure surfaces as the constructor's error return
// — no goroutine is started in that case.
//
// `interval` must be positive. Pick a value substantially shorter
// than the keys' overlap window: if old and new keys are valid for
// 30 minutes after rotation, refresh every 5–10 minutes.
func NewProvider(ctx context.Context, src PublicKeySource, interval time.Duration, opts ...ProviderOption) (*Provider, error) {
	if src == nil {
		return nil, errors.New("paseto: PublicKeySource must not be nil")
	}
	if interval <= 0 {
		return nil, errors.New("paseto: refresh interval must be > 0")
	}
	p := &Provider{
		src:      src,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, o := range opts {
		o(p)
	}

	if err := p.refresh(ctx); err != nil {
		return nil, fmt.Errorf("paseto: initial key load: %w", err)
	}

	go p.loop()
	return p, nil
}

// Verify delegates to the currently-loaded [V4Public]. Returns
// [ErrTokenInvalid] (wrapped by the underlying verifier) when no
// active key set is available — should only happen if the caller
// races Verify against Stop.
func (p *Provider) Verify(token string, now time.Time) (*Claims, error) {
	v := p.current.Load()
	if v == nil {
		return nil, fmt.Errorf("%w: provider stopped", ErrTokenInvalid)
	}
	return v.Verify(token, now)
}

// Stop terminates the refresh goroutine. Subsequent Verify calls
// continue to use the last loaded key set until it expires; callers
// that need stricter shutdown semantics should drop the Provider
// reference.
func (p *Provider) Stop() {
	select {
	case <-p.stop:
		// Already stopped.
	default:
		close(p.stop)
		<-p.done
	}
}

func (p *Provider) loop() {
	defer close(p.done)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), p.interval)
			err := p.refresh(ctx)
			cancel()
			if err != nil && p.onRefreshErr != nil {
				p.onRefreshErr(err)
			}
		}
	}
}

func (p *Provider) refresh(ctx context.Context) error {
	keys, err := p.src(ctx)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("paseto: source returned no keys; refusing to swap to empty trust set")
	}
	v, err := NewV4Public(keys, p.verifyOpts...)
	if err != nil {
		return err
	}
	p.current.Store(v)
	return nil
}
