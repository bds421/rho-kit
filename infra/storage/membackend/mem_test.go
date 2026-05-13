package membackend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestMemBackend_PutAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	content := []byte("hello world")
	err := b.Put(ctx, "test.txt", bytes.NewReader(content), storage.ObjectMeta{ContentType: "text/plain"})
	require.NoError(t, err)

	rc, meta, err := b.Get(ctx, "test.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, content, got)
	assert.Equal(t, "text/plain", meta.ContentType)
	assert.Equal(t, int64(11), meta.Size)
}

func TestMemBackend_PutClosesValidatorOwnedReader(t *testing.T) {
	t.Parallel()
	closed := false
	b := New(func(context.Context, io.Reader, *storage.ObjectMeta) (io.Reader, error) {
		return &trackingReadCloser{Reader: strings.NewReader("validated"), closed: &closed}, nil
	})

	err := b.Put(context.Background(), "validated.txt", strings.NewReader("original"), storage.ObjectMeta{})
	require.NoError(t, err)
	assert.True(t, closed)

	rc, _, err := b.Get(context.Background(), "validated.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, []byte("validated"), got)
}

func TestMemBackend_PutDoesNotCloseCallerOwnedReaderWithoutValidators(t *testing.T) {
	t.Parallel()
	closed := false
	r := &trackingReadCloser{Reader: strings.NewReader("caller-owned"), closed: &closed}

	err := New().Put(context.Background(), "caller-owned.txt", r, storage.ObjectMeta{})
	require.NoError(t, err)
	assert.False(t, closed)
}

func TestMemBackend_PutReadErrorDoesNotReflectCause(t *testing.T) {
	t.Parallel()
	readErr := errors.New("read failed for secret-token")

	err := New().Put(context.Background(), "reader.txt", errReader{err: readErr}, storage.ObjectMeta{})

	require.Error(t, err)
	assert.ErrorIs(t, err, readErr)
	assert.Contains(t, err.Error(), "membackend: read content")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "read failed")
}

func TestNew_PanicsOnNilValidator(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		New(nil)
	})
}

func TestMemBackend_PutRejectsNilReader(t *testing.T) {
	t.Parallel()
	err := New().Put(context.Background(), "nil.txt", nil, storage.ObjectMeta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, storage.ErrValidation)
}

func TestMemBackend_GetNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	_, _, err := b.Get(ctx, "secret-token.txt")
	assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestMemBackend_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	assert.NoError(t, b.Delete(ctx, "nonexistent.txt"))
}

func TestMemBackend_Exists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	ok, err := b.Exists(ctx, "missing.txt")
	require.NoError(t, err)
	assert.False(t, ok)

	err = b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
	require.NoError(t, err)

	ok, err = b.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMemBackend_Copy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	err := b.Put(ctx, "src.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	err = b.Copy(ctx, "src.txt", "dst.txt")
	require.NoError(t, err)

	rc, _, err := b.Get(ctx, "dst.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got, _ := io.ReadAll(rc)
	assert.Equal(t, []byte("data"), got)
}

func TestMemBackend_CopyNotFoundDoesNotReflectSourceKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	err := b.Copy(ctx, "secret-token.txt", "dst.txt")

	assert.ErrorIs(t, err, storage.ErrObjectNotFound)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestMemBackend_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	for _, key := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		err := b.Put(ctx, key, bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.NoError(t, err)
	}

	var keys []string
	for info, err := range b.List(ctx, "sub/", storage.ListOptions{}) {
		require.NoError(t, err)
		keys = append(keys, info.Key)
	}
	assert.Equal(t, []string{"sub/c.txt"}, keys)
}

func TestMemBackend_ListMaxKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
		err := b.Put(ctx, key, bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.NoError(t, err)
	}

	var keys []string
	for info, err := range b.List(ctx, "", storage.ListOptions{MaxKeys: 2}) {
		require.NoError(t, err)
		keys = append(keys, info.Key)
	}
	assert.Len(t, keys, 2)
}

func TestMemBackend_ListRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	b := New()

	var seenErr error
	for _, err := range b.List(context.Background(), "", storage.ListOptions{StartAfter: "bad key"}) {
		seenErr = err
		break
	}

	require.ErrorIs(t, seenErr, storage.ErrValidation)
	assert.Equal(t, 0, b.Len())
}

func TestMemBackend_LenAndReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	err := b.Put(ctx, "a.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
	require.NoError(t, err)
	assert.Equal(t, 1, b.Len())

	b.Reset()
	assert.Equal(t, 0, b.Len())
}

func TestMemBackend_ImmutableStorage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	original := []byte("original")
	err := b.Put(ctx, "file.txt", bytes.NewReader(original), storage.ObjectMeta{})
	require.NoError(t, err)

	// Get data and mutate the returned bytes.
	rc, _, err := b.Get(ctx, "file.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	data[0] = 'X'

	// Verify stored data is unchanged.
	rc2, _, err := b.Get(ctx, "file.txt")
	require.NoError(t, err)
	defer func() { _ = rc2.Close() }()
	data2, _ := io.ReadAll(rc2)
	assert.Equal(t, original, data2)
}

func TestMemBackend_CustomMetadataIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	meta := storage.ObjectMeta{Custom: map[string]string{"owner": "alice"}}
	require.NoError(t, b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), meta))

	meta.Custom["owner"] = "mallory"

	_, gotMeta, err := b.Get(ctx, "file.txt")
	require.NoError(t, err)
	assert.Equal(t, "alice", gotMeta.Custom["owner"])

	gotMeta.Custom["owner"] = "bob"
	_, gotMetaAgain, err := b.Get(ctx, "file.txt")
	require.NoError(t, err)
	assert.Equal(t, "alice", gotMetaAgain.Custom["owner"])
}

func TestMemBackend_InvalidMetadataRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	err := b.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{
		Custom: map[string]string{"bad\nkey": "value"},
	})
	assert.ErrorIs(t, err, storage.ErrValidation)
	assert.Equal(t, 0, b.Len())
}

func TestMemBackend_EmptyKeyRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := New()

	assert.Error(t, b.Put(ctx, "", bytes.NewReader(nil), storage.ObjectMeta{}))

	_, _, err := b.Get(ctx, "")
	assert.Error(t, err)

	assert.Error(t, b.Delete(ctx, ""))

	_, err = b.Exists(ctx, "")
	assert.Error(t, err)
}

// TestHonorsCancelledContext pins M-005: a cancelled ctx must return
// ctx.Err() from every storage operation, so memory wiring agrees with
// remote backends about cancellation semantics.
func TestHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := New()

	require.ErrorIs(t, b.Put(ctx, "k", bytes.NewReader([]byte("v")), storage.ObjectMeta{}), context.Canceled)

	_, _, err := b.Get(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)

	require.ErrorIs(t, b.Delete(ctx, "k"), context.Canceled)

	_, err = b.Exists(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)

	require.ErrorIs(t, b.Copy(ctx, "k", "k2"), context.Canceled)

	var listErr error
	for _, e := range b.List(ctx, "", storage.ListOptions{}) {
		listErr = e
		break
	}
	require.ErrorIs(t, listErr, context.Canceled)

	// Sanity: state is untouched.
	require.Equal(t, 0, b.Len())
}

type trackingReadCloser struct {
	io.Reader
	closed *bool
}

func (r *trackingReadCloser) Close() error {
	*r.closed = true
	return nil
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}
