package identity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/contextutil"
)

const (
	maxPrincipalIDBytes = 255
	maxEntitlements     = 256
	maxClaims           = 32
)

var (
	ErrInvalidPrincipal = errors.New("identity: invalid principal")
	principalKey        = contextutil.NewKey[Principal]("security.identity.principal")
)

// Principal is the provider-neutral identity exposed to application code. It
// intentionally carries only mapped, verified values: raw bearer tokens and
// an unbounded provider-claim dump do not cross the authentication boundary.
type Principal struct {
	Subject     string
	Actor       string
	Kind        ActorKind
	Tenant      string
	Scopes      []string
	Permissions []string
	Claims      map[string]string
}

// MappingProfile explicitly allow-lists provider claims that become principal
// data. Map keys are principal claim names; values are provider claim names.
// The zero profile maps only the verified subject as a human actor.
type MappingProfile struct {
	TenantClaim      string
	ActorClaim       string
	Kind             ActorKind
	ScopesClaim      string
	PermissionsClaim string
	Claims           map[string]string

	// AllowMissingClaims leaves a selected claim empty when the provider
	// omits it instead of failing the projection. A present claim with the
	// wrong shape or an invalid value still fails closed. Default false:
	// every selected claim is required.
	AllowMissingClaims bool
}

// Project maps a verified subject and verified provider claims to a bounded
// Principal. A selected claim with the wrong shape fails closed rather than
// silently changing an authorization input.
func (m MappingProfile) Project(subject string, providerClaims map[string]any) (Principal, error) {
	p := Principal{Subject: subject, Actor: subject, Kind: ActorUser}
	if m.Kind != "" {
		p.Kind = m.Kind
	}
	if !validKind(p.Kind) || !validID(subject) {
		return Principal{}, ErrInvalidPrincipal
	}
	if err := m.mapID(providerClaims, m.TenantClaim, "tenant", &p.Tenant); err != nil {
		return Principal{}, err
	}
	if err := m.mapID(providerClaims, m.ActorClaim, "actor", &p.Actor); err != nil {
		return Principal{}, err
	}
	if err := m.mapEntitlements(providerClaims, m.ScopesClaim, "scopes", &p.Scopes); err != nil {
		return Principal{}, err
	}
	if err := m.mapEntitlements(providerClaims, m.PermissionsClaim, "permissions", &p.Permissions); err != nil {
		return Principal{}, err
	}
	claims, err := m.mapClaims(providerClaims)
	if err != nil {
		return Principal{}, err
	}
	p.Claims = claims
	return p.normalized(), nil
}

// mapID sets *into from the selected claim, which must be a valid ID. An
// unselected or skipped claim leaves *into unchanged.
func (m MappingProfile) mapID(providerClaims map[string]any, claim, field string, into *string) error {
	if claim == "" || m.skip(providerClaims, claim) {
		return nil
	}
	value, err := requiredString(providerClaims, claim)
	if err != nil || !validID(value) {
		return invalidMapping(field, err)
	}
	*into = value
	return nil
}

// mapEntitlements sets *into from the selected claim's entitlement list. An
// unselected or skipped claim leaves *into unchanged.
func (m MappingProfile) mapEntitlements(providerClaims map[string]any, claim, field string, into *[]string) error {
	if claim == "" || m.skip(providerClaims, claim) {
		return nil
	}
	list, err := entitlementList(providerClaims[claim])
	if err != nil {
		return invalidMapping(field, err)
	}
	*into = list
	return nil
}

// mapClaims maps the extra claims; it returns nil when none is set.
func (m MappingProfile) mapClaims(providerClaims map[string]any) (map[string]string, error) {
	if len(m.Claims) > maxClaims {
		return nil, fmt.Errorf("%w: too many mapped claims", ErrInvalidPrincipal)
	}
	if len(m.Claims) == 0 {
		return nil, nil
	}
	claims := make(map[string]string, len(m.Claims))
	for target, source := range m.Claims {
		if !validClaimName(target) || source == "" {
			return nil, fmt.Errorf("%w: invalid claim mapping", ErrInvalidPrincipal)
		}
		if m.skip(providerClaims, source) {
			continue
		}
		value, err := requiredString(providerClaims, source)
		if err != nil || !validID(value) {
			return nil, invalidMapping("claim "+target, err)
		}
		claims[target] = value
	}
	if len(claims) == 0 {
		return nil, nil
	}
	return claims, nil
}

// skip reports whether an absent (or null) selected claim may be left empty.
func (m MappingProfile) skip(claims map[string]any, name string) bool {
	return m.AllowMissingClaims && claims[name] == nil
}

// WithPrincipal stores an immutable snapshot of p in ctx. The copied slices
// and map prevent a handler from mutating identity observed by later layers.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return principalKey.Set(ctx, p.normalized())
}

// FromContext returns an immutable snapshot of the canonical principal.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := principalKey.Get(ctx)
	if !ok {
		return Principal{}, false
	}
	return p.normalized(), true
}

func (p Principal) normalized() Principal {
	if p.Kind == "" && p.Subject != "" {
		p.Kind = ActorUser
	}
	if p.Actor == "" && p.Subject != "" && p.Kind == ActorUser {
		p.Actor = p.Subject
	}
	p.Scopes = normalizeEntitlements(p.Scopes)
	p.Permissions = normalizeEntitlements(p.Permissions)
	p.Claims = maps.Clone(p.Claims)
	return p
}

func requiredString(claims map[string]any, name string) (string, error) {
	v, ok := claims[name]
	if !ok {
		return "", errors.New("claim is missing")
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("claim is not a string")
	}
	return s, nil
}

func entitlementList(v any) ([]string, error) {
	if v == nil {
		return nil, errors.New("claim is missing")
	}
	var values []string
	switch x := v.(type) {
	case string:
		values = strings.Fields(x)
	case []string:
		values = slices.Clone(x)
	case []any:
		values = make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("list contains non-string")
			}
			values = append(values, s)
		}
	default:
		return nil, errors.New("claim is not a string or string list")
	}
	values = normalizeEntitlements(values)
	if len(values) > maxEntitlements {
		return nil, errors.New("too many entitlements")
	}
	for _, value := range values {
		if !validID(value) {
			return nil, errors.New("invalid entitlement")
		}
	}
	return values, nil
}

func normalizeEntitlements(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validKind(kind ActorKind) bool {
	switch kind {
	case ActorUser, ActorAPIKey, ActorOAuthClient, ActorService:
		return true
	default:
		return false
	}
}

func validID(value string) bool {
	if value == "" || len(value) > maxPrincipalIDBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validClaimName(value string) bool {
	if !validID(value) {
		return false
	}
	for _, r := range value {
		if r != '_' && r != '-' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func invalidMapping(field string, err error) error {
	if err == nil {
		err = errors.New("invalid value")
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalidPrincipal, field, err)
}
