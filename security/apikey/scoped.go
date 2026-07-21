package apikey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/randstr"
	"github.com/bds421/rho-kit/core/v2/secret"
	"github.com/bds421/rho-kit/crypto/v2/passhash"
	"github.com/bds421/rho-kit/crypto/v2/passhash/bcryptcompat"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// Default scoped-key wire prefixes. Override via [ScopedGenerateOptions.TokenPrefix]
// or [NewScopedResolver] when a service needs a custom registrable prefix.
const (
	ScopedTokenPrefixAPI   = "rhosk"
	ScopedTokenPrefixOAuth = "rhoat"
	scopedLookupPrefixLen  = 8
	scopedSecretLen        = 32
)

// ScopedKind distinguishes API keys from OAuth client records stored in the
// same scoped-key table.
type ScopedKind string

const (
	ScopedKindAPIKey      ScopedKind = "api_key"
	ScopedKindOAuthClient ScopedKind = "oauth_client"
)

// ScopedKey is a user-bound scoped credential. Hash holds a passhash PHC
// string (legacy bcrypt is accepted at verify time via bcryptcompat).
type ScopedKey struct {
	ID            string
	Tenant        string
	Prefix        string
	Hash          []byte
	Scopes        []string
	Role          string
	SubjectUserID string
	Kind          ScopedKind
	ExpiresAt     time.Time
	RevokedAt     time.Time
	CreatedAt     time.Time
}

// Principal is the auth identity produced by [ScopedResolver.Resolve].
// UserID is the bound visibility subject (UUID). KeyID is the actor lookup
// id for audit attribution; map with
// [github.com/bds421/rho-kit/httpx/v2/middleware/auth.IdentityFromScopedKey].
type Principal struct {
	UserID string
	Tenant string
	Role   string
	Scopes []string
	Kind   ScopedKind
	KeyID  string
	// NeedsRehash is true when the stored hash should be upgraded to the
	// current [WithScopedHashTarget] argon2id policy (legacy bcrypt match,
	// or argon2id params below target). Callers persist a fresh hash via
	// their PrefixRepository when this is set.
	NeedsRehash bool
}

// PrefixRepository looks up active scoped keys by their O(1) prefix column.
type PrefixRepository interface {
	ActiveByPrefix(ctx context.Context, prefix string) (ScopedKey, error)
	InsertScoped(ctx context.Context, key ScopedKey) error
}

// ScopedGenerateOptions configures [GenerateScoped].
type ScopedGenerateOptions struct {
	Tenant string
	Scopes []string
	Role   string
	// SubjectUserID is the optional UUID-shaped visibility subject. Omit for
	// unbound integration keys (tenant-wide machine access).
	SubjectUserID string
	Kind          ScopedKind
	TokenPrefix   string
	ExpiresAt     time.Time
	Now           time.Time
	HashParams    passhash.Params
}

// GenerateScoped mints a scoped key and one-time plaintext token
// (<prefix>_<lookupPrefix>_<secret>). The hash is stored; the token is
// returned once via [secret.String].
func GenerateScoped(opts ScopedGenerateOptions) (ScopedKey, *secret.String, error) {
	if opts.Tenant == "" {
		return ScopedKey{}, nil, fmt.Errorf("apikey: scoped key requires tenant")
	}
	subjectUserID := opts.SubjectUserID
	if subjectUserID != "" {
		norm, ok := jwtutil.NormalizeSubjectID(subjectUserID)
		if !ok {
			return ScopedKey{}, nil, fmt.Errorf("apikey: subject user id must be a UUID")
		}
		subjectUserID = norm
	}
	prefix := opts.TokenPrefix
	if prefix == "" {
		prefix = ScopedTokenPrefixAPI
	}
	if err := validatePrefix(prefix); err != nil {
		return ScopedKey{}, nil, err
	}
	kind := opts.Kind
	if kind == "" {
		kind = ScopedKindAPIKey
	}
	lookup, err := randstr.RuneSequence(scopedLookupPrefixLen, randstr.AlphaNum)
	if err != nil {
		return ScopedKey{}, nil, fmt.Errorf("apikey: generate lookup prefix: %w", err)
	}
	secretPart, err := randstr.RuneSequence(scopedSecretLen, randstr.AlphaNum)
	if err != nil {
		return ScopedKey{}, nil, fmt.Errorf("apikey: generate secret: %w", err)
	}
	params := opts.HashParams
	if params.Memory == 0 {
		params = passhash.DefaultParams()
	}
	hash, err := passhash.Hash(secretPart, params)
	if err != nil {
		return ScopedKey{}, nil, fmt.Errorf("apikey: hash scoped secret: %w", err)
	}
	token := prefix + "_" + lookup + "_" + secretPart
	key := ScopedKey{
		ID:            lookup,
		Tenant:        opts.Tenant,
		Prefix:        lookup,
		Hash:          []byte(hash),
		Scopes:        cloneScopes(opts.Scopes),
		Role:          opts.Role,
		SubjectUserID: subjectUserID,
		Kind:          kind,
		ExpiresAt:     opts.ExpiresAt,
		CreatedAt:     opts.Now,
	}
	return key, secret.NewFromString(token), nil
}

// ScopedResolver verifies presented scoped tokens against [PrefixRepository].
type ScopedResolver struct {
	repo        PrefixRepository
	tokenPrefix string
	hashTarget  passhash.Params
	now         func() time.Time
}

// ScopedResolverOption configures [NewScopedResolver].
type ScopedResolverOption func(*ScopedResolver)

// WithScopedHashTarget sets the argon2id policy for NeedsRehash hints.
func WithScopedHashTarget(p passhash.Params) ScopedResolverOption {
	return func(r *ScopedResolver) { r.hashTarget = p }
}

// WithScopedClock overrides the wall clock used for expiry checks.
func WithScopedClock(now func() time.Time) ScopedResolverOption {
	return func(r *ScopedResolver) {
		if now != nil {
			r.now = now
		}
	}
}

// NewScopedResolver returns a resolver for tokens with the given wire prefix
// (e.g. [ScopedTokenPrefixAPI] or [ScopedTokenPrefixOAuth]).
func NewScopedResolver(repo PrefixRepository, tokenPrefix string, opts ...ScopedResolverOption) *ScopedResolver {
	if repo == nil {
		panic("apikey: NewScopedResolver requires a non-nil PrefixRepository")
	}
	if tokenPrefix == "" {
		tokenPrefix = ScopedTokenPrefixAPI
	}
	r := &ScopedResolver{
		repo:        repo,
		tokenPrefix: tokenPrefix,
		hashTarget:  passhash.DefaultParams(),
		now:         time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

var (
	// ErrScopedMalformed is returned when the presented token structure is invalid.
	ErrScopedMalformed = errors.New("apikey: malformed scoped token")
	// ErrScopedNotFound is returned when no active key matches the prefix.
	ErrScopedNotFound = apperror.NewNotFound("scoped api key", "")
)

// TokenPrefix returns the wire prefix this resolver accepts.
func (r *ScopedResolver) TokenPrefix() string { return r.tokenPrefix }

// Resolve validates a presented secret and returns the impersonation principal.
func (r *ScopedResolver) Resolve(ctx context.Context, presented string) (Principal, error) {
	lookup, secretPart, err := parseScopedToken(presented, r.tokenPrefix)
	if err != nil {
		return Principal{}, err
	}
	key, err := r.repo.ActiveByPrefix(ctx, lookup)
	if err != nil {
		return Principal{}, err
	}
	now := r.now()
	if !key.RevokedAt.IsZero() && !now.Before(key.RevokedAt) {
		return Principal{}, ErrRevoked
	}
	if !key.ExpiresAt.IsZero() && !now.Before(key.ExpiresAt) {
		return Principal{}, ErrExpired
	}
	res, err := bcryptcompat.Verify(secretPart, string(key.Hash), r.hashTarget)
	if err != nil {
		return Principal{}, fmt.Errorf("apikey: verify scoped secret: %w", err)
	}
	if !res.Matched {
		return Principal{}, ErrInvalidSecret
	}
	subject := key.SubjectUserID
	if subject != "" {
		norm, ok := jwtutil.NormalizeSubjectID(subject)
		if !ok {
			return Principal{}, fmt.Errorf("apikey: scoped key %q has non-UUID subject user id", key.ID)
		}
		subject = norm
	}
	return Principal{
		UserID:      subject,
		Tenant:      key.Tenant,
		Role:        key.Role,
		Scopes:      cloneScopes(key.Scopes),
		Kind:        key.Kind,
		KeyID:       key.ID,
		NeedsRehash: res.NeedsRehash,
	}, nil
}

// HasScope reports whether principal carries the exact scope string.
func HasScope(p Principal, scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func parseScopedToken(token, prefix string) (lookupPrefix, secret string, err error) {
	parts := strings.Split(token, "_")
	if len(parts) != 3 || parts[0] != prefix || parts[1] == "" || parts[2] == "" {
		return "", "", ErrScopedMalformed
	}
	return parts[1], parts[2], nil
}

// MemoryPrefixRepository is an in-memory [PrefixRepository] for tests.
// Safe for concurrent use; ActiveByPrefix returns a defensive clone so
// callers cannot mutate the stored Scopes/Hash slices.
type MemoryPrefixRepository struct {
	mu   sync.RWMutex
	keys map[string]ScopedKey
}

// NewMemoryPrefixRepository returns an empty scoped-key store.
func NewMemoryPrefixRepository() *MemoryPrefixRepository {
	return &MemoryPrefixRepository{keys: make(map[string]ScopedKey)}
}

// InsertScoped implements [PrefixRepository].
func (r *MemoryPrefixRepository) InsertScoped(_ context.Context, key ScopedKey) error {
	if key.Prefix == "" {
		return fmt.Errorf("apikey: scoped key requires prefix")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.keys[key.Prefix]; exists {
		return apperror.NewConflict("apikey: scoped key prefix already exists")
	}
	r.keys[key.Prefix] = cloneScopedKey(key)
	return nil
}

// ActiveByPrefix implements [PrefixRepository].
func (r *MemoryPrefixRepository) ActiveByPrefix(_ context.Context, prefix string) (ScopedKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.keys[prefix]
	if !ok {
		return ScopedKey{}, ErrScopedNotFound
	}
	return cloneScopedKey(key), nil
}

func cloneScopedKey(k ScopedKey) ScopedKey {
	out := k
	if k.Scopes != nil {
		out.Scopes = append([]string(nil), k.Scopes...)
	}
	if k.Hash != nil {
		out.Hash = append([]byte(nil), k.Hash...)
	}
	return out
}
