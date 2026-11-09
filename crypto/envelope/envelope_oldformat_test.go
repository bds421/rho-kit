package envelope

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/envelope/kekstatic"
)

// v2 blobs had a uint8 keyID length and the body AAD
// `callerAAD || "rho-kit/envelope/v2"`; nothing reads them any more.
var aadDomainSepV2 = []byte("rho-kit/envelope/v2")

func newOldFormatKEK(t *testing.T) *kekstatic.KEK {
	t.Helper()
	masterKey := make([]byte, 32)
	_, err := rand.Read(masterKey)
	require.NoError(t, err)
	k, err := kekstatic.NewKEK("v1", masterKey)
	require.NoError(t, err)
	return k
}

// sealOldFormat seals plaintext as a v2 blob would, under a fresh DEK
// wrapped by k.
func sealOldFormat(t *testing.T, k *kekstatic.KEK, plaintext, bodyAAD []byte) (keyID string, wrapped, sealed []byte) {
	t.Helper()
	dek := make([]byte, dekLen)
	_, err := rand.Read(dek)
	require.NoError(t, err)
	keyID, wrapped, err = k.Wrap(context.Background(), dek)
	require.NoError(t, err)
	bodyAEAD, err := newBodyAEAD(dek)
	require.NoError(t, err)
	sealed, err = bodyAEAD.Encrypt(plaintext, bodyAAD)
	require.NoError(t, err)
	return keyID, wrapped, sealed
}

func craftV2Blob(t *testing.T, k *kekstatic.KEK, plaintext, callerAAD []byte) []byte {
	t.Helper()
	keyID, wrapped, sealed := sealOldFormat(t, k, plaintext, append(append([]byte{}, callerAAD...), aadDomainSepV2...))
	blob := append([]byte{}, blobMagic[:]...)
	blob = append(blob, 2, byte(len(keyID)))
	blob = append(blob, keyID...)
	blob = binary.BigEndian.AppendUint16(blob, uint16(len(wrapped)))
	blob = append(blob, wrapped...)
	return append(blob, sealed...)
}

func TestAV2BlobIsRefused(t *testing.T) {
	k := newOldFormatKEK(t)
	enc := NewEncryptor(k)
	for _, aad := range [][]byte{[]byte("tenant=acme"), nil} {
		blob := craftV2Blob(t, k, []byte("payload"), aad)

		_, err := enc.Decrypt(context.Background(), blob, aad)
		require.ErrorIs(t, err, ErrUnsupportedVer)
		_, err = enc.Rewrap(context.Background(), blob)
		require.ErrorIs(t, err, ErrUnsupportedVer)
	}
}

func TestACurrentHeaderOverABodySealedWithTheV2AADFails(t *testing.T) {
	k := newOldFormatKEK(t)
	aad := []byte("tenant=acme")
	keyID, wrapped, sealed := sealOldFormat(t, k, []byte("payload"), append(append([]byte{}, aad...), aadDomainSepV2...))
	blob := append(buildHeader(keyID, wrapped), sealed...)

	_, err := NewEncryptor(k).Decrypt(context.Background(), blob, aad)
	require.ErrorIs(t, err, ErrAuthFailed)
}
