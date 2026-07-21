package paseto

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/secret"
)

func newPrivateKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

func privSource(priv ed25519.PrivateKey) PrivateKeySource {
	return func(_ context.Context) (*secret.String, error) {
		return secret.New(priv), nil
	}
}

func TestOpenSigningProvider_RejectsNilSource(t *testing.T) {
	_, err := OpenSigningProvider(context.Background(), nil, time.Second)
	require.Error(t, err)
}

func TestOpenSigningProvider_RejectsNilContext(t *testing.T) {
	_, priv := newPrivateKey(t)
	_, err := OpenSigningProvider(nilContextForTest(), privSource(priv), time.Second)
	require.Error(t, err)
}

func TestOpenSigningProvider_RejectsZeroInterval(t *testing.T) {
	_, priv := newPrivateKey(t)
	_, err := OpenSigningProvider(context.Background(), privSource(priv), 0)
	require.Error(t, err)
}

func TestOpenSigningProvider_RejectsNilOption(t *testing.T) {
	_, priv := newPrivateKey(t)
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	_, _ = OpenSigningProvider(context.Background(), privSource(priv), time.Second, nil)
}

func TestOpenSigningProvider_PropagatesInitialLoadFailure(t *testing.T) {
	boom := errors.New("kms unavailable")
	_, err := OpenSigningProvider(context.Background(),
		func(_ context.Context) (*secret.String, error) { return nil, boom },
		time.Second,
	)
	require.ErrorIs(t, err, boom)
}

func TestOpenSigningProvider_RejectsWrongKeyLength(t *testing.T) {
	_, err := OpenSigningProvider(context.Background(),
		func(_ context.Context) (*secret.String, error) {
			return secret.New([]byte("too short")), nil
		},
		time.Second,
	)
	require.Error(t, err)
}

func TestOpenSigningProvider_RejectsNilSecret(t *testing.T) {
	_, err := OpenSigningProvider(context.Background(),
		func(_ context.Context) (*secret.String, error) { return nil, nil },
		time.Second,
	)
	require.Error(t, err)
}

func TestSigningProvider_SignsAndVerifies(t *testing.T) {
	pub, priv := newPrivateKey(t)
	p, err := OpenSigningProvider(context.Background(), privSource(priv), time.Hour,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	tok, err := p.Sign(Claims{Subject: "alice", Audience: []string{"any"}, ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)

	verifier, err := NewV4PublicVerifier([]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc"), WithAllowAnyAudience(),
	)
	require.NoError(t, err)
	claims, err := verifier.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Subject)
}

func TestSigningProvider_RefreshSwapsKey(t *testing.T) {
	pubOld, privOld := newPrivateKey(t)
	pubNew, privNew := newPrivateKey(t)

	var which atomic.Pointer[ed25519.PrivateKey]
	which.Store(&privOld)

	p, err := OpenSigningProvider(context.Background(),
		func(_ context.Context) (*secret.String, error) {
			k := which.Load()
			return secret.New(*k), nil
		},
		time.Hour,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	tokOld, err := p.Sign(Claims{Subject: "alice", Audience: []string{"any"}, ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)

	verifierOld, err := NewV4PublicVerifier([]ed25519.PublicKey{pubOld},
		WithExpectedIssuer("svc"), WithAllowAnyAudience(),
	)
	require.NoError(t, err)
	_, err = verifierOld.Verify(tokOld, time.Now())
	require.NoError(t, err)

	// Rotate the source; force a refresh directly because the loop's
	// ticker is set to time.Hour to keep the test deterministic.
	which.Store(&privNew)
	require.NoError(t, p.refresh(context.Background()))

	tokNew, err := p.Sign(Claims{Subject: "bob", Audience: []string{"any"}, ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)

	verifierNew, err := NewV4PublicVerifier([]ed25519.PublicKey{pubNew},
		WithExpectedIssuer("svc"), WithAllowAnyAudience(),
	)
	require.NoError(t, err)
	claims, err := verifierNew.Verify(tokNew, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "bob", claims.Subject)

	// A token signed with the previous key must not verify against the
	// new public key — this is a cryptographic property, independent of
	// whether the previous signer's key bytes have been zeroed yet
	// (they are released to the GC at swap and zeroed only at
	// SigningProvider.Close to avoid racing with in-flight Sign calls).
	_, err = verifierNew.Verify(tokOld, time.Now())
	require.Error(t, err)
}

func TestSigningProvider_RejectsSignAfterMaxStale(t *testing.T) {
	_, priv := newPrivateKey(t)

	called := atomic.Bool{}
	src := func(_ context.Context) (*secret.String, error) {
		// First call succeeds (initial load); subsequent calls fail
		// so refresh stalls.
		if called.Swap(true) {
			return nil, errors.New("kms blip")
		}
		return secret.New(priv), nil
	}

	fixed := time.Now()
	clock := func() time.Time { return fixed }
	p, err := OpenSigningProvider(context.Background(), src, time.Minute, // <= maxStale
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithSigningMaxStale(time.Minute),
		withSigningProviderClock(clock),
		WithOnSigningRefreshError(func(error) {}),
	)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	// Within the stale window: Sign succeeds.
	_, err = p.Sign(Claims{Subject: "alice", Audience: []string{"any"}, ExpiresAt: fixed.Add(time.Hour)})
	require.NoError(t, err)

	// Advance past maxStale; the next Sign must fail closed.
	fixed = fixed.Add(2 * time.Minute)
	_, err = p.Sign(Claims{Subject: "alice", Audience: []string{"any"}, ExpiresAt: fixed.Add(time.Hour)})
	require.ErrorIs(t, err, ErrKeySetUnavailable)
}

func TestSigningProvider_AfterCloseReturnsProviderClosed(t *testing.T) {
	_, priv := newPrivateKey(t)
	p, err := OpenSigningProvider(context.Background(), privSource(priv), time.Hour,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	require.NoError(t, p.Close())

	_, err = p.Sign(Claims{Subject: "alice", Audience: []string{"any"}, ExpiresAt: time.Now().Add(time.Hour)})
	require.ErrorIs(t, err, ErrProviderClosed)
}

func TestSigningProvider_CloseIsIdempotent(t *testing.T) {
	_, priv := newPrivateKey(t)
	p, err := OpenSigningProvider(context.Background(), privSource(priv), time.Hour,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	require.NoError(t, p.Close())
	require.NoError(t, p.Close())
}

func TestWithSigningFetchTimeout_RejectsNonPositiveDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"zero":     func() { WithSigningFetchTimeout(0) },
		"negative": func() { WithSigningFetchTimeout(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, fn)
		})
	}
}

func TestWithSigningFetchTimeout_OverridesDefault(t *testing.T) {
	_, priv := newPrivateKey(t)
	p, err := OpenSigningProvider(context.Background(), privSource(priv), time.Hour,
		WithSigningFetchTimeout(42*time.Second),
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	assert.Equal(t, 42*time.Second, p.fetchTimeout,
		"WithSigningFetchTimeout must override the default per-refresh deadline")
}

func TestSigningProvider_CloseDoesNotFireSpuriousRefreshError(t *testing.T) {
	_, priv := newPrivateKey(t)

	var first atomic.Bool
	first.Store(true)
	refreshStarted := make(chan struct{}, 1)
	var refreshErrors atomic.Int32

	src := func(ctx context.Context) (*secret.String, error) {
		if first.CompareAndSwap(true, false) {
			return secret.New(priv), nil
		}
		// Post-init refresh: announce we're in flight, then block until
		// the context is cancelled (by Close) and surface the
		// cancellation as the refresh error.
		select {
		case refreshStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	p, err := OpenSigningProvider(context.Background(), src, 5*time.Millisecond,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithOnSigningRefreshError(func(error) { refreshErrors.Add(1) }),
	)
	require.NoError(t, err)

	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("post-init refresh never started")
	}
	require.NoError(t, p.Close())

	assert.Equal(t, int32(0), refreshErrors.Load(),
		"Close cancelling an in-flight refresh must not fire onRefreshErr (false 'rotation stalled' alert on shutdown)")
}

func TestSigningProvider_OnRefreshErrorCallbackPanicSwallowed(t *testing.T) {
	_, priv := newPrivateKey(t)

	calls := atomic.Int64{}
	src := func(_ context.Context) (*secret.String, error) {
		n := calls.Add(1)
		if n == 1 {
			return secret.New(priv), nil
		}
		return nil, errors.New("post-init failure")
	}

	cbInvoked := make(chan struct{}, 1)
	p, err := OpenSigningProvider(context.Background(), src, time.Hour,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithOnSigningRefreshError(func(err error) {
			// Panic WITHOUT self-recovery so the panic actually escapes
			// the callback and reaches SigningProvider.callOnRefreshError's
			// recover wrapper. A self-recovering callback would never
			// exercise that wrapper, so the test would pass even if the
			// wrapper were deleted.
			select {
			case cbInvoked <- struct{}{}:
			default:
			}
			panic("intentional panic from test callback")
		}),
	)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	// Force a refresh-error notification outside the ticker path so the
	// test is deterministic. callOnRefreshError must recover the panicking
	// callback and return normally; if its recover wrapper were missing,
	// the panic would propagate here and fail the test.
	require.NotPanics(t, func() {
		p.callOnRefreshError(errors.New("simulated"))
	}, "callOnRefreshError must recover a panicking callback")
	select {
	case <-cbInvoked:
	case <-time.After(time.Second):
		t.Fatal("OnSigningRefreshError callback was not invoked")
	}
}

func TestSigningProvider_RaceSignAndRefresh(t *testing.T) {
	_, priv := newPrivateKey(t)

	p, err := OpenSigningProvider(context.Background(), privSource(priv), time.Hour,
		WithSigningOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	defer func() { _ = p.Close() }()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = p.Sign(Claims{Subject: "alice", Audience: []string{"any"}, ExpiresAt: time.Now().Add(time.Hour)})
			}
		}()
	}

	for i := 0; i < 20; i++ {
		require.NoError(t, p.refresh(context.Background()))
	}
	close(stop)
	wg.Wait()
}
