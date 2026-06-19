package envelope

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/envelope/kekstatic"
)

// craftV2Blob hand-builds a legacy v2 envelope blob using the same KEK
// the test will later Decrypt against. The v2 layout is intentionally
// frozen on the read path so blobs written before the v3 upgrade keep
// decrypting unchanged.
func craftV2Blob(t *testing.T, k *kekstatic.KEK, plaintext, callerAAD []byte) []byte {
	t.Helper()

	dek := make([]byte, dekLen)
	_, err := rand.Read(dek)
	require.NoError(t, err)

	keyID, wrapped, err := k.Wrap(context.Background(), dek)
	require.NoError(t, err)
	require.LessOrEqual(t, len(keyID), 0xFF, "v2 uses a uint8 keyID length prefix")
	require.LessOrEqual(t, len(wrapped), 0xFFFF)

	bodyAEAD, err := newBodyAEAD(dek)
	require.NoError(t, err)

	// v2 AAD: callerAAD || aadDomainSepV2
	v2AAD := append(append([]byte{}, callerAAD...), aadDomainSepV2...)
	sealed, err := bodyAEAD.Encrypt(plaintext, v2AAD)
	require.NoError(t, err)

	// v2 header: magic(3) || version(1) || kL(uint8) || keyID || wL(uint16 BE) || wrapped
	blob := make([]byte, 0, 3+1+1+len(keyID)+2+len(wrapped)+len(sealed))
	blob = append(blob, blobMagic[:]...)
	blob = append(blob, blobVersionV2)
	blob = append(blob, byte(len(keyID)))
	blob = append(blob, []byte(keyID)...)
	var lp [2]byte
	binary.BigEndian.PutUint16(lp[:], uint16(len(wrapped)))
	blob = append(blob, lp[:]...)
	blob = append(blob, wrapped...)
	blob = append(blob, sealed...)
	return blob
}

func TestDecrypt_V2BlobRoundTrips(t *testing.T) {
	masterKey := make([]byte, 32)
	_, err := rand.Read(masterKey)
	require.NoError(t, err)
	k, err := kekstatic.NewKEK("v1", masterKey)
	require.NoError(t, err)

	enc := NewEncryptor(k)
	pt := []byte("legacy v2 payload")
	aad := []byte("tenant=acme")

	blob := craftV2Blob(t, k, pt, aad)
	require.Equal(t, blobVersionV2, blob[3], "crafted blob must report v2")

	got, err := enc.Decrypt(context.Background(), blob, aad)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}

func TestDecrypt_V2BlobNoAADRoundTrips(t *testing.T) {
	masterKey := make([]byte, 32)
	_, err := rand.Read(masterKey)
	require.NoError(t, err)
	k, err := kekstatic.NewKEK("v1", masterKey)
	require.NoError(t, err)

	enc := NewEncryptor(k)
	pt := []byte("payload-no-aad")

	blob := craftV2Blob(t, k, pt, nil)
	got, err := enc.Decrypt(context.Background(), blob, nil)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}

func TestDecrypt_V3BlobSealedWithV2AADFails(t *testing.T) {
	// A v3 blob whose body was sealed with v2 AAD (caller || domainSepV2)
	// must NOT decrypt under v3 AAD (domainSepV3 || uvarint(len) || caller).
	// Domain separation must keep the two formats cryptographically
	// distinct even when callers supply the same callerAAD.
	masterKey := make([]byte, 32)
	_, err := rand.Read(masterKey)
	require.NoError(t, err)
	k, err := kekstatic.NewKEK("v1", masterKey)
	require.NoError(t, err)

	enc := NewEncryptor(k)
	aad := []byte("tenant=acme")
	pt := []byte("payload")

	dek := make([]byte, dekLen)
	_, err = rand.Read(dek)
	require.NoError(t, err)
	keyID, wrapped, err := k.Wrap(context.Background(), dek)
	require.NoError(t, err)

	bodyAEAD, err := newBodyAEAD(dek)
	require.NoError(t, err)
	sealed, err := bodyAEAD.Encrypt(pt, combineAAD(blobVersionV2, aad))
	require.NoError(t, err)

	header := buildHeader(keyID, wrapped)
	blob := append(append([]byte{}, header...), sealed...)
	require.Equal(t, blobVersionV3, blob[3])

	_, err = enc.Decrypt(context.Background(), blob, aad)
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestRewrap_V2BlobStaysDecryptable(t *testing.T) {
	// Rewrapping a legacy v2 blob must keep it decryptable: the body
	// stays sealed under the v2 AAD, so the rewrapped header must remain
	// v2. Emitting a v3 header would change the AAD derivation at decrypt
	// time and permanently corrupt the record.
	mk1 := make([]byte, 32)
	_, err := rand.Read(mk1)
	require.NoError(t, err)
	mk2 := make([]byte, 32)
	_, err = rand.Read(mk2)
	require.NoError(t, err)

	k, err := kekstatic.NewKEK("v1", mk1)
	require.NoError(t, err)
	enc := NewEncryptor(k)

	pt := []byte("legacy v2 payload")
	aad := []byte("tenant=acme")
	blob := craftV2Blob(t, k, pt, aad)
	require.Equal(t, blobVersionV2, blob[3], "crafted blob must report v2")

	// Rotate the KEK and rewrap under the new active key.
	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	rewrapped, err := enc.Rewrap(context.Background(), blob)
	require.NoError(t, err)
	require.Equal(t, blobVersionV2, rewrapped[3], "rewrapped v2 blob must stay v2")

	// Remove the old key so decryption must use the new wrap.
	require.NoError(t, k.RemoveKey("v1"))
	got, err := enc.Decrypt(context.Background(), rewrapped, aad)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}

func TestRewrap_V2BlobNoAADStaysDecryptable(t *testing.T) {
	mk1 := make([]byte, 32)
	_, err := rand.Read(mk1)
	require.NoError(t, err)
	mk2 := make([]byte, 32)
	_, err = rand.Read(mk2)
	require.NoError(t, err)

	k, err := kekstatic.NewKEK("v1", mk1)
	require.NoError(t, err)
	enc := NewEncryptor(k)

	pt := []byte("payload-no-aad")
	blob := craftV2Blob(t, k, pt, nil)
	require.Equal(t, blobVersionV2, blob[3])

	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	rewrapped, err := enc.Rewrap(context.Background(), blob)
	require.NoError(t, err)
	require.Equal(t, blobVersionV2, rewrapped[3], "rewrapped v2 blob must stay v2")

	require.NoError(t, k.RemoveKey("v1"))
	got, err := enc.Decrypt(context.Background(), rewrapped, nil)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}
