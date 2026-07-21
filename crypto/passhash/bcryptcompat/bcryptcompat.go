// Package bcryptcompat verifies legacy bcrypt password hashes alongside
// [github.com/bds421/rho-kit/crypto/v2/passhash] PHC strings. Consumers
// migrating from bcrypt to argon2id call [Verify] at login: matched bcrypt
// hashes return NeedsRehash=true so the caller can persist a fresh PHC hash.
package bcryptcompat

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/bds421/rho-kit/crypto/v2/passhash"
)

// Algo names the hash algorithm that verified the presented password.
type Algo string

const (
	// AlgoArgon2id means the stored hash is a passhash PHC argon2id string.
	AlgoArgon2id Algo = "argon2id"
	// AlgoBcrypt means the stored hash is a legacy bcrypt string.
	AlgoBcrypt Algo = "bcrypt"
)

// VerifyResult bundles the outcome of [Verify].
type VerifyResult struct {
	Matched     bool
	NeedsRehash bool
	Algo        Algo
}

// Verify checks password against stored. Legacy bcrypt hashes ($2a$, $2b$,
// $2y$) are detected by prefix and verified with bcrypt; all other strings
// are treated as passhash PHC argon2id. target is the argon2id policy used
// to decide NeedsRehash for matched PHC hashes; bcrypt matches always set
// NeedsRehash=true so callers can upgrade on next login.
//
// Empty passwords are rejected with [passhash.ErrEmptyPassword] for both
// algorithms (same contract as [passhash.Verify]). The bcrypt path is
// limited by bcrypt itself to the first 72 bytes of the password; longer
// secrets that differ only after byte 72 will match. Matched bcrypt rows
// always return NeedsRehash=true so the upgrade path heals this.
func Verify(password, stored string, target passhash.Params) (VerifyResult, error) {
	if password == "" {
		return VerifyResult{}, passhash.ErrEmptyPassword
	}
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return VerifyResult{}, passhash.ErrMalformed
	}
	if isBcryptHash(stored) {
		return verifyBcrypt(password, stored)
	}
	res, err := passhash.Verify(password, stored, target)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{
		Matched:     res.Matched,
		NeedsRehash: res.NeedsRehash,
		Algo:        AlgoArgon2id,
	}, nil
}

func verifyBcrypt(password, stored string) (VerifyResult, error) {
	// bcrypt only consumes the first 72 bytes (Blowfish key schedule);
	// documented on Verify. MaxPasswordLen still caps DoS from huge inputs.
	if len(password) > passhash.MaxPasswordLen {
		return VerifyResult{}, passhash.ErrPasswordTooLong
	}
	err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return VerifyResult{Algo: AlgoBcrypt}, nil
		}
		return VerifyResult{}, fmt.Errorf("bcryptcompat: verify bcrypt: %w", err)
	}
	return VerifyResult{
		Matched:     true,
		NeedsRehash: true,
		Algo:        AlgoBcrypt,
	}, nil
}

// isBcryptHash reports whether stored is a bcrypt PHC string. Only $2a$,
// $2b$, and $2y$ variants are recognized; other legacy prefixes (e.g. $2$)
// are not supported.
func isBcryptHash(stored string) bool {
	return strings.HasPrefix(stored, "$2a$") ||
		strings.HasPrefix(stored, "$2b$") ||
		strings.HasPrefix(stored, "$2y$")
}
