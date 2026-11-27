package oauth2

import (
	"context"
	"fmt"

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
// The jti is recorded before VerifyLogoutToken returns, so a second
// delivery fails with [ErrLogoutTokenReplayed] even when the caller failed
// to end the sessions of the first. Other failures are
// [ErrLogoutTokenInvalid] and [ErrIssuerDiscovery], or the replay cache's
// error. The caller answers 200 on success and 400 with
// {"error":"invalid_request"} for an invalid or replayed token.
func (f *Flow) VerifyLogoutToken(ctx context.Context, cfg Config, raw string) (LogoutToken, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || raw == "" {
		return LogoutToken{}, ErrLogoutTokenInvalid
	}
	provider, err := f.provider(ctx, cfg.Issuer)
	if err != nil {
		return LogoutToken{}, err
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, Now: f.now})
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
	return LogoutToken{Issuer: tok.issuer, Subject: tok.subject, SID: tok.sid}, nil
}
