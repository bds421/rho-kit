package oauth2

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/secret"
	"github.com/bds421/rho-kit/observability/v2/health"
	xoauth2 "golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// ClientCredentialsConfig configures an OAuth 2.0 client-credentials token
// source. Secrets are never exposed by this type or included in errors.
type ClientCredentialsConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret *secret.String
	Scopes       []string
}

// ClientCredentials caches a short-lived service token and serializes refresh
// work, preventing concurrent callers from stampeding the token endpoint.
type ClientCredentials struct {
	config     clientcredentials.Config
	skew       time.Duration
	mu         sync.Mutex
	token      *xoauth2.Token
	refreshing chan struct{}
	metrics    *ClientCredentialsMetrics
}

// ClientCredentialsOption configures a [ClientCredentials] source.
type ClientCredentialsOption func(*ClientCredentials)

// WithClientCredentialsMetrics supplies explicit metrics. Omit it to use the
// standard registered collectors.
func WithClientCredentialsMetrics(metrics *ClientCredentialsMetrics) ClientCredentialsOption {
	if metrics == nil {
		panic("oauth2: WithClientCredentialsMetrics requires non-nil metrics")
	}
	return func(c *ClientCredentials) { c.metrics = metrics }
}

// NewClientCredentials constructs a refresh-before-expiry token source.
func NewClientCredentials(cfg ClientCredentialsConfig, opts ...ClientCredentialsOption) (*ClientCredentials, error) {
	if cfg.TokenURL == "" || cfg.ClientID == "" || cfg.ClientSecret == nil || cfg.ClientSecret.IsEmpty() {
		return nil, errors.New("oauth2: client credentials require token URL, client ID, and client secret")
	}
	clientConfig := clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret.RevealString(), TokenURL: cfg.TokenURL, Scopes: append([]string(nil), cfg.Scopes...)}
	source := &ClientCredentials{config: clientConfig, skew: 30 * time.Second, metrics: NewClientCredentialsMetrics()}
	for _, opt := range opts {
		if opt == nil {
			panic("oauth2: ClientCredentials option must not be nil")
		}
		opt(source)
	}
	return source, nil
}

// Token returns a cached token while valid. Concurrent refreshes share one
// token-endpoint call; callers waiting on an in-flight refresh still observe
// their own cancellation/deadline.
func (c *ClientCredentials) Token(ctx context.Context) (*xoauth2.Token, error) {
	if c == nil || c.config.TokenURL == "" {
		return nil, errors.New("oauth2: client credentials not initialized")
	}
	if ctx == nil {
		return nil, errors.New("oauth2: context must not be nil")
	}
	for {
		c.mu.Lock()
		if c.token != nil && time.Until(c.token.Expiry) > c.skew {
			clone := *c.token
			c.mu.Unlock()
			c.metrics.cacheHit()
			return &clone, nil
		}
		if done := c.refreshing; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				// A successful leader refresh is returned on the next loop. A
				// failed refresh may be retried by this caller with its own ctx.
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		c.refreshing = done
		c.mu.Unlock()

		// Construct the source at refresh time so the token endpoint sees the
		// leader caller's cancellation and deadline rather than a construction-
		// time context.
		started := time.Now()
		token, err := c.config.Token(ctx)
		c.metrics.refreshed(time.Since(started).Seconds(), err == nil)
		c.mu.Lock()
		if err == nil {
			c.token = token
		}
		c.refreshing = nil
		close(done)
		c.mu.Unlock()
		if err != nil {
			return nil, errors.New("oauth2: client credentials token refresh failed")
		}
		clone := *token
		return &clone, nil
	}
}

// HealthCheck returns a critical readiness check for an outbound OAuth
// dependency. It verifies a cached token or refreshes one using the health
// probe's bounded context; no token value is exposed.
func (c *ClientCredentials) HealthCheck() health.DependencyCheck {
	return health.DependencyCheck{
		Name: "oauth2-client-credentials",
		Check: func(ctx context.Context) string {
			if _, err := c.Token(ctx); err != nil {
				return health.StatusUnhealthy
			}
			return health.StatusHealthy
		},
		Critical: true,
	}
}

// Start implements lifecycle.Component. It obtains an initial token, then
// refreshes ahead of expiry until ctx is cancelled. Services register the
// source with their lifecycle runner; request callers remain free to call
// Token directly and share the same single-flight refresh path.
func (c *ClientCredentials) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("oauth2: client credentials start context must not be nil")
	}
	if _, err := c.Token(ctx); err != nil {
		return err
	}
	for {
		delay := c.refreshDelay()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
			if _, err := c.Token(ctx); err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
}

// Stop implements lifecycle.Component. Token refresh work is controlled by
// the context supplied to Start, so there are no background resources left to
// close here.
func (c *ClientCredentials) Stop(_ context.Context) error { return nil }

func (c *ClientCredentials) refreshDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == nil || c.token.Expiry.IsZero() {
		return time.Second
	}
	delay := time.Until(c.token.Expiry) - c.skew
	if delay < time.Second {
		return time.Second
	}
	return delay
}
