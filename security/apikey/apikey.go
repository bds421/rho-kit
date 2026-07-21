package apikey

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/randstr"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// Sentinel errors returned by [Key.Verify] and [Parse]. Callers (transport
// middleware) translate these into protocol-specific responses; the core
// stays HTTP-agnostic. Match with [errors.Is].
var (
	// ErrMalformedToken means the presented string is not a well-formed
	// API key token (wrong segment count, empty parts, or bad prefix).
	ErrMalformedToken = errors.New("apikey: malformed token")
	// ErrInvalidSecret means the token's secret does not match the stored hash.
	ErrInvalidSecret = errors.New("apikey: secret does not match")
	// ErrExpired means the key's ExpiresAt is in the past.
	ErrExpired = errors.New("apikey: key expired")
	// ErrRevoked means the key has been revoked.
	ErrRevoked = errors.New("apikey: key revoked")
)

// Kind distinguishes ordinary access keys from privileged management keys.
// A root key may mint and revoke other keys; an api key only authenticates
// API calls. Baking the distinction in from the start keeps a future
// self-serve key-management surface additive (no schema break).
type Kind string

const (
	// KindAPI is an ordinary access key used to authenticate API requests.
	KindAPI Kind = "api"
	// KindRoot is a privileged key permitted to manage other keys.
	KindRoot Kind = "root"
)

// Valid reports whether k is a recognised kind.
func (k Kind) Valid() bool { return k == KindAPI || k == KindRoot }

const (
	// DefaultPrefix is the leak-scanner-friendly token prefix.
	DefaultPrefix = "rho"
	// secretLength is the number of AlphaNum runes in the secret segment.
	// 43 runes of base-62 ≈ 256 bits of entropy.
	secretLength = 43
	// prefixIDLen is how many leading id characters are surfaced (with the
	// token prefix) as the human-readable [Key.Prefix] shown in dashboards.
	prefixIDLen = 8
)

// Key is the persisted, non-secret record of an issued API key. It never
// holds the plaintext secret — only its SHA-256 hash. Copies are safe to
// log: Hash is not reversible and no field is sensitive.
type Key struct {
	// ID is the public lookup identifier embedded in the token (UUID v7).
	ID string
	// Prefix is the safe-to-display token prefix, e.g. "rho_018f0a3c",
	// used to identify a key in a dashboard without revealing the secret.
	Prefix string
	// Hash is SHA-256 of the secret segment.
	Hash [32]byte
	// Kind is the key's privilege level.
	Kind Kind
	// Scopes are opaque scope strings validated against the authz registry
	// at a higher layer (kept as strings here to avoid an authz dependency).
	Scopes []string
	// Owner identifies the principal/tenant the key belongs to.
	Owner string
	// ExpiresAt is when the key stops being valid; zero means no expiry.
	ExpiresAt time.Time
	// RevokedAt is when the key was revoked; zero means active.
	RevokedAt time.Time
	// RotatedFrom is the ID of the key this one supersedes during a
	// rotation overlap window; empty when the key was minted fresh.
	RotatedFrom string
	// CreatedAt is when the key was issued.
	CreatedAt time.Time
}

// GenerateOptions configures a single [Generate] call.
type GenerateOptions struct {
	// Kind defaults to [KindAPI] when empty.
	Kind Kind
	// Scopes are copied into the resulting Key.
	Scopes []string
	// Owner identifies the owning principal/tenant.
	Owner string
	// Prefix overrides [DefaultPrefix] when non-empty. Must be lowercase
	// alphanumeric (secret-scanner registrable, URL-safe).
	Prefix string
	// ExpiresAt sets an optional expiry; zero means the key never expires.
	ExpiresAt time.Time
	// RotatedFrom records the superseded key ID for a rotation overlap.
	RotatedFrom string
	// Now stamps CreatedAt; the caller injects a clock for determinism.
	Now time.Time
}

// Generate mints a new API key. It returns the persistable [Key] record and
// the full plaintext token wrapped in [secret.String] — the token is the
// only time the secret is available, so the caller must deliver it to the
// owner and discard it. Only the Key (with the SHA-256 hash) is persisted.
func Generate(opts GenerateOptions) (Key, *secret.String, error) {
	kind := opts.Kind
	if kind == "" {
		kind = KindAPI
	}
	if !kind.Valid() {
		return Key{}, nil, fmt.Errorf("apikey: invalid kind %q", opts.Kind)
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if err := validatePrefix(prefix); err != nil {
		return Key{}, nil, err
	}

	keyID := id.New()
	secretSegment, err := randstr.RuneSequence(secretLength, randstr.AlphaNum)
	if err != nil {
		return Key{}, nil, fmt.Errorf("apikey: generate secret: %w", err)
	}

	token := prefix + "_" + keyID + "_" + secretSegment
	key := Key{
		ID:          keyID,
		Prefix:      displayPrefix(prefix, keyID),
		Hash:        Hash(secretSegment),
		Kind:        kind,
		Scopes:      cloneScopes(opts.Scopes),
		Owner:       opts.Owner,
		ExpiresAt:   opts.ExpiresAt,
		RotatedFrom: opts.RotatedFrom,
		CreatedAt:   opts.Now,
	}
	return key, secret.NewFromString(token), nil
}

// Hash returns the SHA-256 digest of a token's secret segment. It is
// deterministic so the digest can be stored in an indexed column and
// recomputed at verification time.
func Hash(secretSegment string) [32]byte {
	return sha256.Sum256([]byte(secretSegment))
}

// Parse splits a presented token into its public id and secret segments,
// validating the prefix and structure. The id is safe to use as a lookup
// key; the secret must be passed to [Key.Verify]. Returns [ErrMalformedToken]
// for any structural problem.
func Parse(token, prefix string) (keyID, secretSegment string, err error) {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	parts := strings.Split(token, "_")
	if len(parts) != 3 {
		return "", "", ErrMalformedToken
	}
	if parts[0] != prefix || parts[1] == "" || parts[2] == "" {
		return "", "", ErrMalformedToken
	}
	return parts[1], parts[2], nil
}

// Verify checks a presented secret segment against the key. It always
// performs the SHA-256 hash and constant-time compare first so the
// revoked/expired and unknown-id (dummy-key) paths do equivalent work,
// then reports revoked/expired status before InvalidSecret so callers
// still learn the key is no longer usable when the secret matched.
// now is supplied by the caller (clock injection) so verification is
// deterministic in tests. Returns nil when the key authenticates.
func (k Key) Verify(presentedSecret string, now time.Time) error {
	// Always hash+compare so expired/revoked keys are not timing-distinguishable
	// from the middleware's dummy-key path on a repository miss.
	presented := Hash(presentedSecret)
	match := subtle.ConstantTimeCompare(presented[:], k.Hash[:]) == 1
	if err := k.statusError(now); err != nil {
		return err
	}
	if !match {
		return ErrInvalidSecret
	}
	return nil
}

// statusError returns ErrRevoked/ErrExpired when the key is inactive at now.
// Shared by [Key.Verify] and [Key.IsActive] so the two cannot drift.
func (k Key) statusError(now time.Time) error {
	if !k.RevokedAt.IsZero() && !now.Before(k.RevokedAt) {
		return ErrRevoked
	}
	if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// IsActive reports whether the key is neither expired nor revoked at now.
func (k Key) IsActive(now time.Time) bool {
	return k.statusError(now) == nil
}

func displayPrefix(prefix, keyID string) string {
	id := keyID
	if len(id) > prefixIDLen {
		id = id[:prefixIDLen]
	}
	return prefix + "_" + id
}

func cloneScopes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func validatePrefix(prefix string) error {
	if len(prefix) == 0 || len(prefix) > 8 {
		return fmt.Errorf("apikey: prefix must be 1-8 characters")
	}
	for _, r := range prefix {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !isLower && !isDigit {
			return fmt.Errorf("apikey: prefix must be lowercase alphanumeric")
		}
	}
	return nil
}
