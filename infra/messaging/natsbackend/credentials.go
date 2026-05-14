package natsbackend

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

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
	if err != nil {
		b.mu.Lock()
		cached, u, p := b.cache, b.user, b.pass
		b.mu.Unlock()
		slog.Default().Warn("natsbackend: UsernamePasswordProvider failed",
			slog.Any("error", err),
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
	if err != nil {
		b.mu.Lock()
		cached, last := b.cache, b.token
		b.mu.Unlock()
		slog.Default().Warn("natsbackend: TokenProvider failed",
			slog.Any("error", err),
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
