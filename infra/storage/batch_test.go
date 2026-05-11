package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteMany_Sequential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &inlineBackend{store: make(map[string][]byte)}

	// Seed data.
	backend.store["a.txt"] = []byte("a")
	backend.store["b.txt"] = []byte("b")
	backend.store["c.txt"] = []byte("c")

	err := DeleteMany(ctx, backend, []string{"a.txt", "c.txt"})
	require.NoError(t, err)

	assert.Len(t, backend.store, 1)
	assert.Contains(t, backend.store, "b.txt")
}

func TestDeleteMany_SequentialErrorDoesNotReflectKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &deleteFailBackend{inlineBackend: &inlineBackend{store: make(map[string][]byte)}}

	err := DeleteMany(ctx, backend, []string{"secret-token.txt"})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestDeleteMany_InvalidKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &inlineBackend{store: make(map[string][]byte)}

	err := DeleteMany(ctx, backend, []string{"valid.txt", ""})
	assert.Error(t, err)
}

func TestDeleteMany_RejectsTooManyKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &batchProbe{}
	keys := make([]string, MaxBatchKeys+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("object-%d.txt", i)
	}

	err := DeleteMany(ctx, backend, keys)

	assert.ErrorIs(t, err, ErrValidation)
	assert.ErrorIs(t, err, ErrBatchTooLarge)
	assert.False(t, backend.called)
}

func TestDeleteMany_NilBackendReturnsError(t *testing.T) {
	t.Parallel()

	err := DeleteMany(context.Background(), nil, []string{"valid.txt"})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backend is required")
}

func TestDeleteMany_UsesBatchDeleterThroughUnwrapChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &batchProbe{}
	wrapped := &wrapper{inner: backend}

	err := DeleteMany(ctx, wrapped, []string{"a.txt", "b.txt"})
	require.NoError(t, err)
	assert.True(t, backend.called)
	assert.Equal(t, []string{"a.txt", "b.txt"}, backend.keys)
}

func TestDeleteMany_BatchErrorDoesNotReflectKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &batchFailProbe{}

	err := DeleteMany(ctx, backend, []string{"secret-token.txt"})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestCopyMany(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &inlineBackend{store: make(map[string][]byte)}
	backend.store["src1.txt"] = []byte("one")
	backend.store["src2.txt"] = []byte("two")

	pairs := []CopyPair{
		{SrcKey: "src1.txt", DstKey: "dst1.txt"},
		{SrcKey: "src2.txt", DstKey: "dst2.txt"},
	}

	err := CopyMany(ctx, backend, pairs)
	require.NoError(t, err)
	assert.Equal(t, []byte("one"), backend.store["dst1.txt"])
	assert.Equal(t, []byte("two"), backend.store["dst2.txt"])
}

func TestCopyMany_ErrorDoesNotReflectKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &inlineBackend{store: make(map[string][]byte)}

	err := CopyMany(ctx, backend, []CopyPair{
		{SrcKey: "secret-token.txt", DstKey: "other-secret.txt"},
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "other-secret")
}

func TestCopyMany_RejectsTooManyPairs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &inlineBackend{store: make(map[string][]byte)}
	pairs := make([]CopyPair, MaxBatchKeys+1)
	for i := range pairs {
		pairs[i] = CopyPair{
			SrcKey: fmt.Sprintf("src-%d.txt", i),
			DstKey: fmt.Sprintf("dst-%d.txt", i),
		}
	}

	err := CopyMany(ctx, backend, pairs)

	assert.ErrorIs(t, err, ErrValidation)
	assert.ErrorIs(t, err, ErrBatchTooLarge)
	assert.Empty(t, backend.store)
}

func TestCopyMany_NilBackendReturnsError(t *testing.T) {
	t.Parallel()

	err := CopyMany(context.Background(), nil, []CopyPair{{SrcKey: "src.txt", DstKey: "dst.txt"}})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backend is required")
}

// inlineBackend is a simple in-memory implementation for testing batch operations
// within the storage package (no circular dependency).
type inlineBackend struct {
	store map[string][]byte
}

func (b *inlineBackend) Put(_ context.Context, key string, r io.Reader, _ ObjectMeta) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	data, _ := io.ReadAll(r)
	b.store[key] = data
	return nil
}

func (b *inlineBackend) Get(_ context.Context, key string) (io.ReadCloser, ObjectMeta, error) {
	if err := ValidateKey(key); err != nil {
		return nil, ObjectMeta{}, err
	}
	data, ok := b.store[key]
	if !ok {
		return nil, ObjectMeta{}, ErrObjectNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return io.NopCloser(bytes.NewReader(cp)), ObjectMeta{Size: int64(len(data))}, nil
}

func (b *inlineBackend) Delete(_ context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	delete(b.store, key)
	return nil
}

func (b *inlineBackend) Exists(_ context.Context, key string) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	_, ok := b.store[key]
	return ok, nil
}

type deleteFailBackend struct {
	*inlineBackend
}

func (b *deleteFailBackend) Delete(context.Context, string) error {
	return errors.New("backend down")
}

type batchProbe struct {
	stubStorage
	called bool
	keys   []string
}

func (b *batchProbe) DeleteMany(_ context.Context, keys []string) map[string]error {
	b.called = true
	b.keys = append([]string(nil), keys...)
	return nil
}

type batchFailProbe struct {
	stubStorage
}

func (b *batchFailProbe) DeleteMany(context.Context, []string) map[string]error {
	return map[string]error{"secret-token.txt": errors.New("backend down")}
}
