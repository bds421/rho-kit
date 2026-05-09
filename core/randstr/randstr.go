package randstr

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Pre-defined charsets exposed as string constants so importers cannot
// mutate them and corrupt token generation process-wide.
const (
	// AlphaNum contains [A-Za-z0-9].
	AlphaNum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// AlphaLowerNum contains [a-z0-9].
	AlphaLowerNum = "abcdefghijklmnopqrstuvwxyz0123456789"

	// AlphaUpperNum contains [A-Z0-9].
	AlphaUpperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// AlphaNumNoAmbiguous is AlphaNum with the visually-ambiguous runes
	// 0, O, I, l, 1 removed. Use for human-typed codes (vouchers, OTPs,
	// share-IDs printed on a coupon) where transcription errors matter.
	AlphaNumNoAmbiguous = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	// Numeric contains [0-9].
	Numeric = "0123456789"
)

// MaxLength caps how many runes a single RuneSequence call may
// generate (audit FR-043). 1 MiB worth of runes is well above any
// realistic token size and prevents user-influenced lengths from
// exhausting memory or saturating crypto/rand.
const MaxLength = 1 << 20

// RuneSequence returns a length-rune string drawn uniformly from charset.
// It returns an error from [crypto/rand.Int] when the OS RNG is unavailable;
// callers that treat that case as fatal should use [MustString] instead.
//
// Length must be non-negative and at most [MaxLength]; charset must be
// non-empty; otherwise an error is returned.
func RuneSequence(length int, charset string) (string, error) {
	if length < 0 {
		return "", fmt.Errorf("randstr: length must be non-negative, got %d", length)
	}
	if length > MaxLength {
		return "", fmt.Errorf("randstr: length %d exceeds MaxLength %d", length, MaxLength)
	}
	runes := []rune(charset)
	if len(runes) == 0 {
		return "", fmt.Errorf("randstr: charset must not be empty")
	}
	if length == 0 {
		return "", nil
	}

	// Rejection sampling: draw a uniform big.Int in [0, len(runes)) using
	// crypto/rand.Int, which already implements rejection sampling internally
	// against the next power of two. This avoids modulo-bias.
	max := big.NewInt(int64(len(runes)))
	out := make([]rune, length)
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("randstr: crypto/rand failure: %w", err)
		}
		out[i] = runes[idx.Int64()]
	}
	return string(out), nil
}

// MustString is the panicking variant of [RuneSequence]. Use only in startup
// paths, tests, or call sites where a [crypto/rand] failure is unrecoverable
// — never on the request path.
func MustString(length int, charset string) string {
	s, err := RuneSequence(length, charset)
	if err != nil {
		panic(err)
	}
	return s
}
