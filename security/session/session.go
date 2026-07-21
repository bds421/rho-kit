// Package session provides stateless HMAC session tokens with a token
// version (ver) claim for password-change revocation and optional live
// role re-validation against a [VersionedUserStore].
package session

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/bds421/rho-kit/core/v2/clock"
)

var (
	// ErrEmptyRoot is returned when DeriveKey or NewSigner receive an empty root.
	ErrEmptyRoot = errors.New("session: root key must not be empty")
	// ErrEmptyLabel is returned when DeriveKey or NewSigner receive an empty label.
	ErrEmptyLabel = errors.New("session: label must not be empty")
	// ErrShortRoot is returned when the root key is shorter than minRootLen.
	ErrShortRoot = errors.New("session: root key must be at least 32 bytes")
	// ErrInvalidToken is returned for malformed or wrongly signed tokens.
	ErrInvalidToken = errors.New("session: invalid token")
	// ErrInvalidClaims is returned by [HMACSigner.Mint] when required claim
	// fields are missing (empty UserID/Tenant or zero Exp).
	ErrInvalidClaims = errors.New("session: invalid claims")
	// ErrExpired is returned when the token exp claim is in the past.
	ErrExpired = errors.New("session: token expired")
	// ErrSessionRevoked is returned when ver no longer matches the store.
	ErrSessionRevoked = errors.New("session: session revoked")
	// ErrValidatorNotConfigured is returned by [Validator.Validate] when
	// Signer is nil — a wiring bug, not a bad token.
	ErrValidatorNotConfigured = errors.New("session: validator has no Signer")
)

const (
	minRootLen    = 32
	derivedKeyLen = 32
	maxTokenLen   = 4096
)

// Claims are the session token fields carried on the wire and returned
// after verification.
type Claims struct {
	UserID       string    `json:"uid"`
	Tenant       string    `json:"ten"`
	Role         string    `json:"role"`
	TokenVersion int       `json:"ver"`
	Exp          time.Time `json:"exp"`
}

type wireClaims struct {
	UserID       string `json:"uid"`
	Tenant       string `json:"ten"`
	Role         string `json:"role"`
	TokenVersion int    `json:"ver"`
	Exp          int64  `json:"exp"`
}

// VersionedUserStore supplies live token version and role for re-validation.
type VersionedUserStore interface {
	TokenVersion(ctx context.Context, userID string) (int, error)
	Role(ctx context.Context, userID string) (string, error)
}

// Signer mints and verifies stateless session tokens.
type Signer interface {
	Mint(claims Claims) (string, error)
	Verify(token string, now time.Time) (Claims, error)
}

// DeriveKey derives a 32-byte purpose-specific key from root and label
// via HKDF-SHA256. Use distinct labels per purpose (session vs audit).
func DeriveKey(root []byte, label string) ([]byte, error) {
	if len(root) == 0 {
		return nil, ErrEmptyRoot
	}
	if len(root) < minRootLen {
		return nil, ErrShortRoot
	}
	if label == "" {
		return nil, ErrEmptyLabel
	}
	r := hkdf.New(sha256.New, root, nil, []byte(label))
	key := make([]byte, derivedKeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("session: derive key: %w", err)
	}
	return key, nil
}

// HMACSigner signs session tokens with HMAC-SHA256 over the base64url payload.
// Signing always uses the first (current) key; verification accepts any key
// in the ring so root rotation can overlap without force-logging-out users.
type HMACSigner struct {
	key  []byte   // current signing key (also first of keys)
	keys [][]byte // current + previous verification keys
	now  clock.Func
}

// SignerOption configures [NewSigner].
type SignerOption func(*HMACSigner)

// WithClock overrides the time source used when [HMACSigner.Verify] is
// called with a zero now (and by any helper that falls back to the
// signer's clock). Callers that pass an explicit now are unaffected.
func WithClock(now clock.Func) SignerOption {
	if now == nil {
		panic("session: WithClock requires a non-nil time source")
	}
	return func(s *HMACSigner) { s.now = now }
}

// NewSigner constructs an HMAC session signer from root and label.
func NewSigner(root []byte, label string, opts ...SignerOption) (*HMACSigner, error) {
	return NewSignerWithRoots(root, nil, label, opts...)
}

// NewSignerWithRoots constructs an HMAC session signer that signs with
// current and accepts previous roots at verify time (rotation overlap).
// Each root is HKDF-derived with label independently. Empty previous is
// equivalent to [NewSigner].
func NewSignerWithRoots(current []byte, previous [][]byte, label string, opts ...SignerOption) (*HMACSigner, error) {
	key, err := DeriveKey(current, label)
	if err != nil {
		return nil, err
	}
	keys := make([][]byte, 0, 1+len(previous))
	keys = append(keys, key)
	for _, prev := range previous {
		pk, err := DeriveKey(prev, label)
		if err != nil {
			return nil, err
		}
		keys = append(keys, pk)
	}
	s := &HMACSigner{key: key, keys: keys, now: time.Now}
	for _, opt := range opts {
		if opt == nil {
			panic("session: NewSigner option must not be nil")
		}
		opt(s)
	}
	return s, nil
}

// Mint returns a signed token for claims. Exp must be set by the caller.
// Missing required fields return [ErrInvalidClaims]; verification failures
// continue to use [ErrInvalidToken].
func (s *HMACSigner) Mint(claims Claims) (string, error) {
	if claims.UserID == "" || claims.Tenant == "" {
		return "", ErrInvalidClaims
	}
	if claims.Exp.IsZero() {
		return "", ErrInvalidClaims
	}
	w := wireClaims{
		UserID:       claims.UserID,
		Tenant:       claims.Tenant,
		Role:         claims.Role,
		TokenVersion: claims.TokenVersion,
		Exp:          claims.Exp.Unix(),
	}
	payload, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("session: marshal claims: %w", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	mac := s.sign([]byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// Verify checks the token signature and exp claim.
// When now is the zero time, the signer's clock (see [WithClock],
// defaulting to time.Now) is used so production call sites can omit an
// explicit clock while tests inject a fixed one.
func (s *HMACSigner) Verify(token string, now time.Time) (Claims, error) {
	if s == nil {
		return Claims{}, ErrInvalidToken
	}
	if now.IsZero() {
		clockFn := s.now
		if clockFn == nil {
			clockFn = time.Now
		}
		now = clockFn()
	}
	if len(token) > maxTokenLen {
		return Claims{}, ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	if !s.macValid([]byte(parts[0]), gotMAC) {
		return Claims{}, ErrInvalidToken
	}
	var w wireClaims
	if err := json.Unmarshal(payload, &w); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if w.UserID == "" || w.Tenant == "" || w.Exp == 0 {
		return Claims{}, ErrInvalidToken
	}
	exp := time.Unix(w.Exp, 0)
	if !now.Before(exp) {
		return Claims{}, ErrExpired
	}
	return Claims{
		UserID:       w.UserID,
		Tenant:       w.Tenant,
		Role:         w.Role,
		TokenVersion: w.TokenVersion,
		Exp:          exp,
	}, nil
}

func (s *HMACSigner) sign(msg []byte) []byte {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(msg)
	return mac.Sum(nil)
}

func (s *HMACSigner) macValid(msg, got []byte) bool {
	keys := s.keys
	if len(keys) == 0 && len(s.key) > 0 {
		keys = [][]byte{s.key}
	}
	ok := 0
	for _, k := range keys {
		mac := hmac.New(sha256.New, k)
		_, _ = mac.Write(msg)
		want := mac.Sum(nil)
		ok |= subtle.ConstantTimeCompare(got, want)
	}
	return ok == 1
}

// Validator verifies tokens and optionally re-validates ver/role against store.
type Validator struct {
	Signer Signer
	Store  VersionedUserStore
}

// Validate verifies the token and, when Store is set, checks token version
// and refreshes Role from the database.
func (v *Validator) Validate(ctx context.Context, token string, now time.Time) (Claims, error) {
	if v == nil || v.Signer == nil {
		return Claims{}, ErrValidatorNotConfigured
	}
	claims, err := v.Signer.Verify(token, now)
	if err != nil {
		return Claims{}, err
	}
	if v.Store == nil {
		return claims, nil
	}
	ver, err := v.Store.TokenVersion(ctx, claims.UserID)
	if err != nil {
		return Claims{}, err
	}
	if ver != claims.TokenVersion {
		return Claims{}, ErrSessionRevoked
	}
	role, err := v.Store.Role(ctx, claims.UserID)
	if err != nil {
		return Claims{}, err
	}
	claims.Role = role
	return claims, nil
}
