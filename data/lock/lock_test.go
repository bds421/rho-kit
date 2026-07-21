package lock_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/lock"
)

func TestValidateKey(t *testing.T) {
	require.NoError(t, lock.ValidateKey("user-42"))
	assert.ErrorIs(t, lock.ValidateKey(""), lock.ErrKeyEmpty)
	assert.ErrorIs(t, lock.ValidateKey(strings.Repeat("x", lock.MaxKeyLen+1)), lock.ErrKeyTooLong)
	assert.ErrorIs(t, lock.ValidateKey("has space"), lock.ErrKeyInvalidChars)
	assert.ErrorIs(t, lock.ValidateKey("has\nnewline"), lock.ErrKeyInvalidChars)
	assert.ErrorIs(t, lock.ValidateKey(string([]byte{0xff, 0xfe})), lock.ErrKeyInvalidChars)
	require.NoError(t, lock.ValidateKey(strings.Repeat("x", lock.MaxKeyLen)))
}
