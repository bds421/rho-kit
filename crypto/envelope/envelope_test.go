package envelope_test

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/envelope"
	"github.com/bds421/rho-kit/crypto/envelope/kekstatic"
)

func newKEK(t *testing.T, keyID string) *kekstatic.KEK {
	t.Helper()
	mk := make([]byte, 32)
	_, err := rand.Read(mk)
	require.NoError(t, err)
	k, err := kekstatic.New(keyID, mk)
	require.NoError(t, err)
	return k
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)

	pt := []byte("hello world")
	blob, err := enc.Encrypt(context.Background(), pt, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), string(pt))

	got, err := enc.Decrypt(context.Background(), blob, nil)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}

func TestEncryptDecrypt_AADBound(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), []byte("tenant=acme"))
	require.NoError(t, err)

	// Correct AAD → succeeds.
	pt, err := enc.Decrypt(context.Background(), blob, []byte("tenant=acme"))
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), pt)

	// Wrong AAD → fails closed.
	_, err = enc.Decrypt(context.Background(), blob, []byte("tenant=evil"))
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)

	// Missing AAD → fails closed.
	_, err = enc.Decrypt(context.Background(), blob, nil)
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
}

func TestEncryptDecrypt_TamperedHeaderRejected(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Flip a bit in the keyID byte (offset 5+).
	blob[5] ^= 0x01
	_, err = enc.Decrypt(context.Background(), blob, nil)
	require.Error(t, err) // either unknown keyID or auth-failed
}

func TestEncryptDecrypt_EmptyPlaintextRoundTrips(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)

	blob, err := enc.Encrypt(context.Background(), nil, nil)
	require.NoError(t, err)

	got, err := enc.Decrypt(context.Background(), blob, nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	blob2, err := enc.Encrypt(context.Background(), []byte{}, []byte("aad"))
	require.NoError(t, err)
	got2, err := enc.Decrypt(context.Background(), blob2, []byte("aad"))
	require.NoError(t, err)
	assert.Empty(t, got2)
}

func TestDecrypt_RejectsTruncated(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	_, err = enc.Decrypt(context.Background(), blob[:8], nil)
	assert.ErrorIs(t, err, envelope.ErrTruncated)
}

func TestDecrypt_RejectsBadMagic(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)
	_, err := enc.Decrypt(context.Background(), []byte("not-an-envelope-blob"), nil)
	assert.ErrorIs(t, err, envelope.ErrMalformed)
}

func TestDecrypt_RejectsWrongVersion(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)
	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Bump version byte.
	blob[3] = 99
	_, err = enc.Decrypt(context.Background(), blob, nil)
	assert.ErrorIs(t, err, envelope.ErrUnsupportedVer)
}

func TestRotation_OldBlobsStillReadable(t *testing.T) {
	mk1 := make([]byte, 32)
	_, _ = rand.Read(mk1)
	mk2 := make([]byte, 32)
	_, _ = rand.Read(mk2)

	k, err := kekstatic.New("v1", mk1)
	require.NoError(t, err)
	enc := envelope.New(k)

	// Encrypt under v1.
	blobV1, err := enc.Encrypt(context.Background(), []byte("legacy"), nil)
	require.NoError(t, err)

	// Add v2 and rotate.
	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	// New writes use v2.
	blobV2, err := enc.Encrypt(context.Background(), []byte("fresh"), nil)
	require.NoError(t, err)
	assert.NotEqual(t, blobV1, blobV2)

	// Both still decrypt — v1 is still registered.
	got1, err := enc.Decrypt(context.Background(), blobV1, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("legacy"), got1)

	got2, err := enc.Decrypt(context.Background(), blobV2, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("fresh"), got2)
}

func TestRewrap_RewrapsUnderActiveKey(t *testing.T) {
	mk1 := make([]byte, 32)
	_, _ = rand.Read(mk1)
	mk2 := make([]byte, 32)
	_, _ = rand.Read(mk2)

	k, _ := kekstatic.New("v1", mk1)
	enc := envelope.New(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Rotate to v2.
	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	rewrapped, err := enc.Rewrap(context.Background(), blob)
	require.NoError(t, err)

	// Remove v1 — rewrapped must still decrypt under v2.
	k.RemoveKey("v1")
	got, err := enc.Decrypt(context.Background(), rewrapped, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), got)
}

func TestRewrap_PreservesAADBinding(t *testing.T) {
	mk1 := make([]byte, 32)
	_, _ = rand.Read(mk1)
	mk2 := make([]byte, 32)
	_, _ = rand.Read(mk2)

	k, _ := kekstatic.New("v1", mk1)
	enc := envelope.New(k)

	aad := []byte("tenant=acme,row=42")
	blob, err := enc.Encrypt(context.Background(), []byte("secret"), aad)
	require.NoError(t, err)

	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	rewrapped, err := enc.Rewrap(context.Background(), blob)
	require.NoError(t, err)

	// Body ciphertext bytes must be unchanged — Rewrap rewrites only
	// the wrap header.
	_, _, _, oldBody := splitForTest(t, blob)
	_, _, _, newBody := splitForTest(t, rewrapped)
	assert.Equal(t, oldBody, newBody, "Rewrap must not touch nonce+ciphertext")

	// Same AAD still decrypts under the new wrap key.
	k.RemoveKey("v1")
	got, err := enc.Decrypt(context.Background(), rewrapped, aad)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got)

	// Wrong/missing AAD still fails closed after rewrap.
	_, err = enc.Decrypt(context.Background(), rewrapped, []byte("tenant=evil"))
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
	_, err = enc.Decrypt(context.Background(), rewrapped, nil)
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
}

func TestRewrap_RejectsMalformedBlob(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.New(k)
	_, err := enc.Rewrap(context.Background(), []byte("not-an-envelope"))
	assert.Error(t, err)
}

// splitForTest mirrors the wire format and returns the body suffix
// (nonce || ciphertext+tag) so tests can compare it across rewraps.
func splitForTest(t *testing.T, blob []byte) (magic byte, version byte, header []byte, body []byte) {
	t.Helper()
	require.GreaterOrEqual(t, len(blob), 5)
	kL := int(blob[4])
	off := 5 + kL
	require.GreaterOrEqual(t, len(blob), off+2)
	wL := int(blob[off])<<8 | int(blob[off+1])
	off += 2 + wL
	require.GreaterOrEqual(t, len(blob), off)
	return blob[0], blob[3], blob[:off], blob[off:]
}

func TestKEKStatic_RemoveActiveKeyPanics(t *testing.T) {
	k := newKEK(t, "v1")
	assert.Panics(t, func() { k.RemoveKey("v1") })
}

func TestKEKStatic_UnknownKeyIDRejected(t *testing.T) {
	k := newKEK(t, "v1")
	_, err := k.Unwrap(context.Background(), "v999", []byte("garbage"))
	assert.Error(t, err)
}

func TestKEKStatic_KeyIDBoundAsAAD(t *testing.T) {
	mk := make([]byte, 32)
	_, err := rand.Read(mk)
	require.NoError(t, err)

	k, err := kekstatic.New("keyA", mk)
	require.NoError(t, err)
	require.NoError(t, k.AddKey("keyB", mk))

	enc := envelope.New(k)
	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Tamper the blob's keyID from "keyA" to "keyB". Layout:
	// magic(3) || version(1) || keyIDLen(1) || keyID(...) || ...
	require.Equal(t, byte(4), blob[4], "expected keyID length 4")
	require.Equal(t, byte('A'), blob[8])
	blob[8] = 'B'

	_, err = enc.Decrypt(context.Background(), blob, nil)
	assert.Error(t, err, "swapped keyID must fail to unwrap because keyID is bound as AAD")
}
