package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChecksumValidator(t *testing.T) {
	t.Parallel()

	data := []byte("hello world")
	meta := &ObjectMeta{}
	v := ChecksumValidator()

	r, err := v(context.Background(), bytes.NewReader(data), meta)
	require.NoError(t, err)

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	// Verify checksum was stored.
	expected := sha256.Sum256(data)
	assert.Equal(t, hex.EncodeToString(expected[:]), meta.Custom[ChecksumMetaKey])
}

func TestChecksumValidator_RejectsNonSeekableReader(t *testing.T) {
	t.Parallel()

	meta := &ObjectMeta{}
	v := ChecksumValidator()
	_, err := v(context.Background(), io.LimitReader(bytes.NewReader([]byte("hello")), 5), meta)
	assert.ErrorIs(t, err, ErrValidation)
}

func TestChecksumValidator_DetachesCustomMetadata(t *testing.T) {
	t.Parallel()

	custom := map[string]string{"owner": "alice"}
	meta := &ObjectMeta{Custom: custom}
	v := ChecksumValidator()

	_, err := v(context.Background(), bytes.NewReader([]byte("hello")), meta)
	require.NoError(t, err)

	meta.Custom["owner"] = "bob"
	assert.Equal(t, "alice", custom["owner"])
	assert.NotContains(t, custom, ChecksumMetaKey)
}

func TestChecksumValidator_RewindsToOriginalOffset(t *testing.T) {
	t.Parallel()

	r := bytes.NewReader([]byte("prefix-body"))
	_, err := r.Seek(7, io.SeekStart)
	require.NoError(t, err)

	meta := &ObjectMeta{}
	v := ChecksumValidator()
	out, err := v(context.Background(), r, meta)
	require.NoError(t, err)

	got, err := io.ReadAll(out)
	require.NoError(t, err)
	assert.Equal(t, []byte("body"), got)

	expected := sha256.Sum256([]byte("body"))
	assert.Equal(t, hex.EncodeToString(expected[:]), meta.Custom[ChecksumMetaKey])
}

func TestChecksumValidator_PropagatesSeekError(t *testing.T) {
	t.Parallel()

	meta := &ObjectMeta{}
	v := ChecksumValidator()
	_, err := v(context.Background(), errorReadSeeker{}, meta)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrValidation))
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
	rc := VerifyChecksum(io.NopCloser(bytes.NewReader(data)), "secret-token")

	_, err := io.ReadAll(rc)
	assert.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestVerifyChecksum_EmptyExpected(t *testing.T) {
	t.Parallel()

	data := []byte("anything")
	rc := VerifyChecksum(io.NopCloser(bytes.NewReader(data)), "")

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// eofAfterDataReader delivers all data in the first Read, then signals EOF on a
// separate zero-byte Read. This forces verifyReader to detect a checksum
// mismatch on a Read where n == 0.
type eofAfterDataReader struct {
	data []byte
	done bool
}

func (r *eofAfterDataReader) Read(p []byte) (int, error) {
	if !r.done {
		n := copy(p, r.data)
		r.data = r.data[n:]
		if len(r.data) == 0 {
			r.done = true
		}
		return n, nil
	}
	return 0, io.EOF
}

func (r *eofAfterDataReader) Close() error { return nil }

func TestVerifyChecksum_MismatchOnZeroByteEOFKeepsReportingError(t *testing.T) {
	t.Parallel()

	// The reader hands back all bytes first, then EOF with n == 0, so the
	// mismatch is detected on a zero-byte Read.
	rc := VerifyChecksum(&eofAfterDataReader{data: []byte("payload")}, "deadbeef")

	buf := make([]byte, 16)
	n, err := rc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), buf[:n])

	// The EOF Read detects the mismatch and must report it.
	_, err = rc.Read(buf)
	require.ErrorIs(t, err, ErrValidation)

	// Critical regression guard: a retrying caller must keep seeing the error,
	// never (0, nil), which would stall with no progress.
	_, err = rc.Read(buf)
	require.ErrorIs(t, err, ErrValidation)
}

func TestVerifyChecksum_AcceptsUppercaseExpectedDigest(t *testing.T) {
	t.Parallel()

	data := []byte("case insensitive")
	sum := sha256.Sum256(data)
	upper := strings.ToUpper(hex.EncodeToString(sum[:]))

	rc := VerifyChecksum(io.NopCloser(bytes.NewReader(data)), upper)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	require.NoError(t, rc.Close())
}

type errorReadSeeker struct{}

func (errorReadSeeker) Read([]byte) (int, error) { return 0, io.EOF }

func (errorReadSeeker) Seek(int64, int) (int64, error) {
	return 0, errors.New("seek failed")
}
