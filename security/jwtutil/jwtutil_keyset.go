package jwtutil

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// KeySet holds a JWKS key set for JWT signature verification.
//
// ExpectedIssuer / ExpectedAudience are the construction-time policy knobs
// for the low-level [KeySet.Verify] path. Prefer the immutable copy helpers
// [KeySet.WithExpectedIssuer] / [KeySet.WithExpectedAudience] (or a
// [Provider], which carries policy on the Provider itself) over mutating
// fields on a shared KeySet.
//
// Concurrency contract: the first call to [KeySet.Verify] freezes the
// issuer/audience policy into an internal snapshot. Subsequent Verifys
// use that frozen snapshot; later assignments to the exported fields are
// ignored. Set policy once before the first Verify (or use With* to get
// a fresh KeySet) — toggling fields under concurrent Verifys was always
// a data race; freeze makes the post-first-Verify half of that race a
// no-op rather than a silent policy flip.
type KeySet struct {
	set jwk.Set
	// ExpectedIssuer, when non-empty, is validated against the "iss" claim.
	// Captured on the first [KeySet.Verify]; later mutations are ignored.
	// Prefer [KeySet.WithExpectedIssuer] for a frozen copy.
	ExpectedIssuer string
	// ExpectedAudience, when non-empty, is validated against the "aud" claim.
	// REQUIRED for multi-service deployments — without it, a token issued for
	// service A is silently valid at service B as long as both trust the same
	// signer. Standard JWT confused-deputy mitigation (RFC 7519 §4.1.3).
	// Captured on the first [KeySet.Verify]; later mutations are ignored.
	// Prefer [KeySet.WithExpectedAudience] for a frozen copy.
	ExpectedAudience string

	policyOnce     sync.Once
	frozenIssuer   string
	frozenAudience string
}

// WithExpectedIssuer returns a shallow copy of ks with ExpectedIssuer set.
// The copy shares the underlying jwk.Set (immutable through its public API)
// and has an independent freeze state, so concurrent Verifys on the
// original are unaffected. Prefer this over mutating ExpectedIssuer on a
// KeySet that may already be shared.
func (ks *KeySet) WithExpectedIssuer(issuer string) *KeySet {
	if ks == nil {
		return &KeySet{ExpectedIssuer: issuer}
	}
	return &KeySet{
		set:              ks.set,
		ExpectedIssuer:   issuer,
		ExpectedAudience: ks.ExpectedAudience,
	}
}

// WithExpectedAudience returns a shallow copy of ks with ExpectedAudience
// set. See [KeySet.WithExpectedIssuer] for the copy/freeze contract.
func (ks *KeySet) WithExpectedAudience(audience string) *KeySet {
	if ks == nil {
		return &KeySet{ExpectedAudience: audience}
	}
	return &KeySet{
		set:              ks.set,
		ExpectedIssuer:   ks.ExpectedIssuer,
		ExpectedAudience: audience,
	}
}

// frozenPolicy returns the issuer/audience captured on the first Verify.
// Safe for concurrent use; the sync.Once publishes the snapshot before
// any concurrent reader observes it.
func (ks *KeySet) frozenPolicy() (issuer, audience string) {
	ks.policyOnce.Do(func() {
		ks.frozenIssuer = ks.ExpectedIssuer
		ks.frozenAudience = ks.ExpectedAudience
	})
	return ks.frozenIssuer, ks.frozenAudience
}

// ErrInvalidKeySet is returned when verification is attempted with a
// KeySet that was not constructed by ParseKeySet or ParseKeySetFromPEM.
var ErrInvalidKeySet = errors.New("jwtutil: key set is not initialized")

var (
	errMalformedPermissionsClaim = errors.New("malformed permissions claim")
	errMalformedScopesClaim      = errors.New("malformed scopes claim")
)

// ParseKeySet parses a JWKS JSON document into a KeySet.
//
// Symmetric (HMAC / kty=oct) keys are rejected to prevent the classic
// alg-confusion attack, where a token signed with HS256 is accepted by a
// verifier that holds an EC/RSA public key — by treating the public key
// bytes as the HMAC secret. Trusted JWKS endpoints do not publish symmetric
// keys; if you have a legitimate use for shared
// secrets, pass them outside the JWKS surface.
func ParseKeySet(data []byte) (*KeySet, error) {
	set, err := jwk.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	filtered := jwk.NewSet()
	for i := 0; i < set.Len(); i++ {
		key, ok := set.Key(i)
		if !ok {
			continue
		}
		key, usable, err := verificationKey(key)
		if err != nil {
			return nil, err
		}
		if !usable {
			// Skip silently rather than returning an error: a JWKS may
			// legitimately mix algorithms over time, and rejecting the
			// whole set on a single non-verification key would break liveness.
			continue
		}
		if err := filtered.AddKey(key); err != nil {
			return nil, fmt.Errorf("filter jwks: %w", err)
		}
	}
	if filtered.Len() == 0 {
		return nil, errors.New("jwks contains no usable asymmetric keys")
	}
	return &KeySet{set: filtered}, nil
}

// verificationKey normalises a JWKS entry into a public signature-verification
// key. Non-signature or non-verification keys are ignored so a mixed JWKS
// cannot make this verifier use encryption keys or retain private material.
func verificationKey(k jwk.Key) (jwk.Key, bool, error) {
	if isSymmetricKey(k) || !allowsSignatureUse(k) || !allowsVerifyOperation(k) || !allowsSignatureAlgorithm(k) {
		return nil, false, nil
	}
	publicKey, err := k.PublicKey()
	if err != nil {
		return nil, false, fmt.Errorf("filter jwks public key: %w", err)
	}
	return publicKey, true, nil
}

// isSymmetricKey reports whether k is an HMAC-style (kty=oct) key. Such
// keys cannot safely be combined with [jws.WithInferAlgorithmFromKey],
// because the inferred algorithm is HS*, which lets an attacker forge
// tokens against any other key in the set whose public bytes can be
// borrowed as the HMAC secret.
func isSymmetricKey(k jwk.Key) bool {
	return k.KeyType() == jwa.OctetSeq()
}

func allowsSignatureUse(k jwk.Key) bool {
	usage, ok := k.KeyUsage()
	return !ok || usage == "" || usage == jwk.ForSignature.String()
}

func allowsVerifyOperation(k jwk.Key) bool {
	ops, ok := k.KeyOps()
	if !ok {
		return true
	}
	for _, op := range ops {
		if op == jwk.KeyOpVerify {
			return true
		}
	}
	return false
}

func allowsSignatureAlgorithm(k jwk.Key) bool {
	alg, ok := k.Algorithm()
	if !ok {
		return true
	}
	_, ok = jwa.LookupSignatureAlgorithm(alg.String())
	return ok
}

// ParseKeySetFromPEM parses a PEM-encoded public key into a KeySet with a
// single key using the given key ID.
//
// Private PEM material is reduced to its public part before retention so a
// misconfigured signer private key mounted as the verifier input cannot leave
// live signing material in process memory.
func ParseKeySetFromPEM(pemData []byte, kid string) (*KeySet, error) {
	key, err := jwk.ParseKey(pemData, jwk.WithPEM(true))
	if err != nil {
		return nil, fmt.Errorf("parse PEM key: %w", err)
	}
	publicKey, err := key.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("parse PEM public key: %w", err)
	}
	key = publicKey
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
// It validates the signature, expiration, and not-before claims. Tokens
// without an `exp` claim are rejected — non-expiring bearer tokens are
// indistinguishable from a stolen credential and have no place in this kit.
//
// Issuer and audience are validated against the policy frozen on the
// first Verify call (seeded from [KeySet.ExpectedIssuer] /
// [KeySet.ExpectedAudience], or set via [KeySet.WithExpectedIssuer] /
// [KeySet.WithExpectedAudience]). Empty values skip the corresponding
// check (signature-only verification). This is a deliberate low-level
// escape hatch; production services MUST set both (or use [Provider] /
// [NewProvider], which refuse to construct without an explicit
// issuer/audience policy or AllowAny opt-out). Leaving both empty
// accepts tokens minted for any sibling audience that trusts the same
// signer (RFC 7519 confused-deputy). Prefer [Provider.Verify] for
// service auth.
//
// A future major version may fail closed when ExpectedIssuer /
// ExpectedAudience are unset; see V3_BREAKING_PROPOSALS.md.
func (ks *KeySet) Verify(tokenString string, now time.Time) (*Claims, error) {
	if ks == nil {
		return nil, ErrInvalidKeySet
	}
	iss, aud := ks.frozenPolicy()
	return verifyToken(ks.set, tokenString, now, iss, aud, defaultStringClaims)
}

// verifyTimingFloor is the minimum wall-clock duration verifyToken
// holds before returning. A valid ES256 token verify takes ~50 µs on
// modern hardware; rejecting fast (wrong kid, malformed token) used to
// return in ~4 µs, which creates a kid-existence / token-shape side
// channel: a hostile probe can distinguish "no matching key" from
// "key matched, signature failed" purely by timing. The floor closes
// the gap by sleeping any fast-path return until verifyTimingFloor
// has elapsed.
//
// The floor only adds latency to paths that beat it (rejections). A
// real verify exceeds the floor naturally. Tests can shrink the floor
// via the verifyTimingFloorOverride package var (test-only seam) to
// keep benchmark wall-clock honest.
const verifyTimingFloor = 50 * time.Microsecond

// verifyTimingFloorOverride is a test-only seam. Zero means "use
// verifyTimingFloor"; production callers never assign to it.
var verifyTimingFloorOverride time.Duration

func currentVerifyFloor() time.Duration {
	if d := verifyTimingFloorOverride; d > 0 {
		return d
	}
	return verifyTimingFloor
}

// verifyToken is the lower-level verification primitive. It does not read
// any mutable policy state — issuer and audience are passed in by the
// caller. Provider.Verify calls this with its own stored policy so two
// providers can share one *KeySet without racing on or overwriting each
// other's iss/aud fields (R4 fix).
//
// Wall-clock floor: every return from this function is held until at
// least verifyTimingFloor (default 50 µs) has elapsed since entry. This
// removes the kid-existence side channel described above
// verifyTimingFloor.
var defaultStringClaims = []string{"client_id", "azp", "act"}

func verifyToken(set jwk.Set, tokenString string, now time.Time, expectedIssuer, expectedAudience string, stringClaimNames []string) (*Claims, error) {
	start := time.Now()
	defer func() {
		floor := currentVerifyFloor()
		elapsed := time.Since(start)
		if elapsed < floor {
			time.Sleep(floor - elapsed)
		}
	}()
	if set == nil || set.Len() == 0 {
		return nil, ErrInvalidKeySet
	}
	// Cap token length before handing to jwt.Parse so a hostile caller
	// of Provider.Verify cannot force a 100 MB parse allocation. The
	// httpx auth middleware enforces an 8 KiB bearer cap upstream, but
	// Provider.Verify is reachable from non-HTTP callers (grpc, MCP,
	// background workers). 16 KiB is comfortably above any realistic
	// JWT with custom claims while a hard stop short of a parse-cost DoS.
	if len(tokenString) > maxJWTLen {
		return nil, errors.New("jwtutil: token exceeds maximum length")
	}
	now = verificationTime(now)
	parseOpts := []jwt.ParseOption{
		jwt.WithKeySet(set, jws.WithInferAlgorithmFromKey(true)),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(clockSkew),
		jwt.WithClock(jwt.ClockFunc(func() time.Time { return now })),
		jwt.WithRequiredClaim(jwt.ExpirationKey),
	}
	if expectedIssuer != "" {
		parseOpts = append(parseOpts, jwt.WithIssuer(expectedIssuer))
	}
	if expectedAudience != "" {
		parseOpts = append(parseOpts, jwt.WithAudience(expectedAudience))
	}
	tok, err := jwt.Parse([]byte(tokenString), parseOpts...)
	if err != nil {
		return nil, err
	}

	// Defence-in-depth header check: jwx accepts any `typ` header.
	// RFC 9068 §4 and JWT BCP recommend pinning typ to "JWT" or
	// "at+jwt" so a federated issuer that mints both id-tokens and
	// access-tokens with the same key cannot cross-substitute one for
	// the other. Empty typ stays accepted — many issuers omit it on
	// vanilla JWTs.
	if err := requireExpectedJWTType(tokenString); err != nil {
		return nil, err
	}

	exp, hasExp := tok.Expiration()
	if !hasExp || exp.IsZero() {
		// Belt-and-braces: WithRequiredClaim already enforces this, but
		// re-check after parse so a future jwx upgrade that loosens the
		// validator cannot silently re-introduce non-expiring tokens.
		return nil, errors.New("missing exp claim")
	}

	sub, _ := tok.Subject()
	if sub == "" {
		return nil, errors.New("missing sub claim")
	}

	iss, _ := tok.Issuer()
	claims := &Claims{
		Subject:   sub,
		Issuer:    iss,
		ExpiresAt: exp.Unix(),
	}
	if id, ok := tok.JwtID(); ok {
		claims.ID = id
	}
	if iat, ok := tok.IssuedAt(); ok {
		claims.IssuedAt = iat.Unix()
	}
	if nbf, ok := tok.NotBefore(); ok {
		claims.NotBefore = nbf.Unix()
	}

	var perms []any
	switch err := tok.Get("permissions", &perms); {
	case err == nil:
		converted, convErr := toStringSlice(perms)
		if convErr != nil {
			return nil, errMalformedPermissionsClaim
		}
		claims.Permissions = converted
	case errors.Is(err, jwt.ClaimNotFoundError()):
		// Older issuers and role-less tokens omit permissions entirely.
		// That is a valid token; downstream RBAC fails closed on the empty set.
	default:
		// Claim is present but not assignable to []any — e.g. a bare string
		// or number. Treating that as "no permissions" lets a buggy issuer
		// silently downgrade an authenticated request to no privileges; the
		// confused-deputy variant of the empty-set problem. Reject instead.
		slog.Warn("jwt: permissions claim malformed; rejecting token",
			"claim", "permissions",
			redact.ErrorKey("err", errMalformedPermissionsClaim),
		)
		return nil, errMalformedPermissionsClaim
	}
	var scopes string
	switch err := tok.Get("scopes", &scopes); {
	case err == nil:
		claims.Scopes = scopes
	case errors.Is(err, jwt.ClaimNotFoundError()):
		// Optional claim; empty string is the correct zero value.
	default:
		slog.Warn("jwt: scopes claim malformed; rejecting token",
			"claim", "scopes",
			redact.ErrorKey("err", errMalformedScopesClaim),
		)
		return nil, errMalformedScopesClaim
	}

	populateStringClaims(tok, claims, stringClaimNames)
	return claims, nil
}

// mergeStringClaimNames returns the deduplicated claim-name list used by
// populateStringClaims. defaultStringClaims are always included; empty
// entries and duplicates in extra are dropped.
func mergeStringClaimNames(extra []string) []string {
	seen := make(map[string]struct{}, len(defaultStringClaims)+len(extra))
	out := make([]string, 0, len(defaultStringClaims)+len(extra))
	for _, name := range defaultStringClaims {
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range extra {
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func populateStringClaims(tok jwt.Token, c *Claims, names []string) {
	for _, name := range names {
		var s string
		if err := tok.Get(name, &s); err != nil || s == "" {
			continue
		}
		if c.stringClaims == nil {
			c.stringClaims = make(map[string]string)
		}
		c.stringClaims[name] = s
	}
}

func verificationTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}

// toStringSlice converts a JSON-decoded value to []string. Returns an error
// when v is the wrong shape (e.g. []any{123}) so callers can distinguish a
// misshaped claim from an empty-but-well-formed one.
func toStringSlice(v any) ([]string, error) {
	switch val := v.(type) {
	case []string:
		return val, nil
	case []any:
		out := make([]string, 0, len(val))
		for i, item := range val {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want string", i, item)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("value is %T, want []string", v)
	}
}
