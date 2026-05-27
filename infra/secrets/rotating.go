package secrets

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// NewRotatingProvider returns a function that resolves the current
// value for key against loader on every call. Suitable for SDK
// "credential provider" hooks (pgx PasswordProvider, go-redis
// CredentialsProvider, AMQP URL provider) that accept a callback the
// SDK calls before each (re)connect.
//
// Each call hits the supplied [Loader] — wrap in [NewCachedLoader] for
// hot paths. The provider does not cache on its own because most SDKs
// hand it a fresh ctx per connect and expect the loader to dedup.
//
// Pass timeout = 0 to use context.Background() unbounded; positive
// values bound each invocation with context.WithTimeout(parent, timeout).
func NewRotatingProvider(loader Loader, key string, timeout time.Duration) func() (string, error) {
	if loader == nil {
		panic("secrets: NewRotatingProvider requires non-nil loader")
	}
	if key == "" {
		panic("secrets: NewRotatingProvider requires non-empty key")
	}
	return func() (string, error) {
		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		s, err := loader.Get(ctx, key)
		if err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				// Annotating a kit sentinel with key context is the
				// canonical use of fmt.Errorf %w — the wrapped value
				// is the sentinel itself, nothing to redact.
				return "", fmt.Errorf("secrets: %s: %w", key, err) // kit:ok-fmt-errorf-wrap
			}
			return "", err
		}
		if s.Value == nil {
			return "", errors.New("secrets: nil value returned by loader")
		}
		return s.Value.RevealString(), nil
	}
}
