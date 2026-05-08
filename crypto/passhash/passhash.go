// Package passhash implements password hashing using argon2id with a
// PHC-formatted output string. argon2id is the OWASP-recommended
// algorithm and the default in NIST SP 800-63B; using it from this
// package gives services consistent parameters and the
// verify-then-rehash pattern needed for transparent parameter
// upgrades.
//
// Output format (PHC string, RFC 9106 reference):
//
//	$argon2id$v=19$m=65536,t=3,p=1$<salt-base64>$<hash-base64>
//
// The format embeds the parameters used to generate the hash, so
// [Verify] can recompute without callers tracking them. When the
// stored parameters are weaker than the verifier's `target` along
// any dimension, Verify returns needsRehash=true so the caller can
// transparently upgrade on next login.
//
// asvs: V6.2.1
package passhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Sentinel errors. Verify wraps the underlying error so callers can
// distinguish parse failures from genuine mismatches.
var (
	ErrEmptyPassword     = errors.New("passhash: password must not be empty")
	ErrPasswordTooLong   = errors.New("passhash: password exceeds maximum length")
	ErrMalformed         = errors.New("passhash: malformed encoded hash")
	ErrUnsupportedFormat = errors.New("passhash: unsupported encoded format (only argon2id v=19)")
	ErrParamsOutOfBounds = errors.New("passhash: stored argon2 params exceed the verifier's accepted bounds")
)

// MaxPasswordLen caps the password length both Hash and Verify will
// process. Argon2's prelude streams the password through Blake2b
// without any length cap of its own, so an unbounded password lets an
// attacker amplify per-request CPU/memory cost (a 100 MiB "password"
// turns one login attempt into a multi-second worker stall). 1 KiB is
// orders of magnitude above any legitimate passphrase and below the
// pathological-DoS threshold.
const MaxPasswordLen = 1024

// VerifyLimits caps the cost parameters Verify is willing to accept
// from a stored hash before invoking argon2. A corrupted database row
// or maliciously crafted PHC string can otherwise request multi-GiB
// memory or thousands of iterations and DoS login workers.
//
// All fields are upper bounds, inclusive. A zero value means "use the
// default bound" (see [DefaultVerifyLimits]). Apply with
// [WithVerifyLimits] when calling [Verify].
type VerifyLimits struct {
	MaxMemory      uint32 // KiB; default 1 GiB
	MaxIterations  uint32 // default 100
	MaxParallelism uint8  // default 16
	MaxSaltLen     uint32 // bytes; default 64
	MaxKeyLen      uint32 // bytes; default 64
}

// DefaultVerifyLimits returns the bounds Verify uses when no
// [WithVerifyLimits] option is supplied. They are deliberately
// generous compared to [DefaultParams] so legitimate parameter
// upgrades still verify, but small enough that a malicious row cannot
// pin a CPU or allocate gigabytes.
func DefaultVerifyLimits() VerifyLimits {
	return VerifyLimits{
		MaxMemory:      1 * 1024 * 1024,
		MaxIterations:  100,
		MaxParallelism: 16,
		MaxSaltLen:     64,
		MaxKeyLen:      64,
	}
}

func (l VerifyLimits) withDefaults() VerifyLimits {
	d := DefaultVerifyLimits()
	if l.MaxMemory == 0 {
		l.MaxMemory = d.MaxMemory
	}
	if l.MaxIterations == 0 {
		l.MaxIterations = d.MaxIterations
	}
	if l.MaxParallelism == 0 {
		l.MaxParallelism = d.MaxParallelism
	}
	if l.MaxSaltLen == 0 {
		l.MaxSaltLen = d.MaxSaltLen
	}
	if l.MaxKeyLen == 0 {
		l.MaxKeyLen = d.MaxKeyLen
	}
	return l
}

func (l VerifyLimits) accepts(p Params) bool {
	return p.Memory <= l.MaxMemory &&
		p.Iterations <= l.MaxIterations &&
		p.Parallelism <= l.MaxParallelism &&
		p.SaltLen <= l.MaxSaltLen &&
		p.KeyLen <= l.MaxKeyLen
}

// VerifyOption configures [Verify] behaviour.
type VerifyOption func(*verifyConfig)

type verifyConfig struct {
	limits VerifyLimits
}

// WithVerifyLimits overrides the per-dimension caps Verify enforces
// before calling argon2. Use this if you know your deployment's
// upgrade ceiling differs from [DefaultVerifyLimits]. Zero fields in
// the supplied limits inherit the default bound.
func WithVerifyLimits(l VerifyLimits) VerifyOption {
	return func(c *verifyConfig) { c.limits = l }
}

// Params controls the cost of an argon2id hash. Defaults are calibrated
// for ~100ms on a 2025-era server core; re-benchmark on production
// hardware before raising.
type Params struct {
	Memory      uint32 // KiB; default 64*1024 (64 MiB)
	Iterations  uint32 // default 3
	Parallelism uint8  // default 1
	SaltLen     uint32 // bytes; default 16
	KeyLen      uint32 // bytes; default 32
}

// DefaultParams returns the OWASP-recommended argon2id parameters
// (subject to per-release re-calibration). Use these unless you have
// benchmarked weaker parameters and decided to accept the tradeoff.
func DefaultParams() Params {
	return Params{
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 1,
		SaltLen:     16,
		KeyLen:      32,
	}
}

// atLeastAsStrongAs reports whether `a` (the stored params) meets or
// exceeds `b` (the verifier's target) along every dimension. When this
// is false the stored hash is weaker than current policy and the
// caller should re-hash on next successful login.
func (a Params) atLeastAsStrongAs(b Params) bool {
	return a.Memory >= b.Memory &&
		a.Iterations >= b.Iterations &&
		a.Parallelism >= b.Parallelism &&
		a.SaltLen >= b.SaltLen &&
		a.KeyLen >= b.KeyLen
}

// Hash returns an argon2id PHC-format encoded string. Empty passwords
// are rejected to fail loudly on misuse — the common cause is a bug
// passing an unset field rather than a deliberate empty value.
func Hash(password string, p Params) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	if len(password) > MaxPasswordLen {
		return "", ErrPasswordTooLong
	}
	if p.SaltLen == 0 {
		p.SaltLen = DefaultParams().SaltLen
	}
	if p.KeyLen == 0 {
		p.KeyLen = DefaultParams().KeyLen
	}
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		return "", fmt.Errorf("passhash: Memory/Iterations/Parallelism must be > 0")
	}

	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("passhash: read salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)

	enc := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		p.Memory,
		p.Iterations,
		p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return enc, nil
}

// Verify checks password against the encoded PHC string. Returns:
//   - matched=true when the password matches.
//   - needsRehash=true when the stored parameters are weaker than
//     target along any dimension (caller should re-hash and persist).
//   - err non-nil for malformed input or when the stored parameters
//     exceed the verifier's accepted bounds (see [VerifyLimits]).
//
// matched is computed in constant time against the stored hash.
// Callers MUST use matched to gate authentication; needsRehash is a
// hint, not a security boundary.
//
// Verify caps the cost parameters it is willing to feed argon2 with
// [DefaultVerifyLimits], or with caller-supplied limits via
// [WithVerifyLimits]. A stored hash that requests more memory,
// iterations, parallelism, salt length, or key length than the cap
// is rejected with [ErrParamsOutOfBounds] before any argon2 work
// runs. This prevents a corrupted row or attacker-controlled hash
// string from pinning login workers or exhausting memory.
func Verify(password, encoded string, target Params, opts ...VerifyOption) (matched bool, needsRehash bool, err error) {
	if password == "" {
		return false, false, ErrEmptyPassword
	}
	if len(password) > MaxPasswordLen {
		return false, false, ErrPasswordTooLong
	}

	cfg := verifyConfig{limits: DefaultVerifyLimits()}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.limits = cfg.limits.withDefaults()

	stored, salt, hash, err := parsePHC(encoded)
	if err != nil {
		return false, false, err
	}
	if !cfg.limits.accepts(stored) {
		return false, false, fmt.Errorf("%w: m=%d t=%d p=%d saltLen=%d keyLen=%d",
			ErrParamsOutOfBounds, stored.Memory, stored.Iterations, stored.Parallelism, stored.SaltLen, stored.KeyLen)
	}

	candidate := argon2.IDKey([]byte(password), salt, stored.Iterations, stored.Memory, stored.Parallelism, uint32(len(hash)))
	matched = subtle.ConstantTimeCompare(candidate, hash) == 1

	if matched && !stored.atLeastAsStrongAs(target) {
		needsRehash = true
	}
	return matched, needsRehash, nil
}

// maxEncodedLen caps the size of a PHC string Verify is willing to
// parse. The longest legitimate encoded form is well under 1 KiB
// (DefaultParams produces ~96 bytes); anything larger is malformed or
// hostile. Capping the input before base64-decoding prevents a
// crafted row from causing megabyte allocations during parse.
const maxEncodedLen = 4096

// parsePHC parses the kit's `$argon2id$v=19$m=…,t=…,p=…$salt$hash`
// format. Tolerates whitespace around the input.
func parsePHC(s string) (Params, []byte, []byte, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxEncodedLen {
		return Params{}, nil, nil, ErrMalformed
	}
	if !strings.HasPrefix(s, "$") {
		return Params{}, nil, nil, ErrMalformed
	}
	parts := strings.Split(s, "$")
	// Expect: ["", "argon2id", "v=N", "m=…,t=…,p=…", saltB64, hashB64].
	if len(parts) != 6 {
		return Params{}, nil, nil, ErrMalformed
	}
	if parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrUnsupportedFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	if version != argon2.Version {
		return Params{}, nil, nil, ErrUnsupportedFormat
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(hash))
	return p, salt, hash, nil
}
