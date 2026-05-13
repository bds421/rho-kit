package paseto

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
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

// Provider wraps a [V4PublicVerifier] verifier and refreshes its trusted-key
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

	current               atomic.Pointer[V4PublicVerifier]
	lastSuccessfulRefresh atomic.Int64
	stop                  chan struct{}
	done                  chan struct{}
	stopOnce              sync.Once

	// rootCtx is cancelled by Close so an in-flight refresh exits
	// promptly instead of running to completion at p.interval (audit
	// FR-046). fetchTimeout caps each refresh independently of the
	// poll interval.
	rootCtx      context.Context
	rootCancel   context.CancelFunc
	fetchTimeout time.Duration
	maxStale     time.Duration
	clock        func() time.Time
}

// defaultFetchTimeout caps each refresh independently of the
// poll interval (audit FR-046). 10s is large enough for a slow JWKS
// endpoint and small enough that Close returns within seconds even
// when a refresh is in flight.
const defaultFetchTimeout = 10 * time.Second

const defaultMaxStale = time.Hour

// ProviderOption configures a [Provider].
type ProviderOption func(*Provider)

// WithFetchTimeout overrides the per-refresh deadline. Useful when
// the upstream key source is genuinely slow.
func WithFetchTimeout(d time.Duration) ProviderOption {
	if d <= 0 {
		panic("paseto: WithFetchTimeout requires a positive duration")
	}
	return func(p *Provider) {
		p.fetchTimeout = d
	}
}

// WithMaxStale sets how long Verify continues to serve the previous
// successful key set after refresh failures. Once exceeded, Verify fails
// closed with [ErrKeySetUnavailable] instead of trusting stale keys forever.
//
// The duration must be positive. Default: 1 hour.
func WithMaxStale(d time.Duration) ProviderOption {
	if d <= 0 {
		panic("paseto: WithMaxStale requires a positive duration")
	}
	return func(p *Provider) { p.maxStale = d }
}

// WithoutMaxStaleLimit disables stale-key expiry. Use only for callers that
// enforce key-source freshness through an external health gate.
func WithoutMaxStaleLimit() ProviderOption {
	return func(p *Provider) { p.maxStale = 0 }
}

func withProviderClock(fn func() time.Time) ProviderOption {
	return func(p *Provider) { p.clock = fn }
}

// WithVerifyOptions passes Verify-time options through to each
// rebuilt [V4PublicVerifier]. Typical use: pin issuer/audience, set clock
// skew tolerance.
func WithVerifyOptions(opts ...Option) ProviderOption {
	copied := append([]Option(nil), opts...)
	return func(p *Provider) { p.verifyOpts = append([]Option(nil), copied...) }
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
	if ctx == nil {
		return nil, errors.New("paseto: context must not be nil")
	}
	if src == nil {
		return nil, errors.New("paseto: PublicKeySource must not be nil")
	}
	if interval <= 0 {
		return nil, errors.New("paseto: refresh interval must be > 0")
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	p := &Provider{
		src:          src,
		interval:     interval,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		rootCtx:      rootCtx,
		rootCancel:   rootCancel,
		fetchTimeout: defaultFetchTimeout,
		maxStale:     defaultMaxStale,
		clock:        time.Now,
	}
	for _, o := range opts {
		if o == nil {
			return nil, errors.New("paseto: provider option must not be nil")
		}
		o(p)
	}
	if p.clock == nil {
		p.clock = time.Now
	}

	if err := p.refresh(ctx); err != nil {
		return nil, fmt.Errorf("paseto: initial key load: %w", err)
	}

	go p.loop()
	return p, nil
}

// Verify delegates to the currently-loaded [V4PublicVerifier]. Returns
// [ErrTokenInvalid] (wrapped by the underlying verifier) when no
// active key set is available — should only happen if the caller
// races Verify against Close.
func (p *Provider) Verify(token string, now time.Time) (*Claims, error) {
	if p == nil {
		return nil, ErrKeySetUnavailable
	}
	v := p.current.Load()
	if v == nil {
		return nil, ErrKeySetUnavailable
	}
	if p.maxStale > 0 {
		last := p.lastSuccessfulRefresh.Load()
		if last == 0 {
			return nil, ErrKeySetUnavailable
		}
		if p.clock().Sub(time.Unix(0, last)) > p.maxStale {
			return nil, ErrKeySetUnavailable
		}
	}
	return v.Verify(token, now)
}

// Close terminates the refresh goroutine. Subsequent Verify calls
// continue to use the last loaded key set until it expires; callers
// that need stricter shutdown semantics should drop the Provider
// reference. Always returns nil — the signature matches [io.Closer]
// so Provider can be wired into resource-cleanup helpers, but the
// shutdown path itself cannot fail.
func (p *Provider) Close() error {
	if p == nil || p.stop == nil || p.done == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		close(p.stop)
		if p.rootCancel != nil {
			p.rootCancel()
		}
	})
	<-p.done
	return nil
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
			// FR-046 [MED]: derive each refresh from rootCtx (cancelled
			// by Close) so a Close in flight aborts the network call
			// instead of waiting for the per-refresh timeout. The
			// per-refresh timeout uses fetchTimeout — independent of
			// p.interval — so a long polling cadence does not also
			// translate into a long shutdown delay.
			ctx, cancel := context.WithTimeout(p.rootCtx, p.fetchTimeout)
			err := p.refresh(ctx)
			cancel()
			if err != nil {
				p.callOnRefreshError(err)
			}
		}
	}
}

func (p *Provider) callOnRefreshError(err error) {
	if p.onRefreshErr == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("paseto: OnRefreshError callback panicked",
				"panic", redactedPanicValue(rec),
				"stack", string(debug.Stack()),
			)
		}
	}()
	p.onRefreshErr(err)
}

func redactedPanicValue(v any) string {
	if v == nil {
		return "<redacted panic value: <nil>>"
	}
	return fmt.Sprintf("<redacted panic value: %T>", v)
}

func (p *Provider) refresh(ctx context.Context) error {
	keys, err := p.src(ctx)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("paseto: source returned no keys; refusing to swap to empty trust set")
	}
	v, err := NewV4PublicVerifier(keys, p.verifyOpts...)
	if err != nil {
		return err
	}
	p.current.Store(v)
	p.lastSuccessfulRefresh.Store(p.clock().UnixNano())
	return nil
}
