package oauth2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// LogoutToken is a verified OpenID Connect back-channel logout token. At
// least one of Subject and SID is set. With SID, end the sessions that
// provider session started (only the subject's when Subject is set too);
// with Subject alone, every session of the subject at Issuer.
type LogoutToken struct {
	Issuer  string
	Subject string
	SID     string
}

// WithFlowKeyRefetchInterval bounds how often logout tokens make the
// provider's keys (JWKS) be fetched again, per issuer (default one minute).
// A logout token comes from anyone, and one whose signature does not
// verify against the cached keys would otherwise cause a fetch each time.
// After a key rotation, tokens signed with the new key fail until the next
// fetch is allowed.
func WithFlowKeyRefetchInterval(d time.Duration) FlowOption {
	return func(f *Flow) { f.keyRefetch = d }
}

// WithFlowReplayCache sets the cache that rejects replayed logout tokens in
// [Flow.VerifyLogoutToken]. Default: the state store when it implements
// [ReplayCache] (auth/oauth2/redis does), otherwise an in-process
// [MemoryReplayCache], which is only correct for a single replica.
func WithFlowReplayCache(cache ReplayCache) FlowOption {
	if cache == nil {
		panic("oauth2: WithFlowReplayCache requires a non-nil cache")
	}
	return func(f *Flow) { f.replay = cache }
}

// VerifyLogoutToken checks a back-channel logout token (OpenID Connect
// Back-Channel Logout 1.0) that the provider of cfg posted as the form value
// logout_token: signature against the provider's JWKS, iss = cfg.Issuer,
// aud = cfg.ClientID and exp; iat present, at most five minutes old and at
// most a minute ahead; the back-channel logout event in events; sid and/or
// sub; no nonce. Only Issuer and ClientID of cfg are used.
//
// admit, when given, runs on a verified token that was not used before,
// after its jti is recorded (so a replay never reaches it); its error is
// returned as it is and the jti forgotten, so the provider may deliver the
// token again. Use it for what only a verified, first delivery should
// spend, such as a budget per provider, so forged or replayed tokens
// cannot use it up.
//
// The jti is recorded before VerifyLogoutToken returns, so a second
// delivery fails with [ErrLogoutTokenReplayed] even when the caller failed
// to end the sessions of the first. Other failures are
// [ErrLogoutTokenInvalid] and [ErrIssuerDiscovery], or the replay cache's
// error. The caller answers 200 on success and 400 with
// {"error":"invalid_request"} for an invalid or replayed token.
func (f *Flow) VerifyLogoutToken(ctx context.Context, cfg Config, raw string, admit ...func(context.Context, LogoutToken) error) (LogoutToken, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || raw == "" {
		return LogoutToken{}, ErrLogoutTokenInvalid
	}
	provider, err := f.provider(ctx, cfg.Issuer)
	if err != nil {
		return LogoutToken{}, err
	}
	verifier, err := f.logoutVerifier(ctx, provider, cfg)
	if err != nil {
		return LogoutToken{}, err
	}
	tok, err := verifyLogoutToken(ctx, verifier, f.httpClient, raw, f.now())
	if err != nil {
		return LogoutToken{}, fmt.Errorf("%w: %w", ErrLogoutTokenInvalid, redact.WrapError("logout token", err))
	}
	first, err := f.replay.Remember(ctx, tok.replayKey(), logoutTokenMaxAge+logoutTokenClockSkew)
	if err != nil {
		return LogoutToken{}, redact.WrapError("oauth2: record the logout token", err)
	}
	if !first {
		return LogoutToken{}, ErrLogoutTokenReplayed
	}
	verified := LogoutToken{Issuer: tok.issuer, Subject: tok.subject, SID: tok.sid}
	for _, fn := range admit {
		if err := fn(ctx, verified); err != nil {
			// Refused after the jti was recorded: forget it, so the provider
			// may deliver the token again.
			if ferr := f.replay.Forget(ctx, tok.replayKey()); ferr != nil {
				return LogoutToken{}, errors.Join(err, redact.WrapError("oauth2: forget the logout token", ferr))
			}
			return LogoutToken{}, err
		}
	}
	return verified, nil
}

// logoutKeySet is the key set of an issuer's logout tokens: go-oidc's
// remote key set, which fetches the keys again whenever a signature does
// not verify, behind a client that allows one fetch per keyRefetch.
type logoutKeySet struct {
	jwksURL string
	keys    *oidc.RemoteKeySet
}

// errKeyRefetchBounded fails a key fetch that came too soon after the last.
var errKeyRefetchBounded = errors.New("oauth2: the provider's keys were fetched moments ago")

// boundedFetch lets one request through per interval.
type boundedFetch struct {
	next  http.RoundTripper
	every time.Duration
	now   func() time.Time

	mu   sync.Mutex
	last time.Time
}

func (b *boundedFetch) RoundTrip(r *http.Request) (*http.Response, error) {
	b.mu.Lock()
	now := b.now()
	if !b.last.IsZero() && now.Sub(b.last) < b.every {
		b.mu.Unlock()
		return nil, errKeyRefetchBounded
	}
	b.last = now
	b.mu.Unlock()
	return b.next.RoundTrip(r)
}

// signingAlgs are the algorithms go-oidc verifies.
var signingAlgs = []string{
	oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512, oidc.EdDSA,
}

// logoutVerifier checks logout tokens of cfg against the issuer's bounded
// key set, kept per issuer while its discovery names the same JWKS URL.
func (f *Flow) logoutVerifier(ctx context.Context, provider *oidc.Provider, cfg Config) (*oidc.IDTokenVerifier, error) {
	var meta struct {
		JWKSURL string   `json:"jwks_uri"`
		Algs    []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := provider.Claims(&meta); err != nil || meta.JWKSURL == "" {
		return nil, fmt.Errorf("%w: discovery names no jwks_uri", ErrIssuerDiscovery)
	}
	f.mu.Lock()
	set, ok := f.logoutKeys[cfg.Issuer]
	if !ok || set.jwksURL != meta.JWKSURL {
		next := f.httpClient.Transport
		if next == nil {
			next = http.DefaultTransport
		}
		// kit-doctor:allow default-http-client reason="wraps the caller's client transport and keeps its timeout"
		client := &http.Client{Timeout: f.httpClient.Timeout, Transport: &boundedFetch{next: next, every: f.keyRefetch, now: f.now}}
		// The key set keeps its context for later fetches, as the provider does.
		set = logoutKeySet{jwksURL: meta.JWKSURL, keys: oidc.NewRemoteKeySet(oidc.ClientContext(context.WithoutCancel(ctx), client), meta.JWKSURL)}
		f.logoutKeys[cfg.Issuer] = set
	}
	f.mu.Unlock()
	algs := slices.DeleteFunc(slices.Clone(meta.Algs), func(a string) bool { return !slices.Contains(signingAlgs, a) })
	if len(algs) == 0 {
		algs = []string{oidc.RS256}
	}
	return oidc.NewVerifier(cfg.Issuer, set.keys, &oidc.Config{ClientID: cfg.ClientID, Now: f.now, SupportedSigningAlgs: algs}), nil
}
