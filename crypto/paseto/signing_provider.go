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

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// PrivateKeySource returns the current Ed25519 signing key. The
// SigningProvider invokes it once at construction (synchronously) and
// again on every refresh tick. Implementations typically read from a
// KMS, a sealed secret, or a workload-identity-mounted file.
//
// The returned key is wrapped in [*secret.String] so it is opaque to
// logs and stringification. The SigningProvider's refresh path copies
// the key bytes into the new [V4PublicSigner] and zeroes the
// caller-returned slice immediately; the previous signer is released
// to the garbage collector together with its key bytes (in-place
// zeroize on a still-in-use signer would race with concurrent Sign
// goroutines, so we only zero at [SigningProvider.Close]).
//
// Source errors are surfaced via the [WithOnSigningRefreshError]
// callback after the initial load — the SigningProvider keeps signing
// with the previous successful key rather than going dark on a
// transient backend blip. The [WithSigningMaxStale] window bounds how
// long the previous key is trusted after the source last succeeded.
type PrivateKeySource func(ctx context.Context) (*secret.String, error)

// SigningProvider wraps a [V4PublicSigner] and rotates its Ed25519
// private key on a schedule. Use it when issuer keys rotate without
// a service restart — typical for multi-tenant deployments where
// signing identities live in a KMS or an HSM-fronted secret manager.
//
// Sign is safe for concurrent use; the hot path is an atomic load of
// the current signer. Refresh swaps in a new signer atomically — Sign
// callers never observe a torn state — and then closes the previous
// signer so its in-memory key material is zeroed (see
// [V4PublicSigner.Close]). Always pair construction with
// [SigningProvider.Close] in a defer or shutdown hook.
type SigningProvider struct {
	src          PrivateKeySource
	interval     time.Duration
	signerOpts   []Option
	onRefreshErr func(error)

	current               atomic.Pointer[V4PublicSigner]
	lastSuccessfulRefresh atomic.Int64
	closed                atomic.Bool
	stop                  chan struct{}
	done                  chan struct{}
	stopOnce              sync.Once

	rootCtx      context.Context
	rootCancel   context.CancelFunc
	fetchTimeout time.Duration
	maxStale     time.Duration
	clock        func() time.Time
}

// SigningProviderOption configures a [SigningProvider].
type SigningProviderOption func(*SigningProvider)

// WithSigningMaxStale bounds how long Sign continues to use the
// previously-loaded key after refresh failures. Once exceeded, Sign
// fails closed with [ErrKeySetUnavailable] instead of trusting stale
// keys forever — issuing tokens with a key the operator has rotated
// out is a credential-rotation violation.
//
// Default: 1 hour. Use [WithoutSigningMaxStaleLimit] only when an
// external health gate already enforces key-source freshness.
func WithSigningMaxStale(d time.Duration) SigningProviderOption {
	if d <= 0 {
		panic("paseto: WithSigningMaxStale requires a positive duration")
	}
	return func(p *SigningProvider) { p.maxStale = d }
}

// WithoutSigningMaxStaleLimit disables stale-key expiry for the
// signing-side Provider. Use only when callers enforce
// key-source freshness through an external health gate.
func WithoutSigningMaxStaleLimit() SigningProviderOption {
	return func(p *SigningProvider) { p.maxStale = 0 }
}

// WithSigningFetchTimeout overrides the per-refresh deadline. Useful
// when the upstream key source (a KMS or HSM-fronted secret manager)
// is genuinely slow. Mirrors [WithFetchTimeout] on the verifying side.
//
// The duration must be positive. Default: 10 seconds.
func WithSigningFetchTimeout(d time.Duration) SigningProviderOption {
	if d <= 0 {
		panic("paseto: WithSigningFetchTimeout requires a positive duration")
	}
	return func(p *SigningProvider) {
		p.fetchTimeout = d
	}
}

// WithSigningOptions passes signer construction options through to
// every rebuilt [V4PublicSigner]. Typical use: pin issuer/audience or
// configure footer/implicit-assertion handling.
func WithSigningOptions(opts ...Option) SigningProviderOption {
	copied := append([]Option(nil), opts...)
	return func(p *SigningProvider) { p.signerOpts = append([]Option(nil), copied...) }
}

// WithOnSigningRefreshError installs a callback for refresh failures.
// The initial load failure surfaces via [OpenSigningProvider]'s error
// return, not this callback. The SigningProvider keeps signing with
// the previous key when refreshes fail, so the callback is the only
// signal that rotation has stalled — wire it to a metric or alert.
//
// The callback runs on the SigningProvider's refresh goroutine. It
// must not call [SigningProvider.Close]: Close blocks until that
// goroutine returns, so closing from within the callback
// self-deadlocks. To shut down in response to repeated failures,
// signal a separate goroutine instead.
//
// Panics if fn is nil: silently swallowing the callback would hide
// the only operator-visible signal of stalled rotation.
func WithOnSigningRefreshError(fn func(error)) SigningProviderOption {
	if fn == nil {
		panic("paseto: WithOnSigningRefreshError requires a non-nil callback")
	}
	return func(p *SigningProvider) { p.onRefreshErr = fn }
}

func withSigningProviderClock(fn func() time.Time) SigningProviderOption {
	return func(p *SigningProvider) { p.clock = fn }
}

// OpenSigningProvider performs the initial key load synchronously and
// starts a background goroutine that refreshes every `interval`.
// The initial load failure surfaces as the constructor's error return —
// no goroutine is started in that case.
//
// `interval` must be positive. Pick a value substantially shorter than
// the consumer-side verification key's overlap window: if verifiers
// trust both the old and new public key for 30 minutes after rotation,
// refresh signing every 5–10 minutes so an issuer that misses one
// rotation cycle still produces verifier-acceptable tokens.
//
// Naming: this is `Open*` (not `New*`) because the constructor does
// I/O (the initial key fetch) and spawns a long-lived background
// goroutine. Always pair with [SigningProvider.Close].
func OpenSigningProvider(ctx context.Context, src PrivateKeySource, interval time.Duration, opts ...SigningProviderOption) (*SigningProvider, error) {
	if ctx == nil {
		return nil, errors.New("paseto: context must not be nil")
	}
	if src == nil {
		return nil, errors.New("paseto: PrivateKeySource must not be nil")
	}
	if interval <= 0 {
		return nil, errors.New("paseto: refresh interval must be > 0")
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	p := &SigningProvider{
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
			rootCancel()
			panic("paseto: OpenSigningProvider signing provider option must not be nil")
		}
		o(p)
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	// Fail closed at construction when the refresh interval is >= the
	// stale window (mirrors OpenProvider). interval >= maxStale produces a
	// recurring self-inflicted outage of Sign between maxStale and the next
	// refresh tick.
	if p.maxStale > 0 && p.interval > p.maxStale {
		rootCancel()
		return nil, fmt.Errorf("paseto: refresh interval (%s) must be <= maxStale (%s) to avoid periodic fail-closed gaps", p.interval, p.maxStale)
	}

	loadCtx, loadCancel := context.WithTimeout(ctx, p.fetchTimeout)
	err := p.refresh(loadCtx)
	loadCancel()
	if err != nil {
		rootCancel()
		return nil, fmt.Errorf("paseto: initial signing key load: %w", err)
	}

	go p.loop()
	return p, nil
}

// Sign delegates to the currently-loaded [V4PublicSigner]. After
// [SigningProvider.Close], returns [ErrProviderClosed] so callers can
// distinguish a shut-down provider from a transient stale-key
// situation. Returns [ErrKeySetUnavailable] when the key has expired
// its [WithSigningMaxStale] window or never loaded.
func (p *SigningProvider) Sign(claims Claims) (string, error) {
	if p == nil {
		return "", ErrKeySetUnavailable
	}
	if p.closed.Load() {
		return "", ErrProviderClosed
	}
	s := p.current.Load()
	if s == nil {
		return "", ErrKeySetUnavailable
	}
	if p.maxStale > 0 {
		last := p.lastSuccessfulRefresh.Load()
		if last == 0 {
			return "", ErrKeySetUnavailable
		}
		if p.clock().Sub(time.Unix(0, last)) > p.maxStale {
			return "", ErrKeySetUnavailable
		}
	}
	return s.Sign(claims)
}

// Close terminates the refresh goroutine and zeroes the in-memory
// private key of the currently-loaded signer. Subsequent Sign calls
// return [ErrProviderClosed]. Idempotent; safe for concurrent use.
// Always returns nil — the signature matches [io.Closer] so the
// provider can be wired into resource-cleanup helpers, but the
// shutdown path itself cannot fail.
func (p *SigningProvider) Close() error {
	if p == nil || p.stop == nil || p.done == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		p.closed.Store(true)
		close(p.stop)
		if p.rootCancel != nil {
			p.rootCancel()
		}
	})
	<-p.done
	if s := p.current.Swap(nil); s != nil {
		_ = s.Close()
	}
	return nil
}

func (p *SigningProvider) loop() {
	defer close(p.done)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			// Derive each refresh from rootCtx (cancelled by Close)
			// so an in-flight Close aborts the network call instead
			// of waiting for the per-refresh timeout. The per-refresh
			// timeout uses fetchTimeout — independent of p.interval —
			// so a long polling cadence does not translate into a
			// long shutdown delay.
			ctx, cancel := context.WithTimeout(p.rootCtx, p.fetchTimeout)
			err := p.refresh(ctx)
			cancel()
			// Suppress the callback during shutdown: Close cancels
			// rootCtx, so an in-flight refresh fails with
			// context.Canceled. Reporting that as a refresh error would
			// fire a false "rotation stalled" alert on every shutdown
			// that races a tick.
			if err != nil && !p.closed.Load() {
				p.callOnRefreshError(err)
			}
		}
	}
}

func (p *SigningProvider) callOnRefreshError(err error) {
	if p.onRefreshErr == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("paseto: OnSigningRefreshError callback panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
		}
	}()
	p.onRefreshErr(err)
}

func (p *SigningProvider) refresh(ctx context.Context) error {
	keySecret, err := p.src(ctx)
	if err != nil {
		return err
	}
	if keySecret == nil {
		return errors.New("paseto: PrivateKeySource returned a nil secret.String")
	}
	// Reveal returns a defensive copy; also Zero the secret.String so
	// the Source-owned buffer does not retain the Ed25519 private key
	// until GC reclaims it.
	raw := keySecret.Reveal()
	defer keySecret.Zero()
	if len(raw) != ed25519.PrivateKeySize {
		// Zero the rejected key bytes before discarding the reference
		// so a misconfigured Source cannot leave a torn private-key
		// fragment on the goroutine stack.
		for i := range raw {
			raw[i] = 0
		}
		return fmt.Errorf("paseto: PrivateKeySource returned %d bytes; expected %d", len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	newSigner, err := NewV4PublicSigner(priv, p.signerOpts...)
	// NewV4PublicSigner copies the key bytes, so the source-side raw
	// slice can be zeroed regardless of whether construction succeeded.
	for i := range raw {
		raw[i] = 0
	}
	if err != nil {
		return err
	}
	p.current.Store(newSigner)
	p.lastSuccessfulRefresh.Store(p.clock().UnixNano())
	// We intentionally do NOT call Close on the previous signer here:
	// V4PublicSigner.Close zeroes the underlying ed25519 private key
	// in place, which races with any in-flight Sign on the previous
	// signer (ed25519.Sign reads the key while Close is writing zeros
	// to the same bytes). The previous signer goes out of scope at
	// this Store and is reclaimed by GC together with its key bytes.
	// At shutdown the live signer is zeroed via SigningProvider.Close,
	// at which point no Sign callers should remain.
	return nil
}
