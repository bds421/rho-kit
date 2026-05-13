package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/observability/v2/promutil"
	"golang.org/x/sync/singleflight"
)

// ErrCacheClosed is returned by GetOrCompute when the ComputeCache has been closed.
var ErrCacheClosed = errors.New("cache: ComputeCache is closed")

// ComputeFunc computes a cache value. Returns the value, its TTL, and any error.
type ComputeFunc[T any] func(ctx context.Context) (T, time.Duration, error)

// envelope is the structure stored in the cache backend.
// It wraps the actual value with an expiration timestamp for
// stale-while-revalidate support.
type envelope struct {
	Value     []byte `json:"v"`
	ExpiresAt int64  `json:"e"` // unix nanoseconds
}

// computeConfig holds configuration for a ComputeCache.
type computeConfig struct {
	staleTTL       time.Duration
	refreshTimeout time.Duration
	computeTimeout time.Duration
	metrics        *ComputeMetrics
	name           string
	logger         *slog.Logger
}

// defaultRefreshTimeout is used when no WithRefreshTimeout option is provided.
const defaultRefreshTimeout = 30 * time.Second

// defaultComputeTimeout caps a foreground compute when the caller's
// context has no deadline. Without this cap, a slow ComputeFunc on a
// caller using context.Background blocks every singleflight follower
// indefinitely.
const defaultComputeTimeout = 60 * time.Second

// ComputeOption configures a ComputeCache.
type ComputeOption func(*computeConfig)

// WithStaleTTL sets the stale-while-revalidate window. After the primary TTL
// expires, stale data is served for up to this duration while a background
// refresh runs. Zero means no stale serving (default).
// Negative values panic.
func WithStaleTTL(d time.Duration) ComputeOption {
	if d < 0 {
		panic("cache: WithStaleTTL requires d >= 0")
	}
	return func(cfg *computeConfig) {
		cfg.staleTTL = d
	}
}

// WithComputeMetricsRegisterer attaches pre-built metrics to the ComputeCache.
// Use NewComputeMetrics to create the metrics, or WithComputePrometheusMetrics
// for a one-step option.
func WithComputeMetricsRegisterer(m *ComputeMetrics) ComputeOption {
	if m == nil {
		panic("cache: WithComputeMetricsRegisterer requires non-nil metrics")
	}
	return func(cfg *computeConfig) {
		cfg.metrics = m
	}
}

// WithComputeName sets the Prometheus metric label for this cache instance.
func WithComputeName(name string) ComputeOption {
	if err := promutil.ValidateStaticLabelValue("compute cache name", name); err != nil {
		panic("cache: invalid compute name")
	}
	return func(cfg *computeConfig) {
		cfg.name = name
	}
}

// WithComputeLogger sets the logger used for backend-error notifications.
// Default: slog.Default(). Pass io.Discard-backed slog.Handler to mute.
func WithComputeLogger(l *slog.Logger) ComputeOption {
	if l == nil {
		panic("cache: WithComputeLogger requires a non-nil logger")
	}
	return func(cfg *computeConfig) {
		cfg.logger = l
	}
}

// WithRefreshTimeout sets the timeout for background refresh operations.
// The duration must be positive.
func WithRefreshTimeout(d time.Duration) ComputeOption {
	if d <= 0 {
		panic("cache: WithRefreshTimeout requires a positive duration")
	}
	return func(cfg *computeConfig) {
		cfg.refreshTimeout = d
	}
}

// WithComputeTimeout caps the duration a foreground compute may run
// when the caller's context has no deadline. Default 60 seconds —
// without this cap a slow ComputeFunc invoked from a Background-typed
// ctx blocks every singleflight follower for the lifetime of the call.
//
// The duration must be positive.
func WithComputeTimeout(d time.Duration) ComputeOption {
	if d <= 0 {
		panic("cache: WithComputeTimeout requires a positive duration")
	}
	return func(cfg *computeConfig) {
		cfg.computeTimeout = d
	}
}

// ComputeCache wraps a Cache backend with singleflight deduplication and
// stale-while-revalidate support. Only one goroutine computes a given key
// at a time; concurrent callers wait for the in-flight computation.
type ComputeCache[T any] struct {
	backend Cache
	prefix  string
	codec   Codec[T]
	group   singleflight.Group
	cfg     computeConfig

	bgWg         sync.WaitGroup
	foregroundWg sync.WaitGroup
	cancelBg     context.CancelFunc
	bgCtx        context.Context
	closeOnce    sync.Once
	closed       atomic.Bool
	// bgMu serialises bgWg.Add against bgWg.Wait. Without it, the closed-load
	// → Add(1) sequence in triggerBackgroundRefresh can run concurrently
	// with Close's Wait — sync.WaitGroup explicitly forbids Add called in
	// parallel with Wait and may panic or skip the goroutine entirely.
	bgMu sync.Mutex

	// refreshing tracks per-key in-flight background refreshes
	// (audit FR-049). singleflight deduplicates the *compute* but
	// not the goroutines waiting on its result; without this map a
	// hot stale key could spawn a goroutine on every stale hit.
	refreshing sync.Map // map[string]struct{}

	// inflight tracks which singleflight keys are currently being
	// computed in the foreground so a caller can tell whether it is
	// the leader (first to claim the key) or a follower (joining an
	// existing leader). singleflight does not expose this directly —
	// counting at this layer keeps the metric semantics precise
	// without changing the deduplication contract.
	inflight sync.Map // map[string]struct{}
}

// NewComputeCache creates a ComputeCache that wraps the given backend.
// The prefix is prepended to all keys to avoid collisions.
//
// Returns an error if backend is nil, or if the prefix contains invalid
// characters or is too long.
func NewComputeCache[T any](backend Cache, prefix string, opts ...ComputeOption) (*ComputeCache[T], error) {
	if backend == nil {
		return nil, fmt.Errorf("cache: NewComputeCache requires a non-nil backend")
	}
	if err := ValidateKeyPrefix(prefix); err != nil {
		return nil, err
	}

	cfg := computeConfig{
		name:           "default",
		refreshTimeout: defaultRefreshTimeout,
		computeTimeout: defaultComputeTimeout,
		logger:         slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("cache: NewComputeCache option must not be nil")
		}
		o(&cfg)
	}

	bgCtx, cancelBg := context.WithCancel(context.Background())

	return &ComputeCache[T]{
		backend:  backend,
		prefix:   prefix,
		codec:    JSONCodec[T]{},
		cfg:      cfg,
		bgCtx:    bgCtx,
		cancelBg: cancelBg,
	}, nil
}

// NewComputeCacheWithCodec creates a ComputeCache with a custom Codec.
// If codec is nil, JSONCodec[T] is used.
func NewComputeCacheWithCodec[T any](backend Cache, prefix string, codec Codec[T], opts ...ComputeOption) (*ComputeCache[T], error) {
	cc, err := NewComputeCache[T](backend, prefix, opts...)
	if err != nil {
		return nil, err
	}
	if codec != nil {
		cc.codec = codec
	}
	return cc, nil
}

// fullKey validates the user-provided key and returns the combined prefix+key.
func (cc *ComputeCache[T]) fullKey(key string) (string, error) {
	if cc == nil || cc.backend == nil || cc.codec == nil || cc.cancelBg == nil {
		return "", ErrInvalidCache
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	full := cc.prefix + key
	if len(full) > MaxKeyLen {
		return "", fmt.Errorf("%w: key with prefix exceeds maximum length", ErrKeyTooLong)
	}
	return full, nil
}

// GetOrCompute retrieves a cached value or computes it if missing/expired.
//
// Behavior:
//   - Cache hit (not expired): returns cached value immediately.
//   - Cache hit (expired, within stale window): returns stale value,
//     triggers async background refresh via singleflight.
//   - Cache miss: uses singleflight to deduplicate concurrent calls,
//     computes the value, stores it, and returns.
//
// Errors from fn are propagated to all waiters and are NOT cached.
func (cc *ComputeCache[T]) GetOrCompute(ctx context.Context, key string, fn ComputeFunc[T]) (T, error) {
	var zero T

	full, err := cc.fullKey(key)
	if err != nil {
		return zero, err
	}
	if cc.closed.Load() {
		return zero, ErrCacheClosed
	}
	if fn == nil {
		return zero, ErrInvalidComputeFunc
	}

	// Try the backend first.
	data, getErr := cc.backend.Get(ctx, full)
	if getErr == nil {
		return cc.handleHit(ctx, full, data, fn)
	}
	if !errors.Is(getErr, ErrCacheMiss) {
		// Backend error (e.g., Redis timeout) — fall through to compute
		// but record the error for observability.
		cc.recordError()
	}

	// Cache miss — compute via singleflight.
	cc.recordMiss()
	return cc.computeAndStore(ctx, full, fn)
}

// handleHit processes a cache hit, checking freshness and triggering
// background refresh for stale entries.
func (cc *ComputeCache[T]) handleHit(
	ctx context.Context,
	full string,
	data []byte,
	fn ComputeFunc[T],
) (T, error) {
	val, expiresAt, err := cc.decodeEnvelope(data)
	if err != nil {
		cc.recordError()
		cc.recordMiss()
		return cc.computeAndStore(ctx, full, fn)
	}

	now := time.Now().UnixNano()
	if now < expiresAt {
		// Fresh hit.
		cc.recordHit()
		return val, nil
	}

	// Expired but still within backend TTL (stale window).
	if cc.cfg.staleTTL == 0 {
		// No stale serving configured; treat expired entry as a miss.
		cc.recordMiss()
		return cc.computeAndStore(ctx, full, fn)
	}
	cc.recordStaleServe()
	cc.triggerBackgroundRefresh(full, fn)
	return val, nil
}

// decodeEnvelope unmarshals the envelope and returns the decoded value and expiration.
func (cc *ComputeCache[T]) decodeEnvelope(data []byte) (T, int64, error) {
	var zero T
	var env envelope
	if err := envelopeCodec.Unmarshal(data, &env); err != nil {
		return zero, 0, err
	}
	var val T
	if err := cc.codec.Unmarshal(env.Value, &val); err != nil {
		return zero, 0, err
	}
	return val, env.ExpiresAt, nil
}

// envelopeCodec is used for the outer envelope (always JSON).
var envelopeCodec = JSONCodec[envelope]{}

// computeAndStore runs fn through singleflight, stores the result, and returns.
//
// The shared compute runs under a context that combines the caller's
// deadline with cc.bgCtx — so Close cancels in-flight foreground
// computes (preventing leaked goroutines that keep hitting the backend
// after shutdown) while a single cancelled follower does not abort work
// other waiters still need. Each leader goroutine is tracked in
// foregroundWg so Close can wait for it to drain.
func (cc *ComputeCache[T]) computeAndStore(ctx context.Context, full string, fn ComputeFunc[T]) (T, error) {
	var zero T

	// Hold bgMu across the closed-load and foregroundWg.Add so a
	// concurrent Close cannot race the Add against its own Wait.
	cc.bgMu.Lock()
	if cc.closed.Load() {
		cc.bgMu.Unlock()
		return zero, ErrCacheClosed
	}
	cc.foregroundWg.Add(1)
	cc.bgMu.Unlock()
	defer cc.foregroundWg.Done()

	// computeCtx for the leader: anchored on bgCtx so Close cancels it,
	// with the caller's deadline preserved (or computeTimeout when no
	// deadline was set) so a slow fn cannot block followers forever.
	computeCtx, cancelCompute := computeContext(cc.bgCtx, ctx, cc.cfg.computeTimeout)
	defer cancelCompute()

	// Leader/follower classification for observability: the first caller
	// to claim the key becomes the leader; concurrent callers that see
	// the key already in `inflight` are followers waiting on the leader.
	// singleflight itself does not expose this distinction.
	_, isFollower := cc.inflight.LoadOrStore(full, struct{}{})
	waitStart := time.Now()
	if isFollower {
		cc.recordSingleflightFollower()
	}

	// FR-048 [MED]: use DoChan + select on ctx so a short-deadline
	// follower can exit promptly instead of waiting for the leader's
	// long compute to finish.
	resCh := cc.group.DoChan(full, func() (any, error) {
		// The function only runs in the leader. Track inflight gauge
		// for the duration of the actual compute.
		cc.recordSingleflightInflightInc()
		defer cc.recordSingleflightInflightDec()
		defer cc.inflight.Delete(full)
		val, execErr := cc.executeCompute(computeCtx, full, fn)
		if execErr != nil {
			cc.recordError()
		}
		return val, execErr
	})
	var (
		result any
		err    error
	)
	select {
	case res := <-resCh:
		if isFollower {
			cc.observeSingleflightWait(time.Since(waitStart))
		}
		result, err = res.Val, res.Err
	case <-ctx.Done():
		// Follower aborted — leader continues. Surface the caller's
		// cancel reason so the request boundary maps to a 499/context
		// error rather than a generic "cache compute" error.
		if isFollower {
			cc.observeSingleflightWait(time.Since(waitStart))
		}
		return zero, ctx.Err()
	case <-cc.bgCtx.Done():
		// Cache closed mid-flight. The leader's computeCtx is anchored
		// on bgCtx so it will also unwind; surface ErrCacheClosed to
		// the follower so callers stop retrying.
		if isFollower {
			cc.observeSingleflightWait(time.Since(waitStart))
		}
		return zero, ErrCacheClosed
	}
	if err != nil {
		return zero, err
	}

	// Guard against nil result (e.g., when T is an interface type).
	if result == nil {
		return zero, nil
	}
	val, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("cache compute: unexpected result type %T", result)
	}
	return val, nil
}

// executeCompute calls fn, marshals the result into an envelope, and stores it.
func (cc *ComputeCache[T]) executeCompute(ctx context.Context, full string, fn ComputeFunc[T]) (val T, err error) {
	defer func() {
		if rv := recover(); rv != nil {
			var zero T
			val = zero
			err = fmt.Errorf("cache compute: ComputeFunc panicked: %s", redact.PanicValue(rv))
		}
	}()

	val, ttl, err := fn(ctx)
	if err != nil {
		var zero T
		return zero, err
	}

	// The base Cache interface treats ttl=0 as "no expiration", but ComputeCache
	// layers stale-while-revalidate on top of an ExpiresAt timestamp: with
	// ttl<=0 the envelope's ExpiresAt would equal now() and every subsequent
	// Get would immediately enter the stale window or recompute. Rather than
	// inherit silently-broken behaviour, ComputeFunc must return a positive
	// TTL — callers wanting a non-expiring computed value should set a long
	// concrete TTL (24h, 7d) appropriate to their refresh budget.
	if ttl <= 0 {
		var zero T
		return zero, fmt.Errorf("cache compute: ComputeFunc returned non-positive ttl; ComputeCache requires a positive TTL because it adds stale-while-revalidate semantics on top")
	}

	valBytes, marshalErr := cc.codec.Marshal(val)
	if marshalErr != nil {
		var zero T
		return zero, fmt.Errorf("cache compute marshal: %w", marshalErr)
	}

	env := envelope{
		Value:     valBytes,
		ExpiresAt: time.Now().Add(ttl).UnixNano(),
	}

	envData, marshalErr := envelopeCodec.Marshal(env)
	if marshalErr != nil {
		var zero T
		return zero, fmt.Errorf("cache compute envelope marshal: %w", marshalErr)
	}

	// Backend TTL = primary TTL + stale window.
	backendTTL := ttl + cc.cfg.staleTTL
	if storeErr := cc.backend.Set(ctx, full, envData, backendTTL); storeErr != nil {
		// Store failure is non-fatal — the caller still gets the
		// computed value — but it must be visible: silently swallowing
		// would let a Redis OOM / OOR / network partition stop the
		// cache from persisting and operators would have no signal.
		cc.recordError()
		cc.cfg.logger.Warn("cache compute: backend Set failed; serving computed value uncached",
			redact.String("key", full), redact.Error(storeErr))
		return val, nil
	}

	return val, nil
}

// triggerBackgroundRefresh starts an async refresh using singleflight.DoChan.
//
// FR-049 [LOW]: only one refresh waiter goroutine per key — additional
// stale hits while a refresh is already in flight return immediately
// instead of stacking goroutines. The atomic LoadOrStore in
// `refreshing` provides the same exactly-once semantics the
// singleflight call gives the compute itself, but at the goroutine
// layer.
func (cc *ComputeCache[T]) triggerBackgroundRefresh(full string, fn ComputeFunc[T]) {
	if _, loaded := cc.refreshing.LoadOrStore(full, struct{}{}); loaded {
		return
	}
	// Hold bgMu across the closed-load and bgWg.Add so a concurrent Close
	// (which acquires bgMu before reading closed) cannot race the Add with
	// its own Wait. Once closed is observed true, no further Add is issued —
	// the WaitGroup is then safe for Wait to consume.
	cc.bgMu.Lock()
	if cc.closed.Load() {
		cc.bgMu.Unlock()
		cc.refreshing.Delete(full)
		return
	}
	cc.bgWg.Add(1)
	cc.bgMu.Unlock()

	ch := cc.group.DoChan(full, func() (any, error) {
		// Use a timeout-scoped context derived from the background context
		// created at construction time. This prevents unbounded refresh
		// operations and allows Close() to cancel in-flight refreshes.
		bgCtx, cancel := context.WithTimeout(cc.bgCtx, cc.cfg.refreshTimeout)
		defer cancel()
		val, err := cc.executeCompute(bgCtx, full, fn)
		if err != nil {
			cc.recordError()
		}
		return val, err
	})

	go func() {
		defer cc.bgWg.Done()
		defer cc.refreshing.Delete(full)
		<-ch
	}()
}

// Wait blocks until all background refresh goroutines and in-flight
// foreground singleflight leaders complete. Primarily useful in tests
// to ensure deterministic behavior.
func (cc *ComputeCache[T]) Wait() {
	if cc == nil {
		return
	}
	cc.bgWg.Wait()
	cc.foregroundWg.Wait()
}

// Close cancels all background refresh operations and waits for them to
// finish. After Close returns, no new background refreshes will be started.
// Close is idempotent; calling it multiple times is safe.
// Implements io.Closer.
//
// The bgMu acquisition publishes the closed=true store to any concurrent
// triggerBackgroundRefresh — once Close releases bgMu, every subsequent
// trigger sees closed and skips the Add. Wait then drains the in-flight
// refreshes that observed closed=false before this Store ran.
func (cc *ComputeCache[T]) Close() error {
	if cc == nil || cc.cancelBg == nil {
		return ErrInvalidCache
	}
	cc.closeOnce.Do(func() {
		cc.bgMu.Lock()
		cc.closed.Store(true)
		cc.bgMu.Unlock()
		cc.cancelBg()
	})
	cc.bgWg.Wait()
	cc.foregroundWg.Wait()
	return nil
}

// recordHit increments the hit counter if metrics are configured.
func (cc *ComputeCache[T]) recordHit() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.hits.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordMiss increments the miss counter if metrics are configured.
func (cc *ComputeCache[T]) recordMiss() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.misses.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordStaleServe increments the stale serve counter if metrics are configured.
func (cc *ComputeCache[T]) recordStaleServe() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.staleServes.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordError increments the error counter if metrics are configured.
func (cc *ComputeCache[T]) recordError() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.errors.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordSingleflightInflightInc increments the in-flight leader gauge.
// Called by the singleflight leader (the goroutine actually executing
// the compute function), not by followers.
func (cc *ComputeCache[T]) recordSingleflightInflightInc() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.singleflightInflight.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordSingleflightInflightDec decrements the in-flight leader gauge.
// Paired with recordSingleflightInflightInc via defer in the leader closure.
func (cc *ComputeCache[T]) recordSingleflightInflightDec() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.singleflightInflight.WithLabelValues(cc.cfg.name).Dec()
	}
}

// recordSingleflightFollower counts a caller that joined an in-flight
// singleflight leader rather than starting a new compute. Together with
// the wait histogram, operators can distinguish "thundering herd
// dedup'd" from "compute is slow per call".
func (cc *ComputeCache[T]) recordSingleflightFollower() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.singleflightFollowers.WithLabelValues(cc.cfg.name).Inc()
	}
}

// observeSingleflightWait records how long a follower waited for the
// leader's result. Only meaningful for followers — leaders observe zero
// wait because they execute the compute inline.
func (cc *ComputeCache[T]) observeSingleflightWait(d time.Duration) {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.singleflightWait.WithLabelValues(cc.cfg.name).Observe(d.Seconds())
	}
}

// computeContext builds the context the leader uses to run fn. It is
// anchored on bgCtx (so Close cancels in-flight foreground computes)
// rather than the caller's ctx (so a single follower's cancel cannot
// abort shared work). The caller's deadline is preserved when set; with
// no deadline the configured computeTimeout caps the run.
func computeContext(bgCtx, caller context.Context, computeTimeout time.Duration) (context.Context, context.CancelFunc) {
	if dl, ok := caller.Deadline(); ok {
		return context.WithDeadline(bgCtx, dl)
	}
	if computeTimeout > 0 {
		return context.WithTimeout(bgCtx, computeTimeout)
	}
	return context.WithCancel(bgCtx)
}
