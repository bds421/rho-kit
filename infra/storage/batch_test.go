package storage

import (
	"bytes"
	"context"
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

func TestDeleteMany_InvalidKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := &inlineBackend{store: make(map[string][]byte)}

	err := DeleteMany(ctx, backend, []string{"valid.txt", ""})
	assert.Error(t, err)
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
