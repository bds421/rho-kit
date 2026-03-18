package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChecksumValidator(t *testing.T) {
	t.Parallel()

	data := []byte("hello world")
	meta := &ObjectMeta{}
	v := ChecksumValidator()

	r, err := v(bytes.NewReader(data), meta)
	require.NoError(t, err)

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	// Verify checksum was stored.
	expected := sha256.Sum256(data)
	assert.Equal(t, hex.EncodeToString(expected[:]), meta.Custom[ChecksumMetaKey])
}

func TestVerifyChecksum_Match(t *testing.T) {
	t.Parallel()

	data := []byte("test data")
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])

	rc := VerifyChecksum(io.NopCloser(bytes.NewReader(data)), expected)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	require.NoError(t, rc.Close())
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	t.Parallel()

	data := []byte("original")
	rc := VerifyChecksum(io.NopCloser(bytes.NewReader(data)), "0000000000000000000000000000000000000000000000000000000000000000")

	_, err := io.ReadAll(rc)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestVerifyChecksum_EmptyExpected(t *testing.T) {
	t.Parallel()

	data := []byte("anything")
	rc := VerifyChecksum(io.NopCloser(bytes.NewReader(data)), "")

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}
