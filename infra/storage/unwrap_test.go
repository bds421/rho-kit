package storage

import (
	"context"
	"io"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// stubStorage implements Storage but not Lister/Copier/PresignedStore.
type stubStorage struct{}

func (s *stubStorage) Put(context.Context, string, io.Reader, ObjectMeta) error { return nil }
func (s *stubStorage) Get(context.Context, string) (io.ReadCloser, ObjectMeta, error) {
	return nil, ObjectMeta{}, nil
}
func (s *stubStorage) Delete(context.Context, string) error         { return nil }
func (s *stubStorage) Exists(context.Context, string) (bool, error) { return false, nil }

// listerStorage implements Storage + Lister.
type listerStorage struct{ stubStorage }

func (l *listerStorage) List(_ context.Context, _ string, _ ListOptions) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {}
}

// copierStorage implements Storage + Copier.
type copierStorage struct{ stubStorage }

func (c *copierStorage) Copy(_ context.Context, _, _ string) error { return nil }

// presignedStorage implements Storage + PresignedStore.
type presignedStorage struct{ stubStorage }

func (p *presignedStorage) PresignGetURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}
func (p *presignedStorage) PresignPutURL(_ context.Context, _ string, _ time.Duration, _ ObjectMeta) (string, error) {
	return "", nil
}

// wrapper simulates a storage decorator.
type wrapper struct {
	stubStorage
	inner Storage
}

func (w *wrapper) Unwrap() Storage { return w.inner }

func TestAsLister_Direct(t *testing.T) {
	backend := &listerStorage{}
	l, ok := AsLister(backend)
	assert.True(t, ok)
	assert.NotNil(t, l)
}

func TestAsLister_Wrapped(t *testing.T) {
	backend := &listerStorage{}
	wrapped := &wrapper{inner: backend}
	l, ok := AsLister(wrapped)
	assert.True(t, ok)
	assert.NotNil(t, l)
}

func TestAsLister_DoubleWrapped(t *testing.T) {
	backend := &listerStorage{}
	inner := &wrapper{inner: backend}
	outer := &wrapper{inner: inner}
	l, ok := AsLister(outer)
	assert.True(t, ok)
	assert.NotNil(t, l)
}

func TestAsLister_NotFound(t *testing.T) {
	backend := &stubStorage{}
	wrapped := &wrapper{inner: backend}
	_, ok := AsLister(wrapped)
	assert.False(t, ok)
}

func TestAsCopier_Wrapped(t *testing.T) {
	backend := &copierStorage{}
	wrapped := &wrapper{inner: backend}
	c, ok := AsCopier(wrapped)
	assert.True(t, ok)
	assert.NotNil(t, c)
}

func TestAsCopier_NotFound(t *testing.T) {
	backend := &stubStorage{}
	_, ok := AsCopier(backend)
	assert.False(t, ok)
}

func TestAsPresigned_Wrapped(t *testing.T) {
	backend := &presignedStorage{}
	wrapped := &wrapper{inner: backend}
	p, ok := AsPresigned(wrapped)
	assert.True(t, ok)
	assert.NotNil(t, p)
}

func TestAsPresigned_NotFound(t *testing.T) {
	backend := &stubStorage{}
	_, ok := AsPresigned(backend)
	assert.False(t, ok)
}
