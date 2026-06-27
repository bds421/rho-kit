package secretcrypt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMaster() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func TestNew_RejectsEmptyMaster(t *testing.T) {
	_, err := New(nil, "webhooks")
	assert.ErrorIs(t, err, ErrEmptyMaster)
}

func TestNew_RejectsEmptyDomainLabel(t *testing.T) {
	_, err := New(testMaster(), "")
	assert.ErrorIs(t, err, ErrEmptyDomainLabel)
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	c, err := New(testMaster(), "webhooks")
	require.NoError(t, err)

	plain := []byte("signing-secret")
	aad := []byte("tenant-acme")
	blob, err := c.Encrypt("key-1", plain, aad)
	require.NoError(t, err)

	got, err := c.Decrypt("key-1", blob, aad)
	require.NoError(t, err)
	assert.Equal(t, plain, got)
}

func TestDecrypt_RejectsWrongIdentity(t *testing.T) {
	c, err := New(testMaster(), "webhooks")
	require.NoError(t, err)

	blob, err := c.Encrypt("key-1", []byte("secret"), []byte("tenant-a"))
	require.NoError(t, err)

	_, err = c.Decrypt("key-2", blob, []byte("tenant-a"))
	assert.Error(t, err)
}

func TestDecrypt_RejectsWrongAAD(t *testing.T) {
	c, err := New(testMaster(), "webhooks")
	require.NoError(t, err)

	blob, err := c.Encrypt("key-1", []byte("secret"), []byte("tenant-a"))
	require.NoError(t, err)

	_, err = c.Decrypt("key-1", blob, []byte("tenant-b"))
	assert.Error(t, err)
}

func TestEncrypt_RejectsEmptyIdentity(t *testing.T) {
	c, err := New(testMaster(), "webhooks")
	require.NoError(t, err)

	_, err = c.Encrypt("", []byte("x"), nil)
	assert.ErrorIs(t, err, ErrEmptyIdentity)
}