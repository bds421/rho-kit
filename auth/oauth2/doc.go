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
// Token refresh and client-credentials (M2M) flows are NOT provided as
// first-class APIs. Use [Client.OAuth2Config] as the escape hatch to
// build a refresh TokenSource or credentials grant via
// golang.org/x/oauth2. [Client.SessionFromRequest] loads the session
// cookie established by the login/callback handlers.
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
// them via the [StateStore], and re-validates on Callback. Stale state
// entries are pruned by the store's TTL.
package oauth2
