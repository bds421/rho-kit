package bcryptcompat

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/bds421/rho-kit/crypto/v2/passhash"
)

func fastParams() passhash.Params {
	return passhash.Params{
		Memory:      8 * 1024,
		Iterations:  1,
		Parallelism: 1,
		SaltLen:     16,
		KeyLen:      32,
	}
}

func TestVerify_AcceptsArgon2idHash(t *testing.T) {
	enc, err := passhash.Hash("hunter2", fastParams())
	require.NoError(t, err)

	res, err := Verify("hunter2", enc, fastParams())
	require.NoError(t, err)
	assert.True(t, res.Matched)
	assert.False(t, res.NeedsRehash)
	assert.Equal(t, AlgoArgon2id, res.Algo)
}

func TestVerify_AcceptsBcryptHash(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.DefaultCost)
	require.NoError(t, err)

	res, err := Verify("hunter2", string(hash), fastParams())
	require.NoError(t, err)
	assert.True(t, res.Matched)
	assert.True(t, res.NeedsRehash)
	assert.Equal(t, AlgoBcrypt, res.Algo)
}

func TestVerify_RejectsWrongPasswordForBcrypt(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.DefaultCost)
	require.NoError(t, err)

	res, err := Verify("wrong", string(hash), fastParams())
	require.NoError(t, err)
	assert.False(t, res.Matched)
	assert.Equal(t, AlgoBcrypt, res.Algo)
}

func TestVerify_RejectsMalformedStoredHash(t *testing.T) {
	_, err := Verify("hunter2", "not-a-hash", fastParams())
	assert.Error(t, err)
}

func TestVerify_RejectsEmptyStoredHash(t *testing.T) {
	_, err := Verify("hunter2", "", fastParams())
	assert.ErrorIs(t, err, passhash.ErrMalformed)
}

func TestVerify_RejectsEmptyPassword(t *testing.T) {
	// Use a minimal bcrypt hash shape so we exercise the top-level empty
	// guard rather than the argon2 path.
	_, err := Verify("", "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", passhash.DefaultParams())
	if !errors.Is(err, passhash.ErrEmptyPassword) {
		t.Fatalf("err = %v, want ErrEmptyPassword", err)
	}
}
