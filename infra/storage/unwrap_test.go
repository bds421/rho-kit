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

type batchDeleterStorage struct{ stubStorage }

func (b *batchDeleterStorage) DeleteMany(context.Context, []string) map[string]error { return nil }

type multipartStorage struct{ stubStorage }

func (m *multipartStorage) InitUpload(context.Context, string, ObjectMeta) (MultipartUpload, error) {
	return MultipartUpload{}, nil
}
func (m *multipartStorage) UploadPart(context.Context, MultipartUpload, int, io.Reader) (PartInfo, error) {
	return PartInfo{}, nil
}
func (m *multipartStorage) CompleteUpload(context.Context, MultipartUpload, []PartInfo) error {
	return nil
}
func (m *multipartStorage) AbortUpload(context.Context, MultipartUpload) error { return nil }

type taggerStorage struct{ stubStorage }

func (t *taggerStorage) GetTags(context.Context, string) (Tags, error) { return nil, nil }
func (t *taggerStorage) SetTags(context.Context, string, Tags) error   { return nil }
func (t *taggerStorage) DeleteTags(context.Context, string) error      { return nil }

type versionerStorage struct{ stubStorage }

func (v *versionerStorage) ListVersions(context.Context, string) iter.Seq2[ObjectVersion, error] {
	return func(yield func(ObjectVersion, error) bool) {}
}
func (v *versionerStorage) GetVersion(context.Context, string, string) (io.ReadCloser, ObjectMeta, error) {
	return nil, ObjectMeta{}, nil
}
func (v *versionerStorage) DeleteVersion(context.Context, string, string) error { return nil }

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

func TestAsAdditionalOptionalInterfaces_Wrapped(t *testing.T) {
	t.Run("batch deleter", func(t *testing.T) {
		backend := &batchDeleterStorage{}
		wrapped := &wrapper{inner: backend}
		got, ok := AsBatchDeleter(wrapped)
		assert.True(t, ok)
		assert.NotNil(t, got)
	})
	t.Run("multipart uploader", func(t *testing.T) {
		backend := &multipartStorage{}
		wrapped := &wrapper{inner: backend}
		got, ok := AsMultipartUploader(wrapped)
		assert.True(t, ok)
		assert.NotNil(t, got)
	})
	t.Run("tagger", func(t *testing.T) {
		backend := &taggerStorage{}
		wrapped := &wrapper{inner: backend}
		got, ok := AsTagger(wrapped)
		assert.True(t, ok)
		assert.NotNil(t, got)
	})
	t.Run("versioner", func(t *testing.T) {
		backend := &versionerStorage{}
		wrapped := &wrapper{inner: backend}
		got, ok := AsVersioner(wrapped)
		assert.True(t, ok)
		assert.NotNil(t, got)
	})
}

// opaqueWrapper simulates a semantic decorator that must NOT be bypassed
// by capability discovery. It implements OpaqueDecorator but no optional
// interfaces.
type opaqueWrapper struct {
	stubStorage
	inner Storage
}

func (w *opaqueWrapper) Unwrap() Storage         { return w.inner }
func (w *opaqueWrapper) OpaqueStorageDecorator() {}

// opaqueListerWrapper is an OpaqueDecorator that DOES implement Lister.
// As* should find Lister on the wrapper itself but still treat it as
// opaque for other capabilities (Copier, Presigned, PublicURLer).
type opaqueListerWrapper struct {
	stubStorage
	inner Storage
}

func (w *opaqueListerWrapper) Unwrap() Storage         { return w.inner }
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

func TestAsAdditionalOptionalInterfaces_BlockedByOpaqueDecorator(t *testing.T) {
	tests := []struct {
		name string
		st   Storage
		as   func(Storage) bool
	}{
		{"batch deleter", &batchDeleterStorage{}, func(s Storage) bool {
			_, ok := AsBatchDeleter(s)
			return ok
		}},
		{"multipart uploader", &multipartStorage{}, func(s Storage) bool {
			_, ok := AsMultipartUploader(s)
			return ok
		}},
		{"tagger", &taggerStorage{}, func(s Storage) bool {
			_, ok := AsTagger(s)
			return ok
		}},
		{"versioner", &versionerStorage{}, func(s Storage) bool {
			_, ok := AsVersioner(s)
			return ok
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, tt.as(&opaqueWrapper{inner: tt.st}))
		})
	}
}
