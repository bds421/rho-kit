package encryption

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"runtime"

	"github.com/bds421/rho-kit/crypto/v2/encrypt"
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
	// EncryptionKey returns the 32-byte AES-256 key.
	// Called once per Put/Get operation.
	EncryptionKey(ctx context.Context) ([]byte, error)
}

// StaticKeyProvider returns the same key for every call.
// Suitable for testing or environments with static secrets.
type StaticKeyProvider struct {
	key []byte
}

// StaticKey creates a KeyProvider that always returns the given key.
// The key must be exactly 32 bytes (AES-256).
func StaticKey(key []byte) KeyProvider {
	if len(key) != 32 {
		panic("encryption: key must be 32 bytes")
	}
	keyCopy := make([]byte, 32)
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
	_ storage.Storage         = (*EncryptedStorage)(nil)
	_ storage.OpaqueDecorator = (*EncryptedStorage)(nil)
	_ storage.Copier          = (*EncryptedStorage)(nil)
)

// EncryptedStorage intentionally does NOT implement storage.PresignedStore:
// presigned URLs would bypass the encryption layer entirely (clients upload
// plaintext directly to the bucket, or download raw ciphertext). There is
// no safe in-band semantics for presigned access to encrypted-at-rest data.
//
// EncryptedStorage intentionally does NOT implement storage.PublicURLer:
// the public URL would serve raw ciphertext as if it were the original
// object.

// EncryptedStorage wraps a [storage.Storage] with AES-256-GCM encryption.
// Data is encrypted before Put and decrypted after Get. The encryption
// is transparent to the caller.
//
// Internally uses [encrypt.NewGCM], [encrypt.SealBytes], and [encrypt.OpenBytes]
// from kit/encrypt for the cryptographic operations.
type EncryptedStorage struct {
	backend storage.Storage
	keys    KeyProvider

	// putSem caps concurrent Put encryptions to bound peak memory under
	// load. Each in-flight Put may hold up to MaxEncryptableSize bytes of
	// plaintext + ciphertext (~512 MiB at the default cap), so an
	// unbounded fan-out from a public upload endpoint can exhaust memory.
	// The semaphore is sized to runtime.NumCPU() by default; override
	// with [WithMaxConcurrentEncryptions].
	putSem chan struct{}
}

// Option configures an EncryptedStorage.
type Option func(*EncryptedStorage)

// WithMaxConcurrentEncryptions caps the number of in-flight Put encryptions.
// Default: runtime.NumCPU(). The value must be positive; use
// [WithoutMaxConcurrentEncryptions] only when an external admission
// controller already bounds upload concurrency.
func WithMaxConcurrentEncryptions(n int) Option {
	if n <= 0 {
		panic("storage/encryption: WithMaxConcurrentEncryptions requires n > 0")
	}
	return func(e *EncryptedStorage) {
		e.putSem = make(chan struct{}, n)
	}
}

// WithoutMaxConcurrentEncryptions disables the in-process encryption
// concurrency cap.
func WithoutMaxConcurrentEncryptions() Option {
	return func(e *EncryptedStorage) {
		e.putSem = nil
	}
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

// New wraps backend with client-side AES-256-GCM encryption.
//
// The returned value always implements [storage.Copier] (via internal
// Get+Put — backend Copier is bypassed because AAD is key-bound). It
// implements [storage.Lister] only when the underlying backend supports
// listing. It deliberately does NOT implement [storage.PresignedStore] or
// [storage.PublicURLer]: those would bypass encryption entirely. Callers
// should detect support via [storage.AsLister] etc — these helpers honor
// the opaque-decorator marker on EncryptedStorage and will not unwrap past
// it for the unsafe capabilities.
//
// Panics if backend or keys is nil — both are required dependencies and
// nil values would only surface as a confusing nil-pointer panic on the
// first Put/Get. Failing fast at construction makes misconfiguration a
// startup error.
func New(backend storage.Storage, keys KeyProvider, opts ...Option) storage.Storage {
	if backend == nil {
		panic("encryption: backend must not be nil")
	}
	if keys == nil {
		panic("encryption: keys provider must not be nil")
	}
	e := &EncryptedStorage{
		backend: backend,
		keys:    keys,
		putSem:  make(chan struct{}, runtime.NumCPU()),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("encryption: option must not be nil")
		}
		opt(e)
	}

	// Forward Lister only when the underlying chain exposes it. AsLister
	// honors opaque-decorator markers in the underlying chain.
	if _, ok := storage.AsLister(backend); ok {
		return &encryptedLister{e}
	}
	return e
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

// aadForKey returns the AEAD associated data that binds ciphertext to its
// storage key. Without AAD, ciphertext is portable across keys: a
// compromised backend (or a confused-deputy copy) can move ciphertext from
// key A to key B and have it decrypt cleanly. Binding the storage key as
// AAD makes such a substitution fail authentication at Open time.
//
// Format: "rho-kit/storage:v1:" + storage key (UTF-8). The version prefix
// reserves room to extend the bound metadata (e.g. tenant) without
// breaking forward compatibility.
//
// BREAKING CHANGE in v2: ciphertext written by v1 cannot be decrypted by
// v2 because v2 supplies AAD that v1 did not bind. There is no on-disk
// format change.
func aadForKey(key string) []byte {
	const prefix = "rho-kit/storage:v1:"
	out := make([]byte, 0, len(prefix)+len(key))
	out = append(out, prefix...)
	out = append(out, key...)
	return out
}

// MaxEncryptableSize is the maximum content size that can be encrypted.
// AES-GCM requires buffering the entire plaintext, so we cap this to
// prevent memory exhaustion. For larger files, use server-side encryption
// (e.g. S3 SSE) or a streaming AEAD like AES-CTR + HMAC.
const MaxEncryptableSize = 256 << 20 // 256 MiB

// Put encrypts the content and stores the ciphertext.
// The stored format is: [12-byte nonce][ciphertext+tag].
// Returns an error if the content exceeds [MaxEncryptableSize].
//
// Holds up to ~2 × MaxEncryptableSize of memory while encrypting (plaintext
// + ciphertext). Concurrent Put calls are bounded by the semaphore set
// in [New] (default runtime.NumCPU()) — a public upload endpoint without
// this cap can exhaust memory under modest fan-out. ctx cancellation
// during the wait returns ctx.Err().
func (e *EncryptedStorage) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	// Validate key early to fail fast before expensive encryption work.
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: reader must not be nil", storage.ErrValidation)
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return err
	}

	if e.putSem != nil {
		select {
		case e.putSem <- struct{}{}:
			defer func() { <-e.putSem }()
		case <-ctx.Done():
			return fmt.Errorf("encryption: %w", ctx.Err())
		}
	}

	keyBytes, err := e.keys.EncryptionKey(ctx)
	if err != nil {
		return storage.WrapSafe("encryption: get key failed", err)
	}
	defer zeroBytes(keyBytes)

	plaintext, err := io.ReadAll(io.LimitReader(r, MaxEncryptableSize+1))
	// Defer zero immediately so the oversize/short-circuit paths below also
	// scrub plaintext from memory.
	defer zeroBytes(plaintext)
	if err != nil {
		return storage.WrapSafe("encryption: read plaintext failed", err)
	}
	if int64(len(plaintext)) > MaxEncryptableSize {
		return fmt.Errorf("encryption: content exceeds maximum encryptable size (%d bytes)", MaxEncryptableSize)
	}

	gcm, err := encrypt.NewGCM(keyBytes)
	if err != nil {
		return fmt.Errorf("encryption: %w", err)
	}

	ciphertext, err := encrypt.SealBytesAAD(gcm, plaintext, aadForKey(key))
	if err != nil {
		return fmt.Errorf("encryption: %w", err)
	}
	defer zeroBytes(ciphertext)

	meta.Size = int64(len(ciphertext))
	if err := e.backend.Put(ctx, key, bytes.NewReader(ciphertext), meta); err != nil {
		return storage.WrapSafe("encryption: put failed", err)
	}
	return nil
}

// Get retrieves and decrypts the stored content.
func (e *EncryptedStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	rc, meta, err := e.backend.Get(ctx, key)
	if err != nil {
		return nil, meta, storage.WrapSafe("encryption: get failed", err)
	}

	// Limit ciphertext read to MaxEncryptableSize + GCM overhead (nonce + tag)
	// to prevent OOM from corrupted or malicious backends returning unbounded data.
	// GCM overhead = 12 (nonce) + 16 (tag) = 28 bytes.
	const gcmOverhead = 12 + 16
	maxCiphertextSize := MaxEncryptableSize + gcmOverhead
	ciphertext, err := io.ReadAll(io.LimitReader(rc, int64(maxCiphertextSize)+1))
	// Close the backend reader immediately — we've consumed all bytes and
	// the returned cleaningReader wraps a bytes.Reader, not rc.
	_ = rc.Close()
	// Defer zero immediately so all error paths below scrub ciphertext too.
	defer zeroBytes(ciphertext)
	if err != nil {
		return nil, meta, storage.WrapSafe("encryption: read ciphertext failed", err)
	}
	if int64(len(ciphertext)) > int64(maxCiphertextSize) {
		return nil, meta, fmt.Errorf("encryption: ciphertext exceeds maximum size (%d bytes)", maxCiphertextSize)
	}

	keyBytes, err := e.keys.EncryptionKey(ctx)
	if err != nil {
		return nil, meta, storage.WrapSafe("encryption: get key failed", err)
	}
	defer zeroBytes(keyBytes)

	gcm, err := encrypt.NewGCM(keyBytes)
	if err != nil {
		return nil, meta, fmt.Errorf("encryption: %w", err)
	}

	plaintext, err := encrypt.OpenBytesAAD(gcm, ciphertext, aadForKey(key))
	if err != nil {
		return nil, meta, fmt.Errorf("encryption: %w", err)
	}

	meta.Size = int64(len(plaintext))
	return &cleaningReader{Reader: bytes.NewReader(plaintext), buf: plaintext}, meta, nil
}

// cleaningReader wraps a bytes.Reader and zeros the underlying plaintext
// buffer when Close is called, preventing decrypted data from lingering
// in memory after the caller is done reading.
type cleaningReader struct {
	*bytes.Reader
	buf []byte
}

func (c *cleaningReader) Close() error {
	zeroBytes(c.buf)
	return nil
}

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

// zeroBytes overwrites a byte slice with zeros to scrub key material from memory.
// Uses the clear builtin (Go 1.21+) which is not eliminated by compiler optimizations.
func zeroBytes(b []byte) {
	clear(b)
}

// list forwards to the underlying backend's [storage.Lister]. It is exposed
// as a method on the *encryptedLister combinator only when the underlying
// chain supports listing. Listing is safe through encryption because keys
// are not encrypted — only object content is. Tenant-scoped key prefixes
// still work as expected.
//
// The iterator rewrites Size to subtract GCM overhead (nonce + tag) so
// callers see plaintext size, matching what Get returns.
func (e *EncryptedStorage) list(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, fmt.Errorf("encryption: %w", err))
		}
	}
	if err := storage.ValidateListOptions(opts); err != nil {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, fmt.Errorf("encryption: %w", err))
		}
	}
	lister, ok := storage.AsLister(e.backend)
	if !ok {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, fmt.Errorf("encryption: underlying backend does not implement storage.Lister"))
		}
	}
	const gcmOverhead = 12 + 16
	return func(yield func(storage.ObjectInfo, error) bool) {
		for info, err := range lister.List(ctx, prefix, opts) {
			if err != nil {
				if !yield(storage.ObjectInfo{}, storage.WrapSafe("encryption: list failed", err)) {
					return
				}
				continue
			}
			if info.Size >= gcmOverhead {
				info.Size -= gcmOverhead
			}
			if !yield(info, nil) {
				return
			}
		}
	}
}

// Copy duplicates an object via Get+Put through the encryption layer. This
// decrypts under the source AAD and re-encrypts under the destination AAD,
// rebinding ciphertext to its new storage key.
//
// A native backend Copy would preserve the source AAD on the destination
// object, making it undecryptable at the destination key — see [aadForKey].
// We deliberately do NOT use the backend's Copier; correctness requires
// going through Get/Put.
func (e *EncryptedStorage) Copy(ctx context.Context, srcKey, dstKey string) error {
	if err := storage.ValidateKey(srcKey); err != nil {
		return fmt.Errorf("encryption: invalid source key: %w", err)
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return fmt.Errorf("encryption: invalid destination key: %w", err)
	}

	rc, meta, err := e.Get(ctx, srcKey)
	if err != nil {
		return fmt.Errorf("encryption: copy get: %w", err)
	}
	defer func() { _ = rc.Close() }()

	putMeta := storage.CloneObjectMeta(meta)
	if err := e.Put(ctx, dstKey, rc, putMeta); err != nil {
		return fmt.Errorf("encryption: copy put: %w", err)
	}
	return nil
}

// Compile-time interface compliance for the encryptedLister combinator.
var _ storage.Lister = (*encryptedLister)(nil)
