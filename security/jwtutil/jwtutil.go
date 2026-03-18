// Package jwtutil provides JWT verification for Oathkeeper-signed id_tokens
// backed by lestrrat-go/jwx/v3.
//
// It supports ES256 tokens signed by Oathkeeper's id_token mutator. Public
// keys are fetched from Oathkeeper's JWKS endpoint and cached with periodic
// background refresh.
package jwtutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/bds421/rho-kit/resilience/retry"
)

const (
	clockSkew              = 30 * time.Second
	defaultRefreshInterval = 10 * time.Minute
	defaultHTTPTimeout     = 5 * time.Second
)

// Claims represents the verified JWT payload from an Oathkeeper id_token.
type Claims struct {
	Subject     string   `json:"sub"`
	Permissions []string `json:"permissions"`
	Scopes      string   `json:"scopes"`
	IssuedAt    int64    `json:"iat"`
	ExpiresAt   int64    `json:"exp"`
	NotBefore   int64    `json:"nbf"`
	Issuer      string   `json:"iss"`
}

// KeySet holds a JWKS key set for JWT signature verification.
type KeySet struct {
	set jwk.Set
	// ExpectedIssuer, when non-empty, is validated against the "iss" claim.
	ExpectedIssuer string
}

// ParseKeySet parses a JWKS JSON document into a KeySet.
func ParseKeySet(data []byte) (*KeySet, error) {
	set, err := jwk.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	if set.Len() == 0 {
		return nil, errors.New("jwks contains no usable keys")
	}
	return &KeySet{set: set}, nil
}

// ParseKeySetFromPEM parses a PEM-encoded public key into a KeySet with a
// single key using the given key ID.
func ParseKeySetFromPEM(pemData []byte, kid string) (*KeySet, error) {
	key, err := jwk.ParseKey(pemData, jwk.WithPEM(true))
	if err != nil {
		return nil, fmt.Errorf("parse PEM key: %w", err)
	}
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		return nil, err
	}
	set := jwk.NewSet()
	if err := set.AddKey(key); err != nil {
		return nil, err
	}
	return &KeySet{set: set}, nil
}

// Verify parses and verifies a compact-serialized JWT (header.payload.signature).
// It validates the signature, expiration, and not-before claims.
func (ks *KeySet) Verify(tokenString string, now time.Time) (*Claims, error) {
	parseOpts := []jwt.ParseOption{
		jwt.WithKeySet(ks.set, jws.WithInferAlgorithmFromKey(true)),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(clockSkew),
		jwt.WithClock(jwt.ClockFunc(func() time.Time { return now })),
	}
	if ks.ExpectedIssuer != "" {
		parseOpts = append(parseOpts, jwt.WithIssuer(ks.ExpectedIssuer))
	}
	tok, err := jwt.Parse([]byte(tokenString), parseOpts...)
	if err != nil {
		return nil, err
	}

	sub, _ := tok.Subject()
	if sub == "" {
		return nil, errors.New("missing sub claim")
	}

	iss, _ := tok.Issuer()
	claims := &Claims{
		Subject: sub,
		Issuer:  iss,
	}
	if iat, ok := tok.IssuedAt(); ok {
		claims.IssuedAt = iat.Unix()
	}
	if exp, ok := tok.Expiration(); ok {
		claims.ExpiresAt = exp.Unix()
	}
	if nbf, ok := tok.NotBefore(); ok {
		claims.NotBefore = nbf.Unix()
	}

	var perms []any
	if err := tok.Get("permissions", &perms); err == nil {
		claims.Permissions = toStringSlice(perms)
	} else {
		slog.Debug("jwt: permissions claim absent or invalid", "error", err)
	}
	var scopes string
	if err := tok.Get("scopes", &scopes); err == nil {
		claims.Scopes = scopes
	} else {
		slog.Debug("jwt: scopes claim absent or invalid", "error", err)
	}

	return claims, nil
}

// toStringSlice converts a JSON-decoded value to []string.
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// Provider manages JWKS fetching and caching. It fetches the public key set
// from the configured URL on first use and refreshes it periodically.
//
// Note: jwk.Cache exists but uses the same interval for retry and refresh,
// making it unsuitable here — we need aggressive retry on startup (2s backoff)
// but infrequent periodic refresh (5–10 min).
type Provider struct {
	url            string
	httpClient     *http.Client
	refresh        time.Duration
	expectedIssuer string

	mu     sync.RWMutex
	keyset *KeySet
}

// ProviderOption configures optional Provider behaviour.
type ProviderOption func(*Provider)

// WithExpectedIssuer sets the JWT issuer claim that Verify will validate.
func WithExpectedIssuer(issuer string) ProviderOption {
	return func(p *Provider) { p.expectedIssuer = issuer }
}

// NewProvider creates a JWKS provider that fetches public keys from the given URL.
// The refresh interval controls how often keys are re-fetched in the background.
// If httpClient is nil, a default client with a 5s timeout is used.
// If refresh <= 0, it defaults to 10 minutes.
func NewProvider(url string, httpClient *http.Client, refresh time.Duration, opts ...ProviderOption) *Provider {
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	if refresh <= 0 {
		refresh = defaultRefreshInterval
	}
	p := &Provider{
		url:        url,
		httpClient: httpClient,
		refresh:    refresh,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// NewProviderWithKeySet creates a Provider pre-loaded with a key set.
// This is intended for testing — the provider will not fetch or refresh keys.
func NewProviderWithKeySet(ks *KeySet) *Provider {
	return &Provider{keyset: ks}
}

// Run starts the background JWKS refresh loop. It blocks until ctx is cancelled.
// Call this in a goroutine before serving requests.
func (p *Provider) Run(ctx context.Context) {
	if p.url == "" {
		// No JWKS URL configured (e.g. test provider created via NewProviderWithKeySet).
		// Block until context is cancelled to match the expected lifecycle contract.
		<-ctx.Done()
		return
	}

	// Phase 1: initial fetch with retry until success.
	err := retry.Do(ctx, func(ctx context.Context) error {
		return p.fetch(ctx)
	},
		retry.WithMaxRetries(-1),
		retry.WithBaseDelay(2*time.Second),
		retry.WithMaxDelay(60*time.Second),
		retry.WithFactor(2.0),
		retry.WithJitter(0.25),
	)
	if err != nil {
		return // context cancelled
	}

	// Phase 2: periodic refresh — failures are non-fatal (cached keys remain valid).
	ticker := time.NewTicker(p.refresh)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.fetch(ctx); err != nil {
				slog.Warn("jwks periodic refresh failed, using cached keys",
					"url", p.url, "error", err)
			}
		}
	}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// KeySet returns the current cached key set. Returns nil if keys haven't
// been fetched yet (provider not started or still retrying).
func (p *Provider) KeySet() *KeySet {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.keyset
}

func (p *Provider) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
	if err != nil {
		return err
	}

	ks, err := ParseKeySet(body)
	if err != nil {
		return err
	}
	ks.ExpectedIssuer = p.expectedIssuer

	p.mu.Lock()
	p.keyset = ks
	p.mu.Unlock()
	return nil
}
