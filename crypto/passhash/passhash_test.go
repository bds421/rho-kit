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

func TestHash_DefaultsZeroParameters(t *testing.T) {
	enc, err := Hash("x", Params{})
	require.NoError(t, err)

	stored, _, _, err := parsePHC(enc)
	require.NoError(t, err)
	assert.Equal(t, DefaultParams(), stored)
}

func TestHash_DefaultsZeroSaltAndKeyLengths(t *testing.T) {
	p := fastParams()
	p.SaltLen = 0
	p.KeyLen = 0

	enc, err := Hash("x", p)
	require.NoError(t, err)

	stored, _, _, err := parsePHC(enc)
	require.NoError(t, err)
	assert.Equal(t, DefaultParams().SaltLen, stored.SaltLen)
	assert.Equal(t, DefaultParams().KeyLen, stored.KeyLen)
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

func TestVerify_ZeroTargetDefaultsToDefaultParams(t *testing.T) {
	enc, err := Hash("hunter2", fastParams())
	require.NoError(t, err)

	matched, needsRehash, err := Verify("hunter2", enc, Params{})
	require.NoError(t, err)
	assert.True(t, matched)
	assert.True(t, needsRehash)
}

func TestVerify_PartialTargetDefaultsZeroFields(t *testing.T) {
	stored := fastParams()
	enc, err := Hash("hunter2", stored)
	require.NoError(t, err)

	target := stored
	target.Memory = 0
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

func TestVerify_RejectsExcessiveMemory(t *testing.T) {
	// Hand-craft a PHC string with memory above the default 1 GiB cap.
	// We don't actually run argon2 — Verify must reject before invoking it.
	encoded := "$argon2id$v=19$m=4294967295,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ"
	matched, _, err := Verify("hunter2", encoded, fastParams())
	require.ErrorIs(t, err, ErrParamsOutOfBounds)
	assert.NotContains(t, err.Error(), "4294967295")
	assert.False(t, matched)
}

func TestVerify_RejectsExcessiveIterations(t *testing.T) {
	encoded := "$argon2id$v=19$m=8192,t=999999,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ"
	matched, _, err := Verify("hunter2", encoded, fastParams())
	require.ErrorIs(t, err, ErrParamsOutOfBounds)
	assert.NotContains(t, err.Error(), "999999")
	assert.False(t, matched)
}

func TestVerify_RejectsExcessiveParallelism(t *testing.T) {
	encoded := "$argon2id$v=19$m=8192,t=1,p=255$YWFhYWFhYWFhYWFhYWFhYQ$YQ"
	matched, _, err := Verify("hunter2", encoded, fastParams())
	require.ErrorIs(t, err, ErrParamsOutOfBounds)
	assert.NotContains(t, err.Error(), "255")
	assert.False(t, matched)
}

func TestHash_RejectsExcessiveParamsWithStableErrors(t *testing.T) {
	tests := map[string]Params{
		"memory":      {Memory: 1<<30 + 1, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		"iterations":  {Memory: 8 * 1024, Iterations: 101, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		"parallelism": {Memory: 8 * 1024, Iterations: 1, Parallelism: 17, SaltLen: 16, KeyLen: 32},
		"salt":        {Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 65, KeyLen: 32},
		"key":         {Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 65},
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Hash("hunter2", params)
			require.Error(t, err)
			for _, leak := range []string{"1073741825", "101", "17", "65", "64"} {
				assert.NotContains(t, err.Error(), leak)
			}
		})
	}
}

func TestVerify_RejectsZeroCostParametersBeforeArgon2(t *testing.T) {
	cases := []string{
		"$argon2id$v=19$m=0,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ",
		"$argon2id$v=19$m=8192,t=0,p=1$YWFhYWFhYWFhYWFhYQ$YQ",
		"$argon2id$v=19$m=8192,t=1,p=0$YWFhYWFhYWFhYWFhYQ$YQ",
	}
	for _, encoded := range cases {
		matched, _, err := Verify("hunter2", encoded, fastParams())
		require.ErrorIs(t, err, ErrInvalidParams)
		assert.False(t, matched)
	}
}

func TestVerify_RejectsZeroLengthSaltOrHash(t *testing.T) {
	cases := []string{
		"$argon2id$v=19$m=8192,t=1,p=1$$YQ",
		"$argon2id$v=19$m=8192,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$",
		"$argon2id$v=19$m=8192,t=1,p=1$$",
	}
	for _, encoded := range cases {
		matched, _, err := Verify("any-password", encoded, fastParams())
		require.ErrorIs(t, err, ErrInvalidParams)
		assert.False(t, matched)
	}
}

func TestVerify_WithVerifyLimits_OverridesDefault(t *testing.T) {
	// A hash with 100 MiB memory exceeds a tight 64 MiB caller cap but
	// is fine under the default 1 GiB cap. Confirm the option wires
	// through.
	encoded := "$argon2id$v=19$m=102400,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YQ"
	tight := VerifyLimits{MaxMemory: 64 * 1024}
	_, _, err := Verify("hunter2", encoded, fastParams(), WithVerifyLimits(tight))
	require.ErrorIs(t, err, ErrParamsOutOfBounds)
}

func TestVerify_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_, _, _ = Verify("hunter2", "not-a-hash", fastParams(), nil)
	})
}

func TestVerify_NormalHashStillVerifies(t *testing.T) {
	// Sanity: bounds enforcement must not break the round-trip case.
	enc, err := Hash("hunter2", fastParams())
	require.NoError(t, err)
	matched, _, err := Verify("hunter2", enc, fastParams())
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestVerify_RejectsOversizedEncodedString(t *testing.T) {
	huge := "$argon2id$v=19$m=8192,t=1,p=1$" + strings.Repeat("A", 8192) + "$YQ"
	_, _, err := Verify("hunter2", huge, fastParams())
	assert.ErrorIs(t, err, ErrMalformed)
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
