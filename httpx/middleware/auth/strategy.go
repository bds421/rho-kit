package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/security/v2/apikey"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
	"github.com/bds421/rho-kit/security/v2/session"
)

// Identity is the result of a successful authentication. It is the
// strategy-agnostic shape the middleware stamps onto the request
// context so downstream RBAC and scope checks (RequirePermission,
// RequireScope, Permissions, Scopes) work regardless of which
// strategy produced the identity.
//
// Subject is who the request is for (visibility, RLS). When non-empty
// it MUST satisfy [jwtutil.IsUUID]. Actor is who performed the action
// (audit, attribution) and MAY be non-UUID (API key id, client id).
// Machine credentials may leave Subject empty when Tenant is set; see
// [Identity.Normalize] and [IsMachineKind].
//
// UserID is a deprecated alias for Subject; [Identity.Normalize] keeps
// both in sync for one release cycle.
type Identity struct {
	// Subject is the UUID-shaped visibility subject. Empty for unbound
	// machine credentials when Tenant is set.
	Subject string
	// Actor is the attribution id (key id, client id, or user UUID).
	Actor string
	// ActorKind classifies Actor for policy branches.
	ActorKind ActorKind
	// UserID is deprecated: use Subject. Normalized to match Subject.
	UserID string
	// Tenant is the resolved tenant id for multi-tenant services.
	Tenant string
	// Role is the coarse RBAC role (member, admin, …).
	Role string
	// Permissions is the unordered list of permission strings
	// granted to this identity (e.g. "billing:read", "admin:*").
	Permissions []string
	// ScopeList is the canonical scope token list for machine credentials.
	// Scopes mirrors ScopeList as a space-separated string for OAuth wire
	// compat and [Scopes] context readers — both are kept in sync by
	// [Identity.Normalize].
	ScopeList []string
	Scopes    string
	// Trusted, when true, stamps the trusted-S2S marker that lets
	// downstream RBAC / scope middleware accept the request
	// without an explicit permissions claim. Mirror of the existing
	// RequireS2SAuth marker — use sparingly.
	Trusted bool
}

// Authenticator turns an HTTP request into a verified [Identity] or
// an authentication error. Implementations MUST NOT mutate the
// request.
//
// Return [ErrUnauthenticated] when the request carries no
// credentials of the type this strategy knows about — that lets
// [Chain] try the next strategy. Return [ErrInvalidCredentials]
// (or a wrapped variant) when credentials are present but invalid;
// Chain stops on that error so a forged token cannot trigger a
// fall-through to a weaker strategy.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// AuthenticatorFunc adapts a function into an [Authenticator].
type AuthenticatorFunc func(r *http.Request) (Identity, error)

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(r *http.Request) (Identity, error) {
	return f(r)
}

// ErrUnauthenticated indicates the request carries no credentials
// of the type this strategy handles. In [Chain], it triggers a
// fall-through to the next strategy.
var ErrUnauthenticated = errors.New("middleware/auth: unauthenticated")

// ErrInvalidCredentials indicates credentials were present but
// invalid. In [Chain], it terminates the chain — a forged Bearer
// must not silently fall through to API-key.
var ErrInvalidCredentials = errors.New("middleware/auth: invalid credentials")

// Strategy returns chain-shape middleware that authenticates every
// request via a. On success, the identity is stamped onto the
// request context using the same keys [JWT] uses, so downstream
// [RequirePermission], [RequireScope], and [UserID] / [Permissions]
// / [Scopes] readers work unchanged.
//
// Panics if a is nil.
//
// This is the generic entry point. The existing [JWT] and
// [RequireS2SAuth] functions remain for the JWT and JWT+mTLS
// flows; Strategy lets services compose API-key, PASETO, or any
// custom verifier without forking the middleware.
func Strategy(a Authenticator) func(http.Handler) http.Handler {
	if a == nil {
		panic("middleware/auth: Strategy requires a non-nil Authenticator")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := safeAuthenticate(a, r)
			if err != nil {
				if errors.Is(err, ErrUnauthenticated) {
					httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				// Treat anything else (including a wrapped
				// ErrInvalidCredentials, a strategy-internal
				// error, or a panic recovered by
				// safeAuthenticate) as bad credentials. The
				// strategy is responsible for logging the
				// underlying cause; the wire response is
				// deliberately opaque.
				httpx.WriteError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			if !identityValid(id) {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			ctx := stampIdentity(r.Context(), id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// stampIdentity writes identity fields onto ctx using the same
// keys the JWT path uses. Centralised so a future change to the
// context-key contract has a single touch point.
func stampIdentity(ctx context.Context, id Identity) context.Context {
	id = id.Normalize()
	if id.Subject != "" {
		ctx = subjectKey.Set(ctx, authSubject(id.Subject))
		ctx = userIDKey.Set(ctx, authUserID(id.Subject))
	}
	if id.Actor != "" {
		ctx = actorKey.Set(ctx, authActor(id.Actor))
	}
	if id.ActorKind != "" {
		ctx = actorKindKey.Set(ctx, id.ActorKind)
	}
	if id.Tenant != "" {
		ctx = tenantKey.Set(ctx, authTenant(id.Tenant))
	}
	if id.Role != "" {
		ctx = roleKey.Set(ctx, authRole(id.Role))
	}
	perms := slices.Clone(id.Permissions)
	ctx = permissionsKey.Set(ctx, perms)
	ps := make(permissionSet, len(perms))
	for _, p := range perms {
		ps[p] = struct{}{}
	}
	ctx = permSetKey.Set(ctx, ps)
	ctx = scopesKey.Set(ctx, authScopes(id.Scopes))
	if id.Trusted {
		ctx = trustedS2SKey.Set(ctx, trustedS2SMarker{})
	}
	return ctx
}

var errStrategyPanicked = errors.New("middleware/auth: strategy panicked")

func safeAuthenticate(a Authenticator, r *http.Request) (id Identity, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			httpx.Logger(r.Context(), slog.Default()).Error("middleware/auth: strategy panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			id = Identity{}
			err = errStrategyPanicked
		}
	}()
	return a.Authenticate(r)
}

// Chain returns an [Authenticator] that tries each strategy in
// order. The first strategy returning a non-[ErrUnauthenticated]
// result wins — success ends the chain; [ErrInvalidCredentials]
// also ends the chain so a forged token cannot fall through to a
// weaker strategy.
//
// Panics if strategies is empty or contains a nil element.
func Chain(strategies ...Authenticator) Authenticator {
	if len(strategies) == 0 {
		panic("middleware/auth: Chain requires at least one strategy")
	}
	for _, s := range strategies {
		if s == nil {
			panic("middleware/auth: Chain strategies must not be nil")
		}
	}
	cloned := slices.Clone(strategies)
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		var lastErr = ErrUnauthenticated
		for _, s := range cloned {
			id, err := s.Authenticate(r)
			if err == nil {
				return id, nil
			}
			if errors.Is(err, ErrUnauthenticated) {
				lastErr = err
				continue
			}
			return Identity{}, err
		}
		return Identity{}, lastErr
	})
}

// NewJWTAuthenticator wraps the existing [jwtutil.Provider] as an
// [Authenticator]. Use with [Strategy] (or [Chain]) when a route
// wants to combine JWT with another auth method.
//
// Behaviour parity with [JWT]:
//   - Bearer is required; absent header → [ErrUnauthenticated] so
//     a chain can try the next strategy.
//   - Invalid Bearer header (multiple values, malformed token,
//     unknown scheme) → [ErrInvalidCredentials].
//   - JWKS handshake failures and signature errors are NOT
//     distinguishable on the wire — both surface as
//     ErrInvalidCredentials.
//
// Panics if provider is nil.
func NewJWTAuthenticator(provider *jwtutil.Provider, opts ...JWTOption) Authenticator {
	if provider == nil {
		panic("middleware/auth: NewJWTAuthenticator requires a non-nil provider")
	}
	jwtCfg := buildJWTIdentityConfig(opts...)
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		token, status := parseBearerToken(r)
		switch status {
		case bearerTokenAbsent:
			return Identity{}, ErrUnauthenticated
		case bearerTokenInvalid:
			return Identity{}, ErrInvalidCredentials
		}
		claims, err := provider.VerifyContext(r.Context(), token, time.Now())
		if err != nil {
			return Identity{}, ErrInvalidCredentials
		}
		id, ok := identityFromJWTClaims(claims, jwtCfg)
		if !ok {
			return Identity{}, ErrInvalidCredentials
		}
		return id, nil
	})
}

// APIKeyVerifier turns an opaque API key into a verified
// [Identity]. The verifier is responsible for the secret
// comparison (constant-time) and for mapping the key's owner to a
// UUID-shaped subject.
type APIKeyVerifier interface {
	VerifyAPIKey(ctx context.Context, key string) (Identity, error)
}

// APIKeyVerifierFunc adapts a function into an [APIKeyVerifier].
type APIKeyVerifierFunc func(ctx context.Context, key string) (Identity, error)

// VerifyAPIKey calls f.
func (f APIKeyVerifierFunc) VerifyAPIKey(ctx context.Context, key string) (Identity, error) {
	return f(ctx, key)
}

// NewAPIKeyAuthenticator returns an [Authenticator] that reads
// the API key from headerName (e.g. "X-API-Key") and verifies it
// via v. Multiple header values or invalid header characters are
// rejected as [ErrInvalidCredentials]; absent header returns
// [ErrUnauthenticated] so a [Chain] can fall through.
//
// Any error returned by v.VerifyAPIKey — including infrastructure
// failures such as a backend outage, a cancelled context, or a
// timeout — is flattened to [ErrInvalidCredentials] so the wire
// response stays opaque and a [Chain] stops rather than pivoting to
// a weaker strategy. The underlying cause is preserved in the
// returned error's message (not as a wrapped sentinel) so operators
// can distinguish an outage from a forged key in logs; callers that
// need the precise category should inspect the cause themselves.
//
// Panics if v is nil, headerName is empty, or headerName fails
// httpguts.ValidHeaderFieldName.
func NewAPIKeyAuthenticator(headerName string, v APIKeyVerifier) Authenticator {
	if v == nil {
		panic("middleware/auth: NewAPIKeyAuthenticator requires a non-nil verifier")
	}
	if headerName == "" {
		panic("middleware/auth: NewAPIKeyAuthenticator requires a non-empty header name")
	}
	if !httpguts.ValidHeaderFieldName(headerName) {
		panic("middleware/auth: NewAPIKeyAuthenticator header name contains invalid characters")
	}
	canonical := http.CanonicalHeaderKey(headerName)
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		values := r.Header.Values(canonical)
		switch len(values) {
		case 0:
			return Identity{}, ErrUnauthenticated
		case 1:
		default:
			return Identity{}, ErrInvalidCredentials
		}
		key := values[0]
		// Cap the key length before invoking the verifier. Verifiers
		// typically hash/compare the key, so an attacker-sized header
		// value would cost CPU per request. Mirror the bearer-token cap
		// (maxBearerTokenLen) so both credential paths in this package
		// are hardened consistently.
		if key == "" || len(key) > maxBearerTokenLen || strings.TrimSpace(key) != key || !httpguts.ValidHeaderFieldValue(key) {
			return Identity{}, ErrInvalidCredentials
		}
		id, err := v.VerifyAPIKey(r.Context(), key)
		if err != nil {
			httpx.Logger(r.Context(), slog.Default()).Error("middleware/auth: api key verification failed",
				redact.Error(err),
			)
			return Identity{}, fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
		}
		return id, nil
	})
}

// ChainMiddleware returns HTTP middleware that tries each [Authenticator] in
// order via [Chain] and stamps the winning [Identity] on the request context.
//
// Recommended Bearer order when combining session tokens, OAuth access tokens,
// scoped API keys, and JWTs:
//
//  1. [NewOAuthAccessSessionAuthenticator] — optional; payload.sig OAuth access
//     (one dot). Register before session when used.
//  2. [NewSessionAuthenticator] — session wire format is <payload>.<mac>
//     (exactly one dot). Non-session shapes fall through via
//     [ErrUnauthenticated]; each machine strategy matches its own prefix.
//  3. [NewOAuthAccessBearerAuthenticator] — optional; <prefix>_<lookup>_<secret>.
//  4. [NewScopedKeyBearerAuthenticator] — scoped keys (prefixed).
//  5. [NewJWTAuthenticator] — JWTs (multiple dots); after session.
//  6. [NewAPIKeyAuthenticator] — X-API-Key header (not Bearer).
//
// ErrUnauthenticated lets the chain fall through; ErrInvalidCredentials stops it.
func ChainMiddleware(strategies ...Authenticator) func(http.Handler) http.Handler {
	return Strategy(Chain(strategies...))
}

// NewSessionAuthenticator authenticates Authorization: Bearer session tokens
// via [session.Validator]. Tokens without a Bearer header return
// [ErrUnauthenticated] so a [Chain] can fall through to API-key strategies.
func NewSessionAuthenticator(v session.Validator) Authenticator {
	if v.Signer == nil {
		panic("middleware/auth: NewSessionAuthenticator requires a non-nil Signer")
	}
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		token, status := parseBearerToken(r)
		switch status {
		case bearerTokenAbsent:
			return Identity{}, ErrUnauthenticated
		case bearerTokenInvalid:
			return Identity{}, ErrInvalidCredentials
		}
		if !looksLikeSessionToken(token) {
			return Identity{}, ErrUnauthenticated
		}
		claims, err := v.Validate(r.Context(), token, time.Now())
		if err != nil {
			httpx.Logger(r.Context(), slog.Default()).Error("middleware/auth: session token validation failed",
				redact.Error(err),
			)
			return Identity{}, ErrInvalidCredentials
		}
		return Identity{
			Subject:   claims.UserID,
			Actor:     claims.UserID,
			ActorKind: ActorUser,
			Tenant:    claims.Tenant,
			Role:      claims.Role,
		}.Normalize(), nil
	})
}

// NewScopedKeyBearerAuthenticator authenticates Bearer tokens prefixed with
// the resolver's wire prefix. Non-matching Bearer tokens
// return [ErrUnauthenticated] for chain fall-through.
func NewScopedKeyBearerAuthenticator(resolver *apikey.ScopedResolver) Authenticator {
	if resolver == nil {
		panic("middleware/auth: NewScopedKeyBearerAuthenticator requires a non-nil resolver")
	}
	prefix := resolver.TokenPrefix()
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		token, status := parseBearerToken(r)
		switch status {
		case bearerTokenAbsent:
			return Identity{}, ErrUnauthenticated
		case bearerTokenInvalid:
			return Identity{}, ErrInvalidCredentials
		}
		if !strings.HasPrefix(token, prefix+"_") {
			return Identity{}, ErrUnauthenticated
		}
		principal, err := resolver.Resolve(r.Context(), token)
		if err != nil {
			httpx.Logger(r.Context(), slog.Default()).Error("middleware/auth: scoped key verification failed",
				redact.Error(err),
			)
			return Identity{}, fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
		}
		return IdentityFromScopedKey(principal), nil
	})
}

// OAuthAccessVerifier validates stateless OAuth access tokens (client
// credentials) and returns a fully populated [Identity].
//
// Implementations MUST return [ErrUnauthenticated] when the Bearer token is
// present but not an OAuth access token of the shape this verifier handles,
// so a [Chain] can fall through to the next strategy. Return
// [ErrInvalidCredentials] (or a wrapped variant) only when the token is
// recognized as OAuth access but cryptographically invalid.
type OAuthAccessVerifier interface {
	VerifyOAuthAccess(ctx context.Context, token string) (Identity, error)
}

// OAuthAccessVerifierFunc adapts a function into an [OAuthAccessVerifier].
type OAuthAccessVerifierFunc func(ctx context.Context, token string) (Identity, error)

// VerifyOAuthAccess calls f.
func (f OAuthAccessVerifierFunc) VerifyOAuthAccess(ctx context.Context, token string) (Identity, error) {
	return f(ctx, token)
}

// NewOAuthAccessSessionAuthenticator verifies payload.sig OAuth access tokens
// (<payload>.<mac>, exactly one dot). Register BEFORE [NewSessionAuthenticator]
// in the chain — both share the one-dot shape. The verifier must return
// [ErrUnauthenticated] for non-OAuth session-shaped tokens.
//
// Panics if verifier is nil.
func NewOAuthAccessSessionAuthenticator(v OAuthAccessVerifier) Authenticator {
	if v == nil {
		panic("middleware/auth: NewOAuthAccessSessionAuthenticator requires a non-nil verifier")
	}
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		token, status := parseBearerToken(r)
		switch status {
		case bearerTokenAbsent:
			return Identity{}, ErrUnauthenticated
		case bearerTokenInvalid:
			return Identity{}, ErrInvalidCredentials
		}
		if !looksLikeSessionToken(token) {
			return Identity{}, ErrUnauthenticated
		}
		return verifyOAuthAccess(r, v, token)
	})
}

// NewOAuthAccessBearerAuthenticator authenticates prefix-based OAuth access
// tokens (<prefix>_<lookup>_<secret>). Non-matching tokens return
// [ErrUnauthenticated]. Session automatically skips this wire shape.
//
// Panics if verifier is nil or tokenPrefix is empty.
func NewOAuthAccessBearerAuthenticator(v OAuthAccessVerifier, tokenPrefix string) Authenticator {
	if v == nil {
		panic("middleware/auth: NewOAuthAccessBearerAuthenticator requires a non-nil verifier")
	}
	if tokenPrefix == "" {
		panic("middleware/auth: NewOAuthAccessBearerAuthenticator requires a non-empty token prefix")
	}
	return AuthenticatorFunc(func(r *http.Request) (Identity, error) {
		token, status := parseBearerToken(r)
		switch status {
		case bearerTokenAbsent:
			return Identity{}, ErrUnauthenticated
		case bearerTokenInvalid:
			return Identity{}, ErrInvalidCredentials
		}
		if !strings.HasPrefix(token, tokenPrefix+"_") {
			return Identity{}, ErrUnauthenticated
		}
		return verifyOAuthAccess(r, v, token)
	})
}

func verifyOAuthAccess(r *http.Request, v OAuthAccessVerifier, token string) (Identity, error) {
	id, err := v.VerifyOAuthAccess(r.Context(), token)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return Identity{}, ErrUnauthenticated
		}
		httpx.Logger(r.Context(), slog.Default()).Error("middleware/auth: oauth access verification failed",
			redact.Error(err),
		)
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
	}
	return id.Normalize(), nil
}

func looksLikeSessionToken(token string) bool {
	if strings.Count(token, ".") != 1 {
		return false
	}
	parts := strings.Split(token, ".")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}


