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
	ErrInvalidParams     = errors.New("passhash: argon2 params must be positive")
	// ErrHashParamsOutOfBounds is returned by Hash when the supplied
	// Params exceed the configured HashLimits along any dimension. It
	// mirrors ErrParamsOutOfBounds on the Verify side so callers can
	// errors.Is-branch on a config-typo rejection. The wrapped message
	// never echoes the offending value.
	ErrHashParamsOutOfBounds = errors.New("passhash: argon2 params exceed the configured hash limits")
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
	MaxMemory      uint32 // KiB; default 256 MiB
	MaxIterations  uint32 // default 100
	MaxParallelism uint8  // default 16
	MaxSaltLen     uint32 // bytes; default 64
	MaxKeyLen      uint32 // bytes; default 64
}

// DefaultVerifyLimits returns the bounds Verify uses when no
// [WithVerifyLimits] option is supplied. Deliberately generous
// compared to [DefaultParams] so legitimate parameter upgrades still
// verify, but small enough that a malicious row cannot pin a CPU or
// allocate gigabytes. The previous default permitted up to 1 GiB of
// Argon2 memory per verification; wave 66 lowered the cap to 256 MiB
// after a hostile review flagged the comment/value mismatch.
// Operators running Argon2id with memory above 256 MiB can raise
// this cap explicitly via [WithVerifyLimits], with full knowledge
// of the memory-amplification trade-off.
func DefaultVerifyLimits() VerifyLimits {
	return VerifyLimits{
		MaxMemory:      256 * 1024,
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

// RFC 9106 recommends ≥8-byte salts and ≥16-byte digests. Verify
// enforces those floors so a corrupted or attacker-crafted PHC row
// cannot degrade ConstantTimeCompare to a single-byte match (p=1/256)
// by declaring a 1-byte digest. Upper bounds remain configurable via
// MaxSaltLen/MaxKeyLen.
const (
	minVerifySaltLen uint32 = 8
	minVerifyKeyLen  uint32 = 16
)

func (l VerifyLimits) accepts(p Params) bool {
	return p.Memory <= l.MaxMemory &&
		p.Iterations <= l.MaxIterations &&
		p.Parallelism <= l.MaxParallelism &&
		p.SaltLen >= minVerifySaltLen &&
		p.SaltLen <= l.MaxSaltLen &&
		p.KeyLen >= minVerifyKeyLen &&
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

func (p Params) validatePositive() error {
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 || p.SaltLen == 0 || p.KeyLen == 0 {
		return ErrInvalidParams
	}
	return nil
}

func (p Params) withDefaults() Params {
	d := DefaultParams()
	if p.Memory == 0 {
		p.Memory = d.Memory
	}
	if p.Iterations == 0 {
		p.Iterations = d.Iterations
	}
	if p.Parallelism == 0 {
		p.Parallelism = d.Parallelism
	}
	if p.SaltLen == 0 {
		p.SaltLen = d.SaltLen
	}
	if p.KeyLen == 0 {
		p.KeyLen = d.KeyLen
	}
	return p
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

// atLeastAsStrongAs reports whether p (the stored params) meets or
// exceeds target along every dimension. When this is false the stored
// hash is weaker than current policy and the caller should re-hash on
// next successful login.
func (p Params) atLeastAsStrongAs(target Params) bool {
	return p.Memory >= target.Memory &&
		p.Iterations >= target.Iterations &&
		p.Parallelism >= target.Parallelism &&
		p.SaltLen >= target.SaltLen &&
		p.KeyLen >= target.KeyLen
}

// HashLimits caps the cost parameters [Hash] is willing to feed
// argon2id. A typo or attacker-influenced Params slice could
// otherwise allocate multi-GiB memory or pin a CPU on every Hash
// call. The defaults are intentionally tighter than [VerifyLimits]
// — verification has to tolerate parameter upgrades from older
// stored hashes, but Hash is invoked under the caller's control and
// should fail loudly when a config typo turns it into a DoS knob.
//
// Zero fields inherit [DefaultHashLimits]. Apply via [WithHashLimits].
type HashLimits struct {
	MaxMemory      uint32 // KiB; default 256 MiB
	MaxIterations  uint32 // default 100
	MaxParallelism uint8  // default 16
	MaxSaltLen     uint32 // bytes; default 64
	MaxKeyLen      uint32 // bytes; default 64
}

// DefaultHashLimits returns the bounds Hash uses when no
// [WithHashLimits] option is supplied.
func DefaultHashLimits() HashLimits {
	return HashLimits{
		MaxMemory:      256 * 1024,
		MaxIterations:  100,
		MaxParallelism: 16,
		MaxSaltLen:     64,
		MaxKeyLen:      64,
	}
}

func (l HashLimits) withDefaults() HashLimits {
	d := DefaultHashLimits()
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

// HashOption configures [Hash].
type HashOption func(*hashConfig)

type hashConfig struct {
	limits HashLimits
}

// WithHashLimits overrides the per-dimension caps Hash enforces
// before calling argon2id. Use this only when the deployment has
// benchmarked that the higher cost is acceptable on production
// hardware — the defaults are tuned to keep a single Hash call
// well under one second on a 2025-era server core.
func WithHashLimits(l HashLimits) HashOption {
	return func(c *hashConfig) { c.limits = l }
}

// Hash returns an argon2id PHC-format encoded string. Empty passwords
// are rejected to fail loudly on misuse — the common cause is a bug
// passing an unset field rather than a deliberate empty value.
//
// Hash enforces [DefaultHashLimits] on the supplied [Params] so a
// typo cannot quietly turn an end-user login flow into a per-request
// CPU/memory DoS. Override via [WithHashLimits] when the deployment
// has benchmarked a higher cost.
func Hash(password string, p Params, opts ...HashOption) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	if len(password) > MaxPasswordLen {
		return "", ErrPasswordTooLong
	}
	p = p.withDefaults()
	if err := p.validatePositive(); err != nil {
		return "", err
	}
	cfg := hashConfig{limits: DefaultHashLimits()}
	for _, opt := range opts {
		if opt == nil {
			panic("passhash: Hash option must not be nil")
		}
		opt(&cfg)
	}
	cfg.limits = cfg.limits.withDefaults()
	if p.Memory > cfg.limits.MaxMemory {
		return "", fmt.Errorf("%w: Memory", ErrHashParamsOutOfBounds)
	}
	if p.Iterations > cfg.limits.MaxIterations {
		return "", fmt.Errorf("%w: Iterations", ErrHashParamsOutOfBounds)
	}
	if p.Parallelism > cfg.limits.MaxParallelism {
		return "", fmt.Errorf("%w: Parallelism", ErrHashParamsOutOfBounds)
	}
	if p.SaltLen > cfg.limits.MaxSaltLen {
		return "", fmt.Errorf("%w: SaltLen", ErrHashParamsOutOfBounds)
	}
	if p.KeyLen > cfg.limits.MaxKeyLen {
		return "", fmt.Errorf("%w: KeyLen", ErrHashParamsOutOfBounds)
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

// VerifyResult bundles the outcome of [Verify]. Matched is the
// security-critical bit — callers MUST use it to gate authentication.
// NeedsRehash is a hint that the stored parameters are weaker than
// the current target along some dimension; the recommended pattern
// is to re-hash and persist on the next successful login.
type VerifyResult struct {
	Matched     bool
	NeedsRehash bool
}

// Verify checks password against the encoded PHC string.
//
// Returns [VerifyResult]{Matched: true} when the password matches the
// stored hash, with NeedsRehash set when the stored parameters are
// weaker than target along any dimension. Returns an error for
// malformed input or when the stored parameters exceed the verifier's
// accepted bounds (see [VerifyLimits]).
//
// Matched is computed in constant time against the stored hash.
// Callers MUST use Matched to gate authentication; NeedsRehash is a
// hint, not a security boundary.
//
// Verify caps the cost parameters it is willing to feed argon2 with
// [DefaultVerifyLimits], or with caller-supplied limits via
// [WithVerifyLimits]. A stored hash that requests more memory,
// iterations, parallelism, salt length, or key length than the cap
// is rejected with [ErrParamsOutOfBounds] before any argon2 work
// runs. This prevents a corrupted row or attacker-controlled hash
// string from pinning login workers or exhausting memory.
func Verify(password, encoded string, target Params, opts ...VerifyOption) (VerifyResult, error) {
	if password == "" {
		return VerifyResult{}, ErrEmptyPassword
	}
	if len(password) > MaxPasswordLen {
		return VerifyResult{}, ErrPasswordTooLong
	}

	cfg := verifyConfig{limits: DefaultVerifyLimits()}
	for _, opt := range opts {
		if opt == nil {
			panic("passhash: Verify option must not be nil")
		}
		opt(&cfg)
	}
	cfg.limits = cfg.limits.withDefaults()
	target = target.withDefaults()

	stored, salt, hash, err := parsePHC(encoded)
	if err != nil {
		return VerifyResult{}, err
	}
	if err := stored.validatePositive(); err != nil {
		return VerifyResult{}, err
	}
	if !cfg.limits.accepts(stored) {
		return VerifyResult{}, ErrParamsOutOfBounds
	}

	candidate := argon2.IDKey([]byte(password), salt, stored.Iterations, stored.Memory, stored.Parallelism, uint32(len(hash)))
	matched := subtle.ConstantTimeCompare(candidate, hash) == 1

	return VerifyResult{
		Matched:     matched,
		NeedsRehash: matched && !stored.atLeastAsStrongAs(target),
	}, nil
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
	// Sscanf tolerates trailing garbage and non-canonical forms
	// ("v=19junk", "v=019", "v= 19"). Round-trip the parsed value
	// through the same format string and reject any input whose
	// re-encoded form does not match exactly, mirroring the params
	// segment check below.
	if expected := fmt.Sprintf("v=%d", version); expected != parts[2] {
		return Params{}, nil, nil, ErrMalformed
	}
	if version != argon2.Version {
		return Params{}, nil, nil, ErrUnsupportedFormat
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	// Sscanf tolerates trailing garbage. Round-trip the parsed values
	// through the same format string and reject if the re-encoded
	// form does not exactly match the input — that catches inputs
	// like "m=64,t=3,p=1junk" or stray whitespace that Sscanf would
	// otherwise accept.
	if expected := fmt.Sprintf("m=%d,t=%d,p=%d", p.Memory, p.Iterations, p.Parallelism); expected != parts[3] {
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
