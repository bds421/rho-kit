package encryption

import (
	"context"
	"fmt"
	"io"
	"iter"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// KeyProvider supplies the encryption key. Implementations can fetch
// keys from AWS KMS, HashiCorp Vault, environment variables, etc.
//
// EncryptionKey is called once per Put/Get operation. If your provider
// calls an external service (e.g. KMS decrypt), consider caching the
// derived data-encryption key locally with a short TTL to avoid per-request
// latency and cost.
type KeyProvider interface {
	// EncryptionKey returns the 32-byte main key. Each object is sealed
	// under its own key derived from it.
	// Called once per Put/Get operation.
	EncryptionKey(ctx context.Context) ([]byte, error)
}

// StaticKeyProvider returns the same key for every call.
// Suitable for testing or environments with static secrets.
type StaticKeyProvider struct {
	key []byte
}

// StaticKey creates a KeyProvider that always returns the given key.
// The key must be exactly 32 bytes; StaticKey panics on any other length
// so a misconfigured wiring never reaches the first encrypt operation.
func StaticKey(key []byte) KeyProvider {
	if len(key) != keySize {
		panic("encryption: StaticKey key must be 32 bytes")
	}
	keyCopy := make([]byte, keySize)
	copy(keyCopy, key)
	return &StaticKeyProvider{key: keyCopy}
}

// EncryptionKey implements [KeyProvider]. Returns a copy to prevent callers
// from mutating or zeroing the internal key material.
func (s *StaticKeyProvider) EncryptionKey(context.Context) ([]byte, error) {
	out := make([]byte, len(s.key))
	copy(out, s.key)
	return out, nil
}

// Compile-time interface compliance. EncryptedStorage itself implements
// Storage and Copier (via Get+Put through the encryption layer). Lister is
// forwarded only when the underlying backend supports it; see [New] for
// the dispatch.
var (
	_ storage.Storage                = (*EncryptedStorage)(nil)
	_ storage.OpaqueDecorator        = (*EncryptedStorage)(nil)
	_ storage.Copier                 = (*EncryptedStorage)(nil)
	_ storage.ExactVersionStore      = (*encryptedExactVersion)(nil)
	_ storage.ExactVersionPageLister = (*encryptedExactVersionPageLister)(nil)
)

// EncryptedStorage intentionally does NOT implement storage.PresignedStore:
// presigned URLs would bypass the encryption layer entirely (clients upload
// plaintext directly to the bucket, or download raw ciphertext). There is
// no safe in-band semantics for presigned access to encrypted-at-rest data.
//
// EncryptedStorage intentionally does NOT implement storage.PublicURLer:
// the public URL would serve raw ciphertext as if it were the original
// object.

// EncryptedStorage wraps a [storage.Storage] with streaming client-side
// encryption (see the package documentation for the format). Put encrypts
// while the backend reads, Get decrypts while the caller reads; each holds
// about two segments of memory, whatever the object's size.
type EncryptedStorage struct {
	backend storage.Storage
	keys    KeyProvider
}

// Unwrap returns the underlying storage backend.
func (e *EncryptedStorage) Unwrap() storage.Storage { return e.backend }

// OpaqueStorageDecorator marks EncryptedStorage as an [storage.OpaqueDecorator].
// Capability discovery via storage.As* must NOT walk past this wrapper to
// the underlying backend's optional interfaces. The encryption layer makes
// presigned URLs (caller uploads plaintext) and public URLs (would serve
// ciphertext) unsafe; callers must opt in to forwarded capabilities that
// the wrapper itself implements with safe semantics (Copier, Lister).
func (e *EncryptedStorage) OpaqueStorageDecorator() {}

// Close delegates [storage.Close] to the wrapped backend so an
// encryption-wrapped Storage forwards lifecycle calls correctly.
func (e *EncryptedStorage) Close() error {
	if e == nil || e.backend == nil {
		return nil
	}
	return storage.Close(e.backend)
}

// New wraps backend with streaming client-side encryption.
//
// The returned value always implements [storage.Copier] (via internal
// Get+Put — backend Copier is bypassed because the object key is bound to
// the storage key). It implements [storage.Lister] only when the
// underlying backend supports listing. It deliberately does NOT implement
// [storage.PresignedStore] or [storage.PublicURLer]: those would bypass
// encryption entirely. Callers should detect support via [storage.AsLister]
// etc — these helpers honor the opaque-decorator marker on
// EncryptedStorage and will not unwrap past it for the unsafe capabilities.
//
// Panics if backend or keys is nil — both are required dependencies and
// nil values would only surface as a confusing nil-pointer panic on the
// first Put/Get. Failing fast at construction makes misconfiguration a
// startup error.
func New(backend storage.Storage, keys KeyProvider) storage.Storage {
	if backend == nil {
		panic("encryption: New backend must not be nil")
	}
	if keys == nil {
		panic("encryption: New keys provider must not be nil")
	}
	e := &EncryptedStorage{backend: backend, keys: keys}

	var composed storage.Storage = e
	// Forward Lister only when the underlying chain exposes it. AsLister
	// honors opaque-decorator markers in the underlying chain.
	if _, ok := storage.AsLister(backend); ok {
		composed = &encryptedLister{e}
	}
	if _, ok := storage.AsExactVersionPageLister(backend); ok {
		composed = &encryptedExactVersionPageLister{
			Storage:   composed,
			encrypted: e,
		}
	}
	if _, ok := storage.AsExactVersionStore(backend); ok {
		composed = &encryptedExactVersion{Storage: composed, encrypted: e}
	}
	return composed
}

// encryptedLister wraps EncryptedStorage to expose the conditional Lister
// capability — instantiated by [New] when the underlying backend supports
// [storage.Lister]. The List method is on this type rather than on
// EncryptedStorage directly so the unconditional method-set does not
// claim Lister when the underlying chain cannot satisfy it.
type encryptedLister struct{ *EncryptedStorage }

func (w *encryptedLister) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return w.list(ctx, prefix, opts)
}

// aadForKey returns the associated data that binds an object to its
// storage key: it is the HKDF info of the object's key, so ciphertext
// moved from key A to key B (by a compromised backend or a confused-deputy
// copy) derives another key and fails authentication.
//
// The label names the streaming format; objects of the earlier
// single-message format ("rho-kit/storage:v1:") do not decrypt.
func aadForKey(key string) []byte {
	const prefix = "rho-kit/storage:stream-v1:"
	out := make([]byte, 0, len(prefix)+len(key))
	out = append(out, prefix...)
	out = append(out, key...)
	return out
}

// Put encrypts r while the backend reads it and stores the ciphertext.
// Memory stays at about two segments whatever the size of r; nothing reads
// r after Put returns.
//
// When meta.Size is set, the backend is given the exact ciphertext size,
// and content longer or shorter than meta.Size fails the Put instead of
// storing it. Zero leaves the size unknown to the backend.
func (e *EncryptedStorage) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Validate key early to fail fast before any encryption work.
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: reader must not be nil", storage.ErrValidation)
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return err
	}

	aead, err := e.streamingAEAD(ctx)
	if err != nil {
		return err
	}
	sealed, err := newEncryptingReader(aead, r, aadForKey(key), meta.Size)
	if err != nil {
		return redact.WrapError("encryption", err)
	}

	putMeta := storage.CloneObjectMeta(meta)
	if meta.Size > 0 {
		putMeta.Size = ciphertextSize(meta.Size)
	}
	if err := e.backend.Put(ctx, key, sealed, putMeta); err != nil {
		return storage.WrapSafe("encryption: put failed", err)
	}
	// A backend that returned without reading to the end did not store the
	// whole object; its last segment was never sealed.
	if !sealed.complete() {
		return fmt.Errorf("encryption: put failed: backend stopped before the end of the content")
	}
	return nil
}

// streamingAEAD derives the streaming cipher from the provider's key and
// scrubs the provider's copy.
func (e *EncryptedStorage) streamingAEAD(ctx context.Context) (aeadCipher, error) {
	keyBytes, err := e.keys.EncryptionKey(ctx)
	if err != nil {
		return nil, storage.WrapSafe("encryption: get key failed", err)
	}
	defer clear(keyBytes)
	if len(keyBytes) != keySize {
		return nil, fmt.Errorf("encryption: key must be %d bytes", keySize)
	}
	aead, err := newStreamingAEAD(keyBytes)
	if err != nil {
		return nil, redact.WrapError("encryption", err)
	}
	return aead, nil
}

// Get returns a reader that decrypts the stored object while the caller
// reads it; memory stays at about two segments whatever its size. Read
// returns [ErrInvalidCiphertext] when the object was truncated, reordered
// or changed. Segments before the damage are returned before that error,
// each of them authentic and in place, so a caller must treat the
// transfer as failed on any error rather than io.EOF, as with any stream.
//
// meta.Size is the plaintext size. Callers MUST Close the reader; it
// holds the backend's.
func (e *EncryptedStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	return e.getWith(ctx, key, func(ctx context.Context) (io.ReadCloser, storage.ObjectMeta, error) {
		return e.backend.Get(ctx, key)
	})
}

func (e *EncryptedStorage) getWith(
	ctx context.Context,
	key string,
	open func(context.Context) (io.ReadCloser, storage.ObjectMeta, error),
) (io.ReadCloser, storage.ObjectMeta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	// Resolve the key before opening the object, so a misconfigured or
	// unreachable key provider fails before the backend is read.
	aead, err := e.streamingAEAD(ctx)
	if err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	rc, meta, err := open(ctx)
	if err != nil {
		return nil, storage.ObjectMeta{}, storage.WrapSafe("encryption: get failed", err)
	}
	if meta.Size > 0 {
		plain, ok := plaintextSize(meta.Size)
		if !ok {
			_ = rc.Close()
			return nil, storage.ObjectMeta{}, ErrInvalidCiphertext
		}
		meta.Size = plain
	}
	plain, err := newDecryptingReader(aead, rc, aadForKey(key))
	if err != nil {
		_ = rc.Close()
		return nil, storage.ObjectMeta{}, err
	}
	return plain, meta, nil
}

// redactRead wraps a failed read of the backend's object.
func redactRead(err error) error {
	return storage.WrapSafe("encryption: read ciphertext failed", err)
}
