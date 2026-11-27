package encryption

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

// boundarySizes are plaintext sizes around the segment boundaries.
var boundarySizes = []int64{
	0, 1, firstPlain - 1, firstPlain, firstPlain + 1,
	firstPlain + plainSegment - 1, firstPlain + plainSegment, firstPlain + plainSegment + 1,
	firstPlain + 3*plainSegment + 17,
}

func randomBytes(t *testing.T, n int64) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// stored puts plaintext through enc and returns the raw ciphertext.
func stored(t *testing.T, enc storage.Storage, backend storage.Storage, key string, plaintext []byte) []byte {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, enc.Put(ctx, key, bytes.NewReader(plaintext), storage.ObjectMeta{Size: int64(len(plaintext))}))
	rc, _, err := backend.Get(ctx, key)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	return raw
}

// replace stores raw as the object under key, bypassing encryption.
func replace(t *testing.T, backend storage.Storage, key string, raw []byte) {
	t.Helper()
	require.NoError(t, backend.Put(t.Context(), key, bytes.NewReader(raw), storage.ObjectMeta{Size: int64(len(raw))}))
}

// readAll reads key through enc; a failure in Get or in a read is err.
func readAll(t *testing.T, enc storage.Storage, key string) ([]byte, error) {
	t.Helper()
	rc, _, err := enc.Get(t.Context(), key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

func TestStreamRoundTripAndSizesAtSegmentBoundaries(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)))
	for _, n := range boundarySizes {
		plaintext := randomBytes(t, n)
		raw := stored(t, enc, backend, "obj", plaintext)
		assert.Equal(t, ciphertextSize(n), int64(len(raw)), "ciphertext size of %d", n)
		plain, ok := plaintextSize(int64(len(raw)))
		assert.True(t, ok)
		assert.Equal(t, n, plain)

		rc, meta, err := enc.Get(t.Context(), "obj")
		require.NoError(t, err)
		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		assert.Equal(t, n, meta.Size)
		assert.True(t, bytes.Equal(plaintext, got), "round trip of %d bytes", n)
	}
}

func TestPlaintextSizeRejectsImpossibleSizes(t *testing.T) {
	t.Parallel()
	for _, n := range []int64{0, headerSize, headerSize + tagSize - 1, SegmentSize + 1, 2*SegmentSize + tagSize - 1} {
		_, ok := plaintextSize(n)
		assert.False(t, ok, "%d", n)
	}
}

// fourSegments is the plaintext size of an object of four segments.
const fourSegments = firstPlain + 2*plainSegment + 100

func TestStreamRejectsTruncation(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)))
	raw := stored(t, enc, backend, "obj", randomBytes(t, fourSegments))
	require.Len(t, raw, 3*SegmentSize+100+tagSize)

	for name, length := range map[string]int{
		"empty":                  0,
		"inside the header":      headerSize - 1,
		"header only":            headerSize,
		"inside the first":       SegmentSize / 2,
		"after the first":        SegmentSize,
		"after the third":        3 * SegmentSize,
		"inside the last":        3*SegmentSize + 50,
		"last tag cut":           len(raw) - 1,
		"after a middle segment": 2 * SegmentSize,
	} {
		replace(t, backend, "obj", raw[:length])
		got, err := readAll(t, enc, "obj")
		assert.ErrorIs(t, err, ErrInvalidCiphertext, "%s: read %d bytes without error", name, len(got))
	}
}

func TestStreamRejectsReorderedSegments(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)))
	raw := stored(t, enc, backend, "obj", randomBytes(t, fourSegments))

	swapped := bytes.Clone(raw)
	copy(swapped[SegmentSize:2*SegmentSize], raw[2*SegmentSize:3*SegmentSize])
	copy(swapped[2*SegmentSize:3*SegmentSize], raw[SegmentSize:2*SegmentSize])
	replace(t, backend, "obj", swapped)
	got, err := readAll(t, enc, "obj")
	require.ErrorIs(t, err, ErrInvalidCiphertext)
	// Only the first segment, which is in place, came out.
	assert.Len(t, got, firstPlain)

	// A full segment moved to the end, where the last one belongs.
	moved := append(bytes.Clone(raw[:3*SegmentSize]), raw[SegmentSize:2*SegmentSize]...)
	replace(t, backend, "obj", moved)
	_, err = readAll(t, enc, "obj")
	assert.ErrorIs(t, err, ErrInvalidCiphertext)
}

func TestStreamRejectsTampering(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)))
	raw := stored(t, enc, backend, "obj", randomBytes(t, fourSegments))

	for name, offset := range map[string]int{
		"header length":    0,
		"salt":             5,
		"nonce prefix":     headerSize - 1,
		"first segment":    headerSize + 10,
		"middle segment":   2*SegmentSize + 10,
		"last segment tag": len(raw) - 1,
	} {
		tampered := bytes.Clone(raw)
		tampered[offset] ^= 0x01
		replace(t, backend, "obj", tampered)
		_, err := readAll(t, enc, "obj")
		assert.ErrorIs(t, err, ErrInvalidCiphertext, name)
	}

	replace(t, backend, "obj", append(bytes.Clone(raw), 0))
	_, err := readAll(t, enc, "obj")
	assert.ErrorIs(t, err, ErrInvalidCiphertext, "appended byte")
}

func TestPutEnforcesTheDeclaredSize(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)))
	ctx := t.Context()

	err := enc.Put(ctx, "long", strings.NewReader("12345"), storage.ObjectMeta{Size: 4})
	require.Error(t, err)
	err = enc.Put(ctx, "short", strings.NewReader("123"), storage.ObjectMeta{Size: 4})
	require.Error(t, err)
	for _, key := range []string{"long", "short"} {
		ok, err := backend.Exists(ctx, key)
		require.NoError(t, err)
		assert.False(t, ok, "%s stored", key)
	}
}

// earlyBackend stops reading the content after n bytes and reports success.
type earlyBackend struct {
	*membackend.Backend
	n int64
}

func (b earlyBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	return b.Backend.Put(ctx, key, io.LimitReader(r, b.n), storage.ObjectMeta{})
}

func TestPutFailsWhenTheBackendStopsEarly(t *testing.T) {
	t.Parallel()
	enc := New(earlyBackend{Backend: membackend.New(), n: 10}, StaticKey(testKey(t)))
	err := enc.Put(t.Context(), "obj", bytes.NewReader(randomBytes(t, 1000)), storage.ObjectMeta{})
	require.Error(t, err)
}

// fileBackend keeps each object in a file, so a large one does not sit in
// the test's memory.
type fileBackend struct{ dir string }

func (b fileBackend) path(key string) string { return filepath.Join(b.dir, filepath.Base(key)) }

func (b fileBackend) Put(_ context.Context, key string, r io.Reader, _ storage.ObjectMeta) error {
	f, err := os.Create(b.path(key))
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (b fileBackend) Get(_ context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	f, err := os.Open(b.path(key))
	if err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, storage.ObjectMeta{}, err
	}
	return f, storage.ObjectMeta{Size: info.Size()}, nil
}

func (b fileBackend) Delete(_ context.Context, key string) error { return os.Remove(b.path(key)) }

func (b fileBackend) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(b.path(key))
	return err == nil, nil
}

// patternReader is left bytes of block repeated, generated as read.
type patternReader struct {
	block []byte
	left  int64
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.left == 0 {
		return 0, io.EOF
	}
	n := copy(b[:min(int64(len(b)), p.left)], p.block)
	p.left -= int64(n)
	return n, nil
}

func TestOneGibibyteInBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 1 GiB through a temporary file")
	}
	const size = 1 << 30
	enc := New(fileBackend{dir: t.TempDir()}, StaticKey(testKey(t)))
	ctx := t.Context()

	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	want := sha256.New()
	src := io.TeeReader(&patternReader{block: randomBytes(t, 1<<20), left: size}, want)
	require.NoError(t, enc.Put(ctx, "big", src, storage.ObjectMeta{Size: size}))

	rc, meta, err := enc.Get(ctx, "big")
	require.NoError(t, err)
	got := sha256.New()
	n, err := io.Copy(got, rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.EqualValues(t, size, n)
	require.EqualValues(t, size, meta.Size)
	require.Equal(t, want.Sum(nil), got.Sum(nil))

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	// Everything allocated over the whole round trip, not just what is
	// live: a few segment buffers and per-segment nonces.
	allocated := after.TotalAlloc - before.TotalAlloc
	assert.Less(t, allocated, uint64(16<<20), "allocated %d bytes for a 1 GiB round trip", allocated)
}

func TestDecryptErrorKeepsBackendReadFailures(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	key := testKey(t)
	enc := New(backend, StaticKey(key))
	raw := stored(t, enc, backend, "obj", randomBytes(t, fourSegments))

	readErr := errors.New("connection reset")
	failing := New(stubBackendGetting(func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
		return io.NopCloser(io.MultiReader(bytes.NewReader(raw[:2*SegmentSize]), errReader{err: readErr})), storage.ObjectMeta{}, nil
	}), StaticKey(key))
	_, err := readAll(t, failing, "obj")
	require.ErrorIs(t, err, readErr)
	assert.NotErrorIs(t, err, ErrInvalidCiphertext)
}

func TestHeaderReadAndUnexpectedEOFAreReadErrors(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	key := testKey(t)
	enc := New(backend, StaticKey(key))
	raw := stored(t, enc, backend, "obj", randomBytes(t, fourSegments))

	readErr := errors.New("connection reset")
	for name, body := range map[string]io.Reader{
		"header read fails": io.MultiReader(bytes.NewReader(raw[:10]), errReader{err: readErr}),
		"backend reports unexpected EOF": io.MultiReader(bytes.NewReader(raw[:2*SegmentSize+5]),
			errReader{err: io.ErrUnexpectedEOF}),
	} {
		failing := New(stubBackendGetting(func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
			return io.NopCloser(body), storage.ObjectMeta{}, nil
		}), StaticKey(key))
		_, err := readAll(t, failing, "obj")
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, ErrInvalidCiphertext, name)
	}
}
