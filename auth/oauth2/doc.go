// Package oauth2 is the kit's OAuth2 / OIDC relying-party client.
// Companion to [security/jwtutil] (which VERIFIES inbound JWTs); this
// package ISSUES login redirects, EXCHANGES authorization codes, and
// persists browser sessions against an upstream OIDC issuer.
//
// Built on the ecosystem-standard [golang.org/x/oauth2] (for the
// OAuth2 dance and PKCE helpers) and
// [github.com/coreos/go-oidc/v3] (for issuer discovery, JWKS
// rotation, and ID-token signature + claims verification). The kit
// adds the session/state persistence interfaces, sensible cookie
// defaults, and the wrap-into-an-http.Handler ergonomics — but the
// security-critical primitives (signature checks, nonce/state CSRF
// guards, PKCE challenge/verifier pairing) come from the audited
// libraries, NOT the kit.
//
// [Client.SessionFromRequest] loads the session cookie established by
// the login/callback handlers; [WithTokenRefresh] refreshes its access
// token transparently. [NewClientCredentials] covers outbound
// client-credentials tokens, and [Client.OAuth2Config] is the escape
// hatch for other golang.org/x/oauth2 flows.
//
// # Use this package when
//
//   - Your service authenticates end-users via an external OIDC
//     provider (Auth0, Keycloak, Cognito, Okta, Google, ...).
//   - You want the kit to handle the PKCE state machine, OIDC
//     well-known discovery, ID-token verification, and browser
//     session cookies instead of hand-rolling them per service.
//
// # Do NOT use this package for
//
//   - Verifying JWTs issued by your own services. Use
//     [security/jwtutil] — it doesn't need OAuth2 client wiring.
//   - Machine-to-machine token issuance. Prefer service-to-service
//     mTLS (see [security/mtlsidentity]), or drive the credentials
//     grant via [Client.OAuth2Config] yourself.
//
// # Sibling packages
//
//   - [security/jwtutil]       — inbound JWT verification
//   - [httpx/middleware/auth]  — middleware that pulls the verified
//     identity off the request context
//   - [security/csrf]          — pairs with this package's session
//     handlers (CSRF guards POST callbacks)
//
// # Quick start
//
//	client, err := oauth2.NewClient(ctx, oauth2.Config{
//	    Issuer:       "https://login.example.com",
//	    ClientID:     "my-app",
//	    ClientSecret: secret.NewFromString(os.Getenv("OIDC_CLIENT_SECRET")),
//	    RedirectURL:  "https://my-app.example.com/oauth/callback",
//	    Scopes:       []string{"openid", "profile", "email"},
//	},
//	    oauth2.WithSessionStore(sessionStore),
//	    oauth2.WithStateStore(stateStore),
//	)
//	if err != nil { return err }
//	mux.Handle("/oauth/", client.Handlers())
//
// # PKCE is on by default
//
// Public clients (no client secret) MUST use PKCE per RFC 7636. The
// client uses PKCE for every flow unless WithoutPKCE() is passed for
// providers that don't support it.
//
// # State / nonce CSRF guards
//
// Every Login generates a fresh state and (for OIDC) nonce, persists
// them via the [StateStore], and binds the state to the browser with a
// cookie keyed by the state, so several tabs can sign in at once (at most
// five pending per browser). The callback exchanges a code only for a state
// bound to this browser, unexpired ([WithStateTTL]) and unused. Any other
// callback exchanges nothing: a browser with a valid session goes to the
// return URL, otherwise the sign-in restarts once with the same return URL.
// What still fails renders a translated page with "Sign in again"
// ([WithErrorPageBrand]), never error text.
//
// # Session security (all opt-in)
//
//   - [WithAbsoluteSessionLifetime] caps a session at CreatedAt+d; refresh
//     and the sliding TTL never extend it.
//   - [WithIdleTimeout] ends sessions without user-initiated requests;
//     mark polling and token renewal with [WithBackgroundRequest] or the
//     [DefaultBackgroundRequestHeader] header so they do not count.
//   - [WithRefreshOutageGrace] keeps sessions across a short provider
//     outage (transport/5xx refresh failures only).
//   - A store implementing [RefreshLocker] serializes token refresh across
//     replicas ([WithRefreshLockWait]).
//   - [WithBackChannelLogout] serves POST /oauth/backchannel-logout;
//     [Client.DeleteSessionsBySubject] and [WithSessionRevocationHook]
//     revoke a subject's sessions. Both need a [SessionIndex] store.
//   - Session.AMR / Session.AuthTime expose how the user authenticated;
//     GET /oauth/login?prompt=login&max_age=0 requests step-up and the
//     callback enforces max_age against auth_time.
package oauth2
