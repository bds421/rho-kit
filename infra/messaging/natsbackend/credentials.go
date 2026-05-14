package natsbackend

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// errEmptyCredential is the sentinel the bridge uses internally when
// a provider returns ("", "", nil) / ("", nil). Treated identically
// to a provider-reported error: log + fall back to cached value if
// one exists, refuse to overwrite cached credentials with an empty
// value. Distinct enough from ctx errors that the warn log can
// classify the cause.
var errEmptyCredential = errors.New("natsbackend: credential provider returned an empty value")

// userPassBridge adapts a ctx-and-error-aware [Config.UsernamePasswordProvider]
// to nats.go's contextless `func() (string, string)` UserInfoHandler. Each
// invocation derives a fresh context with the configured per-call timeout
// and caches the most recent successful credential pair so a transient
// secret-manager outage does not invalidate an established connection.
type userPassBridge struct {
	provider func(ctx context.Context) (string, string, error)
	timeout  time.Duration

	mu    sync.Mutex
	user  string
	pass  string
	cache bool
}

func newUserPassBridge(provider func(ctx context.Context) (string, string, error), timeout time.Duration) func() (string, string) {
	b := &userPassBridge{provider: provider, timeout: timeout}
	return b.fetch
}

func (b *userPassBridge) fetch() (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	user, pass, err := b.provider(ctx)
	// Treat (\"\", \"\", nil) as failure: a credential bug that
	// silently replaced a good cached pair with an empty one would
	// break reauth even though the previous credential was still
	// available.
	if err == nil && (user == "" || pass == "") {
		err = errEmptyCredential
	}
	if err != nil {
		b.mu.Lock()
		cached, u, p := b.cache, b.user, b.pass
		b.mu.Unlock()
		// redact.Error sanitizes provider error strings so vault
		// namespaces, secret paths, or token hints do not leak into
		// the kit's logs.
		slog.Default().Warn("natsbackend: UsernamePasswordProvider failed",
			redact.Error(err),
			slog.Bool("served_cached", cached),
		)
		if cached {
			return u, p
		}
		// No cached value — return empty pair; nats.go surfaces an
		// auth error which the kit's reconnect loop retries.
		return "", ""
	}
	b.mu.Lock()
	b.user, b.pass, b.cache = user, pass, true
	b.mu.Unlock()
	return user, pass
}

// tokenBridge mirrors [userPassBridge] for the bearer-token variant.
type tokenBridge struct {
	provider func(ctx context.Context) (string, error)
	timeout  time.Duration

	mu    sync.Mutex
	token string
	cache bool
}

func newTokenBridge(provider func(ctx context.Context) (string, error), timeout time.Duration) func() string {
	b := &tokenBridge{provider: provider, timeout: timeout}
	return b.fetch
}

func (b *tokenBridge) fetch() string {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	tok, err := b.provider(ctx)
	// Treat ("", nil) as failure — see the userPassBridge equivalent.
	// A transient provider bug must not invalidate an established
	// cached token.
	if err == nil && tok == "" {
		err = errEmptyCredential
	}
	if err != nil {
		b.mu.Lock()
		cached, last := b.cache, b.token
		b.mu.Unlock()
		slog.Default().Warn("natsbackend: TokenProvider failed",
			redact.Error(err),
			slog.Bool("served_cached", cached),
		)
		if cached {
			return last
		}
		return ""
	}
	b.mu.Lock()
	b.token, b.cache = tok, true
	b.mu.Unlock()
	return tok
}
