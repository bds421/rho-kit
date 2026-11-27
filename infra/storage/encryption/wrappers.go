package encryption

import (
	"context"
	"fmt"
	"io"
	"iter"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Delete delegates to the underlying backend.
func (e *EncryptedStorage) Delete(ctx context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if err := e.backend.Delete(ctx, key); err != nil {
		return storage.WrapSafe("encryption: delete failed", err)
	}
	return nil
}

// Exists delegates to the underlying backend.
func (e *EncryptedStorage) Exists(ctx context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}
	ok, err := e.backend.Exists(ctx, key)
	if err != nil {
		return false, storage.WrapSafe("encryption: exists failed", err)
	}
	return ok, nil
}

type encryptedExactVersion struct {
	storage.Storage
	encrypted *EncryptedStorage
}

func (wrapper *encryptedExactVersion) Unwrap() storage.Storage { return wrapper.Storage }
func (wrapper *encryptedExactVersion) Close() error            { return storage.Close(wrapper.Storage) }

func (wrapper *encryptedExactVersion) exact() (storage.ExactVersionStore, error) {
	exact, ok := storage.AsExactVersionStore(wrapper.encrypted.backend)
	if !ok {
		return nil, storage.ErrExactVersionUnavailable
	}
	return exact, nil
}

func (wrapper *encryptedExactVersion) CurrentVersion(
	ctx context.Context,
	key string,
) (storage.ObjectVersion, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	exact, err := wrapper.exact()
	if err != nil {
		return storage.ObjectVersion{}, storage.ObjectMeta{}, err
	}
	object, meta, err := exact.CurrentVersion(ctx, key)
	return object, exactVersionPlaintextMeta(meta), err
}

func (wrapper *encryptedExactVersion) StatVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return storage.ObjectMeta{}, err
	}
	exact, err := wrapper.exact()
	if err != nil {
		return storage.ObjectMeta{}, err
	}
	meta, err := exact.StatVersion(ctx, object)
	return exactVersionPlaintextMeta(meta), err
}

func (wrapper *encryptedExactVersion) GetVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := object.Validate(); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	exact, err := wrapper.exact()
	if err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	return wrapper.encrypted.getWith(ctx, object.Key,
		func(ctx context.Context) (io.ReadCloser, storage.ObjectMeta, error) {
			return exact.GetVersion(ctx, object)
		})
}

func (wrapper *encryptedExactVersion) DeleteVersion(
	ctx context.Context,
	object storage.ObjectVersion,
) error {
	if err := object.Validate(); err != nil {
		return err
	}
	exact, err := wrapper.exact()
	if err != nil {
		return err
	}
	if err = exact.DeleteVersion(ctx, object); err != nil {
		return storage.WrapSafe("encryption: delete version failed", err)
	}
	return nil
}

func exactVersionPlaintextMeta(meta storage.ObjectMeta) storage.ObjectMeta {
	meta = storage.CloneObjectMeta(meta)
	meta.Size = storedPlaintextSize(meta.Size)
	return meta
}

// storedPlaintextSize is the plaintext size of a stored object of n bytes;
// zero (unknown) for a size no object of this format has.
func storedPlaintextSize(n int64) int64 {
	plain, ok := plaintextSize(n)
	if !ok {
		return 0
	}
	return plain
}

type encryptedExactVersionPageLister struct {
	storage.Storage
	encrypted *EncryptedStorage
}

func (wrapper *encryptedExactVersionPageLister) Unwrap() storage.Storage {
	return wrapper.Storage
}

func (wrapper *encryptedExactVersionPageLister) Close() error {
	return storage.Close(wrapper.Storage)
}

func (wrapper *encryptedExactVersionPageLister) VersionsPage(
	ctx context.Context,
	prefix string,
	options storage.ExactVersionPageOptions,
) (storage.ExactVersionPage, error) {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return storage.ExactVersionPage{}, err
	}
	if err := storage.ValidateExactVersionPageOptions(options); err != nil {
		return storage.ExactVersionPage{}, err
	}
	versions, ok := storage.AsExactVersionPageLister(
		wrapper.encrypted.backend,
	)
	if !ok {
		return storage.ExactVersionPage{}, storage.ErrExactVersionUnavailable
	}
	return versions.VersionsPage(ctx, prefix, options)
}

// list forwards to the underlying backend's [storage.Lister]. It is exposed
// as a method on the *encryptedLister combinator only when the underlying
// chain supports listing. Listing is safe through encryption because keys
// are not encrypted — only object content is. Tenant-scoped key prefixes
// still work as expected.
//
// The iterator rewrites Size to the plaintext size, matching what Get
// returns.
func (e *EncryptedStorage) list(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, redact.WrapError("encryption", err))
		}
	}
	if err := storage.ValidateListOptions(opts); err != nil {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, redact.WrapError("encryption", err))
		}
	}
	lister, ok := storage.AsLister(e.backend)
	if !ok {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, fmt.Errorf("encryption: underlying backend does not implement storage.Lister"))
		}
	}
	// Sizes are converted on the assumption that every object under this
	// backend was written through EncryptedStorage.Put; a size no object of
	// this format has is reported as zero (unknown).
	return func(yield func(storage.ObjectInfo, error) bool) {
		for info, err := range lister.List(ctx, prefix, opts) {
			if err != nil {
				if !yield(storage.ObjectInfo{}, storage.WrapSafe("encryption: list failed", err)) {
					return
				}
				continue
			}
			info.Size = storedPlaintextSize(info.Size)
			if !yield(info, nil) {
				return
			}
		}
	}
}

// Copy duplicates an object via Get+Put through the encryption layer,
// streaming: it decrypts under the source key's associated data and
// re-encrypts under the destination's, rebinding the object to its new
// storage key. A damaged source fails the Put rather than being copied.
//
// A native backend Copy would preserve the source AAD on the destination
// object, making it undecryptable at the destination key — see [aadForKey].
// We deliberately do NOT use the backend's Copier; correctness requires
// going through Get/Put.
func (e *EncryptedStorage) Copy(ctx context.Context, srcKey, dstKey string) error {
	if err := storage.ValidateKey(srcKey); err != nil {
		return redact.WrapError("encryption: invalid source key", err)
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return redact.WrapError("encryption: invalid destination key", err)
	}

	rc, meta, err := e.Get(ctx, srcKey)
	if err != nil {
		return redact.WrapError("encryption: copy get", err)
	}
	defer func() { _ = rc.Close() }()

	putMeta := storage.CloneObjectMeta(meta)
	if err := e.Put(ctx, dstKey, rc, putMeta); err != nil {
		return redact.WrapError("encryption: copy put", err)
	}
	return nil
}

// Compile-time interface compliance for the encryptedLister combinator.
var _ storage.Lister = (*encryptedLister)(nil)
