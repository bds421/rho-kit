// Package oauth2 is the kit's OAuth2 / OIDC relying-party client.
// Companion to [security/jwtutil] (which VERIFIES inbound JWTs); this
// package ISSUES login redirects, EXCHANGES authorization codes, and
// REFRESHES access/refresh tokens against an upstream OIDC issuer.
//
// # Use this package when
//
//   - Your service authenticates end-users via an external OIDC
//     provider (Auth0, Keycloak, Cognito, Okta, Google, ...).
//   - You want the kit to handle the PKCE state machine, OIDC
//     well-known discovery, ID-token verification, and refresh-token
//     dance instead of hand-rolling them per service.
//
// # Do NOT use this package for
//
//   - Verifying JWTs issued by your own services. Use
//     [security/jwtutil] — it doesn't need OAuth2 client wiring.
//   - Machine-to-machine token issuance. The OAuth2 client credentials
//     flow ships in this package, but the typical kit pattern for M2M
//     is service-to-service mTLS (see [security/mtlsidentity]) since
//     mTLS doesn't need a token at all.
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
