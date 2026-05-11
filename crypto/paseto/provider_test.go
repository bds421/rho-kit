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
)

func mustGenKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

func TestNewProvider_RejectsNilSource(t *testing.T) {
	_, err := NewProvider(context.Background(), nil, time.Second)
	require.Error(t, err)
}

func TestNewProvider_RejectsNilContext(t *testing.T) {
	pub, _ := mustGenKey(t)
	_, err := NewProvider(nilContextForTest(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		time.Second,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.Error(t, err)
}

func TestNewProvider_RejectsZeroInterval(t *testing.T) {
	pub, _ := mustGenKey(t)
	_, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		0,
	)
	require.Error(t, err)
}

func TestNewProvider_RejectsNilOption(t *testing.T) {
	pub, _ := mustGenKey(t)
	_, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		time.Second,
		nil,
	)
	require.Error(t, err)
}

func TestWithVerifyOptionsCopiesCallerSlice(t *testing.T) {
	pub, _ := mustGenKey(t)
	opts := []Option{WithExpectedIssuer("svc"), WithAllowAnyAudience()}
	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		time.Hour,
		WithVerifyOptions(opts...),
	)
	require.NoError(t, err)
	defer p.Stop()

	opts[0] = nil
	require.NoError(t, p.refresh(context.Background()))
}

func TestNewProvider_PropagatesInitialLoadFailure(t *testing.T) {
	boom := errors.New("backend down")
	_, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return nil, boom },
		time.Second,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.ErrorIs(t, err, boom)
}

func TestProvider_VerifiesAgainstActiveKey(t *testing.T) {
	pub, priv := mustGenKey(t)
	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		time.Hour,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	defer p.Stop()

	signer, err := NewV4Public([]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc"), WithAllowAnyAudience())
	require.NoError(t, err)
	tok, err := signer.Sign(Claims{
		Subject:   "alice",
		Issuer:    "svc",
		ExpiresAt: time.Now().Add(time.Hour),
	}, priv)
	require.NoError(t, err)

	claims, err := p.Verify(tok, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Subject)
}

func TestProvider_RotatesAcceptedKey(t *testing.T) {
	pubOld, privOld := mustGenKey(t)
	pubNew, privNew := mustGenKey(t)

	var current atomic.Pointer[[]ed25519.PublicKey]
	keysOld := []ed25519.PublicKey{pubOld}
	current.Store(&keysOld)

	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) {
			return *current.Load(), nil
		},
		20*time.Millisecond,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	defer p.Stop()

	signOld, err := NewV4Public([]ed25519.PublicKey{pubOld},
		WithExpectedIssuer("svc"), WithAllowAnyAudience())
	require.NoError(t, err)
	signNew, err := NewV4Public([]ed25519.PublicKey{pubNew},
		WithExpectedIssuer("svc"), WithAllowAnyAudience())
	require.NoError(t, err)

	tokOld, err := signOld.Sign(Claims{
		Subject: "alice", Issuer: "svc",
		ExpiresAt: time.Now().Add(time.Hour),
	}, privOld)
	require.NoError(t, err)
	tokNew, err := signNew.Sign(Claims{
		Subject: "bob", Issuer: "svc",
		ExpiresAt: time.Now().Add(time.Hour),
	}, privNew)
	require.NoError(t, err)

	// Initially only the old key is trusted.
	_, err = p.Verify(tokOld, time.Now())
	require.NoError(t, err)
	_, err = p.Verify(tokNew, time.Now())
	require.Error(t, err, "new key should be rejected before rotation")

	// Rotate trust set.
	keysNew := []ed25519.PublicKey{pubNew}
	current.Store(&keysNew)

	// Wait for at least one refresh tick.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.Verify(tokNew, time.Now()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err = p.Verify(tokNew, time.Now())
	assert.NoError(t, err, "new key should be trusted after refresh")
}

func TestProvider_KeepsOldKeysOnRefreshFailure(t *testing.T) {
	pub, priv := mustGenKey(t)
	var failNext atomic.Bool
	var refreshErrors atomic.Int32

	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) {
			if failNext.Load() {
				return nil, errors.New("source unavailable")
			}
			return []ed25519.PublicKey{pub}, nil
		},
		20*time.Millisecond,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithOnRefreshError(func(error) { refreshErrors.Add(1) }),
	)
	require.NoError(t, err)
	defer p.Stop()

	signer, _ := NewV4Public([]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc"), WithAllowAnyAudience())
	tok, _ := signer.Sign(Claims{
		Subject: "alice", Issuer: "svc",
		ExpiresAt: time.Now().Add(time.Hour),
	}, priv)

	failNext.Store(true)
	// Wait until at least one refresh tick has fired.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && refreshErrors.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Greater(t, refreshErrors.Load(), int32(0), "refresh error callback must fire")

	// Old key set must still verify.
	_, err = p.Verify(tok, time.Now())
	assert.NoError(t, err, "previous key set must keep working when refresh fails")
}

func TestProvider_FailsClosedWhenKeySetStale(t *testing.T) {
	pub, priv := mustGenKey(t)
	now := time.Unix(1000, 0)

	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) {
			return []ed25519.PublicKey{pub}, nil
		},
		time.Hour,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithMaxStale(time.Minute),
		withProviderClock(func() time.Time { return now }),
	)
	require.NoError(t, err)
	defer p.Stop()

	signer, _ := NewV4Public([]ed25519.PublicKey{pub},
		WithExpectedIssuer("svc"), WithAllowAnyAudience())
	tok, _ := signer.Sign(Claims{
		Subject: "alice", Issuer: "svc",
		ExpiresAt: now.Add(time.Hour),
	}, priv)

	_, err = p.Verify(tok, now)
	require.NoError(t, err)

	now = now.Add(2 * time.Minute)
	_, err = p.Verify(tok, now)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
}

func TestProviderOptions_RejectNonPositiveDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithFetchTimeout zero":     func() { WithFetchTimeout(0) },
		"WithFetchTimeout negative": func() { WithFetchTimeout(-time.Second) },
		"WithMaxStale zero":         func() { WithMaxStale(0) },
		"WithMaxStale negative":     func() { WithMaxStale(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, fn)
		})
	}
}

func TestProvider_RejectsEmptyKeySetOnRefresh(t *testing.T) {
	pub, _ := mustGenKey(t)
	first := true
	var refreshErrors atomic.Int32
	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) {
			if first {
				first = false
				return []ed25519.PublicKey{pub}, nil
			}
			return nil, nil // empty — must be refused
		},
		20*time.Millisecond,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithOnRefreshError(func(error) { refreshErrors.Add(1) }),
	)
	require.NoError(t, err)
	defer p.Stop()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && refreshErrors.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Greater(t, refreshErrors.Load(), int32(0), "empty-set refresh must surface as a refresh error, not a silent swap")
}

func TestProvider_OnRefreshErrorPanicDoesNotCrashLoop(t *testing.T) {
	pub, _ := mustGenKey(t)
	var first atomic.Bool
	first.Store(true)
	var refreshErrors atomic.Int32

	p, err := NewProvider(context.Background(),
		func(context.Context) ([]ed25519.PublicKey, error) {
			if first.CompareAndSwap(true, false) {
				return []ed25519.PublicKey{pub}, nil
			}
			return nil, errors.New("source unavailable")
		},
		20*time.Millisecond,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
		WithOnRefreshError(func(error) {
			refreshErrors.Add(1)
			panic("refresh hook exploded")
		}),
	)
	require.NoError(t, err)
	defer p.Stop()

	require.Eventually(t, func() bool {
		return refreshErrors.Load() > 0
	}, time.Second, 10*time.Millisecond)
}

func TestProvider_StopIdempotent(t *testing.T) {
	pub, _ := mustGenKey(t)
	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		time.Hour,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)
	p.Stop()
	p.Stop() // must not panic
}

func TestProvider_StopConcurrentSafe(t *testing.T) {
	pub, _ := mustGenKey(t)
	p, err := NewProvider(context.Background(),
		func(_ context.Context) ([]ed25519.PublicKey, error) { return []ed25519.PublicKey{pub}, nil },
		time.Hour,
		WithVerifyOptions(WithExpectedIssuer("svc"), WithAllowAnyAudience()),
	)
	require.NoError(t, err)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			p.Stop()
		}()
	}
	close(start)
	wg.Wait()
}

func TestProvider_InvalidReceiverDoesNotPanic(t *testing.T) {
	var nilProvider *Provider
	if _, err := nilProvider.Verify("token", time.Now()); !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("nil Verify error = %v, want ErrKeySetUnavailable", err)
	}
	nilProvider.Stop()

	var zero Provider
	if _, err := zero.Verify("token", time.Now()); !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("zero Verify error = %v, want ErrKeySetUnavailable", err)
	}
	zero.Stop()
}

func nilContextForTest() context.Context { return nil }
