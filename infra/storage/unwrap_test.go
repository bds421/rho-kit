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

// opaqueWrapper simulates a semantic decorator that must NOT be bypassed
// by capability discovery. It implements OpaqueDecorator but no optional
// interfaces.
type opaqueWrapper struct {
	stubStorage
	inner Storage
}

func (w *opaqueWrapper) Unwrap() Storage           { return w.inner }
func (w *opaqueWrapper) OpaqueStorageDecorator() {}

// opaqueListerWrapper is an OpaqueDecorator that DOES implement Lister.
// As* should find Lister on the wrapper itself but still treat it as
// opaque for other capabilities (Copier, Presigned, PublicURLer).
type opaqueListerWrapper struct {
	stubStorage
	inner Storage
}

func (w *opaqueListerWrapper) Unwrap() Storage           { return w.inner }
func (w *opaqueListerWrapper) OpaqueStorageDecorator() {}
func (w *opaqueListerWrapper) List(_ context.Context, _ string, _ ListOptions) iter.Seq2[ObjectInfo, error] {
	return func(yield func(ObjectInfo, error) bool) {}
}

func TestAsLister_BlockedByOpaqueDecorator(t *testing.T) {
	backend := &listerStorage{}
	wrapped := &opaqueWrapper{inner: backend}
	_, ok := AsLister(wrapped)
	assert.False(t, ok, "opaque decorator must block AsLister from reaching underlying Lister")
}

func TestAsCopier_BlockedByOpaqueDecorator(t *testing.T) {
	backend := &copierStorage{}
	wrapped := &opaqueWrapper{inner: backend}
	_, ok := AsCopier(wrapped)
	assert.False(t, ok, "opaque decorator must block AsCopier from reaching underlying Copier")
}

func TestAsPresigned_BlockedByOpaqueDecorator(t *testing.T) {
	backend := &presignedStorage{}
	wrapped := &opaqueWrapper{inner: backend}
	_, ok := AsPresigned(wrapped)
	assert.False(t, ok, "opaque decorator must block AsPresigned from reaching underlying PresignedStore")
}

func TestAsLister_OpaqueWrapperWithListerStillResolves(t *testing.T) {
	// When the opaque decorator itself implements Lister, As* must
	// return the decorator (not unwrap further).
	backend := &listerStorage{}
	wrapped := &opaqueListerWrapper{inner: backend}
	l, ok := AsLister(wrapped)
	assert.True(t, ok)
	// The lister returned must be the wrapper, not the inner backend.
	assert.NotEqual(t, Lister(backend), l, "AsLister must return the wrapper, not unwrap past it")
}

func TestAsCopier_OpaqueListerWrapperBlocksCopier(t *testing.T) {
	// Wrapper implements Lister but is opaque to Copier discovery.
	backend := &copierStorage{}
	wrapped := &opaqueListerWrapper{inner: backend}
	_, ok := AsCopier(wrapped)
	assert.False(t, ok, "opaque decorator that does not implement Copier must block discovery")
}
