package encryption

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/infra/storage/membackend"
)

func testKey() []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return key
}

func TestEncryptedStorage_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey()
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	original := []byte("secret data that must be encrypted")
	err := enc.Put(ctx, "secret.txt", bytes.NewReader(original), storage.ObjectMeta{
		ContentType: "text/plain",
	})
	require.NoError(t, err)

	// Verify the stored data is NOT plaintext.
	rc, _, err := backend.Get(ctx, "secret.txt")
	require.NoError(t, err)
	stored, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.NotEqual(t, original, stored, "stored data should be encrypted")

	// Decrypt and verify.
	rc, meta, err := enc.Get(ctx, "secret.txt")
	require.NoError(t, err)
	decrypted, _ := io.ReadAll(rc)
	_ = rc.Close()

	assert.Equal(t, original, decrypted)
	assert.Equal(t, int64(len(original)), meta.Size)
}

func TestEncryptedStorage_WrongKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key1 := testKey()
	key2 := testKey()
	backend := membackend.New()

	enc1 := New(backend, StaticKey(key1))
	enc2 := New(backend, StaticKey(key2))

	err := enc1.Put(ctx, "secret.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	// Decrypting with wrong key should fail.
	_, _, err = enc2.Get(ctx, "secret.txt")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt")
}

func TestEncryptedStorage_ExistsAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey()
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	err := enc.Put(ctx, "file.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	ok, err := enc.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, ok)

	err = enc.Delete(ctx, "file.txt")
	require.NoError(t, err)

	ok, err = enc.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEncryptedStorage_EmptyContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey()
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	err := enc.Put(ctx, "empty.txt", bytes.NewReader(nil), storage.ObjectMeta{})
	require.NoError(t, err)

	rc, _, err := enc.Get(ctx, "empty.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Empty(t, data)
}

func TestStaticKey_PanicsOnWrongSize(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { StaticKey([]byte("short")) })
}
