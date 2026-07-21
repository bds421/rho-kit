package secretcrypt

import (
	"errors"
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

func TestNew_RejectsShortMaster(t *testing.T) {
	_, err := New([]byte("short"), "webhooks")
	assert.ErrorIs(t, err, ErrShortMaster)
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

func TestCrypter_ZeroValueRejected(t *testing.T) {
	var c Crypter
	_, err := c.Encrypt("id", []byte("plain"), nil)
	if !errors.Is(err, ErrInvalidCrypter) {
		t.Fatalf("zero Crypter Encrypt err = %v, want ErrInvalidCrypter", err)
	}
	_, err = (*Crypter)(nil).Decrypt("id", []byte("x"), nil)
	if !errors.Is(err, ErrInvalidCrypter) {
		t.Fatalf("nil Crypter Decrypt err = %v, want ErrInvalidCrypter", err)
	}
}

func TestCrypter_CloseZerosAndRejects(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	c, err := New(master, "label")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.Encrypt("id", []byte("secret"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Encrypt("id", []byte("x"), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Encrypt after Close: %v", err)
	}
	if _, err := c.Decrypt("id", ct, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Decrypt after Close: %v", err)
	}
	// Idempotent.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
