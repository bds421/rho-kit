package passhash

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastParams are weaker than DefaultParams so the test suite runs in
// reasonable time. Production must use DefaultParams (or stronger).
func fastParams() Params {
	return Params{
		Memory:      8 * 1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLen:     16,
		KeyLen:      32,
	}
}

func TestHash_RejectsEmptyPassword(t *testing.T) {
	_, err := Hash("", DefaultParams())
	assert.ErrorIs(t, err, ErrEmptyPassword)
}

func TestHash_RejectsZeroParameters(t *testing.T) {
	bad := fastParams()
	bad.Memory = 0
	_, err := Hash("x", bad)
	assert.Error(t, err)
}

func TestHash_PHCFormat(t *testing.T) {
	enc, err := Hash("hunter2", fastParams())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(enc, "$argon2id$v=19$"), "encoded form must start with $argon2id$v=19$, got %q", enc)
	parts := strings.Split(enc, "$")
	require.Len(t, parts, 6, "PHC string should have 6 segments")
}

func TestHash_FreshSaltEachCall(t *testing.T) {
	p := fastParams()
	a, err := Hash("hunter2", p)
	require.NoError(t, err)
	b, err := Hash("hunter2", p)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "salt must be fresh each call → encoded outputs must differ")
}

func TestVerify_AcceptsCorrectPassword(t *testing.T) {
	enc, err := Hash("hunter2", fastParams())
	require.NoError(t, err)
	matched, _, err := Verify("hunter2", enc, fastParams())
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestVerify_RejectsWrongPassword(t *testing.T) {
	enc, err := Hash("hunter2", fastParams())
	require.NoError(t, err)
	matched, _, err := Verify("hunter3", enc, fastParams())
	require.NoError(t, err)
	assert.False(t, matched)
}

func TestVerify_RejectsEmptyPassword(t *testing.T) {
	enc, err := Hash("hunter2", fastParams())
	require.NoError(t, err)
	_, _, err = Verify("", enc, fastParams())
	assert.ErrorIs(t, err, ErrEmptyPassword)
}

func TestVerify_NeedsRehashWhenStoredWeaker(t *testing.T) {
	weaker := fastParams()
	enc, err := Hash("hunter2", weaker)
	require.NoError(t, err)

	// Bump iterations on the verifier — stored params are now weaker
	// along that dimension.
	target := weaker
	target.Iterations = weaker.Iterations + 1

	matched, needsRehash, err := Verify("hunter2", enc, target)
	require.NoError(t, err)
	assert.True(t, matched)
	assert.True(t, needsRehash)
}

func TestVerify_NoRehashWhenStoredEqualOrStronger(t *testing.T) {
	p := fastParams()
	enc, err := Hash("hunter2", p)
	require.NoError(t, err)

	matched, needsRehash, err := Verify("hunter2", enc, p)
	require.NoError(t, err)
	assert.True(t, matched)
	assert.False(t, needsRehash)
}

func TestVerify_NeedsRehashOnlyWhenMatched(t *testing.T) {
	weaker := fastParams()
	enc, err := Hash("hunter2", weaker)
	require.NoError(t, err)

	target := weaker
	target.Iterations += 1

	matched, needsRehash, err := Verify("WRONG", enc, target)
	require.NoError(t, err)
	assert.False(t, matched)
	// needsRehash is meaningless when the password didn't match;
	// implementation never sets it true in that case.
	assert.False(t, needsRehash)
}

func TestVerify_MalformedRejected(t *testing.T) {
	cases := []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=8192,t=1,p=1$short", // missing hash segment
		"$bcrypt$v=19$m=8192,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ",   // wrong algorithm
		"$argon2id$v=20$m=8192,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ", // wrong version
		"$argon2id$v=19$m=BAD,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ",  // bad params
		"$argon2id$v=19$m=8192,t=1,p=1$NOT-BASE64@@@$YQ",          // bad salt b64
	}
	for _, c := range cases {
		_, _, err := Verify("hunter2", c, fastParams())
		assert.Errorf(t, err, "expected error for %q", c)
	}
}

func BenchmarkHash_FastParams(b *testing.B) {
	p := fastParams()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Hash("hunter2", p)
	}
}

func BenchmarkVerify_FastParams(b *testing.B) {
	enc, _ := Hash("hunter2", fastParams())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = Verify("hunter2", enc, fastParams())
	}
}
