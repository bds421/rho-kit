package encryption

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"runtime"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
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
// The key must be exactly 32 bytes (AES-256); StaticKey panics on
// any other length so a misconfigured wiring never reaches the first
// encrypt operation.
func StaticKey(key []byte) KeyProvider {
	if len(key) != 32 {
		panic("encryption: StaticKey key must be 32 bytes")
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
// Internally uses [encrypt.NewGCM], [encrypt.EncryptBytes], and [encrypt.DecryptBytes]
// from kit/encrypt for the cryptographic operations.
// DefaultMaxOpenPlaintextReaders is the default cap on concurrent open
// plaintext readers retained after decrypt. Independent of the decrypt-work
// semaphore so a leaked ReadCloser cannot pin unbounded MaxEncryptableSize
// buffers while still allowing decrypt concurrency to release promptly.
const DefaultMaxOpenPlaintextReaders = 64

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

	// getSem caps concurrent Get *decryption work* (read ciphertext +
	// decrypt) so fan-out from download endpoints cannot run unbounded
	// concurrent AES-GCM operations. The slot is released once plaintext
	// is materialised into the returned reader — not held until Close —
	// so a leaked ReadCloser cannot permanently starve every subsequent
	// encrypted download (review-18). Callers must still Close to zero the
	// plaintext buffer and to release the open-plaintext budget. Sized to
	// runtime.NumCPU() by default; override with [WithMaxConcurrentDecryptions].
	getSem chan struct{}

	// openSem caps concurrent open plaintext readers after successful
	// decrypt. Acquired once plaintext is materialised and released on
	// Close (and on error paths before return). Default:
	// [DefaultMaxOpenPlaintextReaders]; override with
	// [WithMaxOpenPlaintextReaders].
	openSem chan struct{}

	// metrics is optional; set via [WithMetricsRegisterer].
	metrics *Metrics
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

// WithMaxConcurrentDecryptions caps the number of concurrent Get decryption
// operations (ciphertext read + AES-GCM decrypt). Default: runtime.NumCPU().
// The value must be positive; use [WithoutMaxConcurrentDecryptions] only when
// an external admission controller already bounds download concurrency.
//
// The slot is released after plaintext is materialised, not on reader Close,
// so a leaked ReadCloser does not permanently exhaust the cap. Close still
// zeros the plaintext buffer and remains required for secret hygiene.
func WithMaxConcurrentDecryptions(n int) Option {
	if n <= 0 {
		panic("storage/encryption: WithMaxConcurrentDecryptions requires n > 0")
	}
	return func(e *EncryptedStorage) {
		e.getSem = make(chan struct{}, n)
	}
}

// WithoutMaxConcurrentDecryptions disables the in-process decryption
// concurrency cap.
func WithoutMaxConcurrentDecryptions() Option {
	return func(e *EncryptedStorage) {
		e.getSem = nil
	}
}

// WithMaxOpenPlaintextReaders caps how many decrypted plaintext buffers may
// be retained concurrently by open Get readers. Default:
// [DefaultMaxOpenPlaintextReaders]. The value must be positive; use
// [WithoutMaxOpenPlaintextReaders] only when an external admission
// controller already bounds how many decrypted bodies a process may hold.
//
// The slot is acquired after successful decrypt and released in the
// reader's Close. Holding many unclosed readers therefore blocks further
// Gets (ctx cancellation during the wait returns ctx.Err()).
func WithMaxOpenPlaintextReaders(n int) Option {
	if n <= 0 {
		panic("storage/encryption: WithMaxOpenPlaintextReaders requires n > 0")
	}
	return func(e *EncryptedStorage) {
		e.openSem = make(chan struct{}, n)
	}
}

// WithoutMaxOpenPlaintextReaders disables the retained-plaintext budget.
// Decrypt concurrency is still gated by [WithMaxConcurrentDecryptions]
// unless that is also disabled.
func WithoutMaxOpenPlaintextReaders() Option {
	return func(e *EncryptedStorage) {
		e.openSem = nil
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

// Close delegates [storage.Close] to the wrapped backend so an
// encryption-wrapped Storage forwards lifecycle calls correctly.
func (e *EncryptedStorage) Close() error {
	if e == nil || e.backend == nil {
		return nil
	}
	return storage.Close(e.backend)
}

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
		panic("encryption: New backend must not be nil")
	}
	if keys == nil {
		panic("encryption: New keys provider must not be nil")
	}
	e := &EncryptedStorage{
		backend: backend,
		keys:    keys,
		putSem:  make(chan struct{}, runtime.NumCPU()),
		getSem:  make(chan struct{}, runtime.NumCPU()),
		openSem: make(chan struct{}, DefaultMaxOpenPlaintextReaders),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("encryption: New option must not be nil")
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
	if ctx == nil {
		// Normalise a nil context up front so the semaphore Acquire
		// and the underlying backend never see one. Wave 68 closed a
		// hostile-review finding that the semaphore path panicked
		// on ctx.Done() against a nil ctx.
		ctx = context.Background()
	}
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
			return redact.WrapError("encryption", ctx.Err())
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
		return redact.WrapError("encryption", err)
	}

	ciphertext, err := encrypt.EncryptBytesAAD(gcm, plaintext, aadForKey(key))
	if err != nil {
		return redact.WrapError("encryption", err)
	}
	defer zeroBytes(ciphertext)

	putMeta := storage.CloneObjectMeta(meta)
	putMeta.Size = int64(len(ciphertext))
	if err := e.backend.Put(ctx, key, bytes.NewReader(ciphertext), putMeta); err != nil {
		return storage.WrapSafe("encryption: put failed", err)
	}
	return nil
}

// Get retrieves and decrypts the stored content.
//
// Holds up to ~MaxEncryptableSize of ciphertext plus a full plaintext buffer
// while decrypting; the plaintext buffer is retained by the returned reader
// until Close. Two independent budgets apply:
//
//   - Decrypt work ([WithMaxConcurrentDecryptions], default runtime.NumCPU())
//     is acquired for the ciphertext read + AES-GCM window and released once
//     plaintext is materialised, so a leaked ReadCloser cannot starve decrypts.
//   - Retained plaintext ([WithMaxOpenPlaintextReaders], default
//     [DefaultMaxOpenPlaintextReaders]) is acquired after successful decrypt
//     and released on Close, so a leaky caller cannot pin unbounded plaintext
//     buffers.
//
// Callers MUST Close the ReadCloser (prefer defer) — a leaked reader holds
// one open-plaintext slot until process restart (or Close). ctx cancellation
// during either acquire wait returns ctx.Err().
func (e *EncryptedStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if ctx == nil {
		// Normalise a nil context up front so the semaphore acquire and
		// the underlying backend never see one, mirroring Put.
		ctx = context.Background()
	}
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	// Acquire the decryption slot before pulling ciphertext into memory so
	// concurrent decrypt work is bounded. The slot is released when decrypt
	// finishes (success or error), not when the caller Closes the reader —
	// a leaked ReadCloser must not permanently starve the download path.
	var releaseDecrypt func()
	if e.getSem != nil {
		select {
		case e.getSem <- struct{}{}:
			releaseDecrypt = func() { <-e.getSem }
		case <-ctx.Done():
			return nil, storage.ObjectMeta{}, redact.WrapError("encryption", ctx.Err())
		}
	}
	// releaseDecryptOnError frees the decrypt slot on any early return; on
	// success we release immediately after materialising plaintext (below).
	releaseDecryptOnError := releaseDecrypt
	defer func() {
		if releaseDecryptOnError != nil {
			releaseDecryptOnError()
		}
	}()

	// Resolve the decryption key BEFORE pulling ciphertext into memory.
	// Wave 71 closed a hostile-review finding that the prior order
	// (Get → read N MiB → DecryptionKey lookup) let a misconfigured
	// or unreachable key provider waste megabytes of read bandwidth
	// per request before surfacing the wiring failure.
	keyBytes, err := e.keys.EncryptionKey(ctx)
	if err != nil {
		return nil, storage.ObjectMeta{}, storage.WrapSafe("encryption: get key failed", err)
	}
	defer zeroBytes(keyBytes)

	rc, meta, err := e.backend.Get(ctx, key)
	if err != nil {
		return nil, storage.ObjectMeta{}, storage.WrapSafe("encryption: get failed", err)
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
		return nil, storage.ObjectMeta{}, storage.WrapSafe("encryption: read ciphertext failed", err)
	}
	if int64(len(ciphertext)) > int64(maxCiphertextSize) {
		return nil, storage.ObjectMeta{}, fmt.Errorf("encryption: ciphertext exceeds maximum size (%d bytes)", maxCiphertextSize)
	}

	gcm, err := encrypt.NewGCM(keyBytes)
	if err != nil {
		return nil, storage.ObjectMeta{}, redact.WrapError("encryption", err)
	}

	plaintext, err := encrypt.DecryptBytesAAD(gcm, ciphertext, aadForKey(key))
	if err != nil {
		return nil, storage.ObjectMeta{}, redact.WrapError("encryption", err)
	}

	meta.Size = int64(len(plaintext))
	// Decrypt complete: free the decrypt-work slot now so leaked readers
	// cannot starve concurrent AES-GCM work (review-18).
	if releaseDecryptOnError != nil {
		releaseDecryptOnError()
		releaseDecryptOnError = nil
	}

	// Acquire the retained-plaintext budget before handing the buffer out.
	// Released on Close; error paths free it immediately.
	var releaseOpen func()
	if e.openSem != nil {
		acquireStart := time.Now()
		select {
		case e.openSem <- struct{}{}:
			if e.metrics != nil {
				e.metrics.observeOpenReaderAcquire("ok", time.Since(acquireStart))
			}
			releaseOpen = func() { <-e.openSem }
		case <-ctx.Done():
			err := ctx.Err()
			if e.metrics != nil {
				result := "canceled"
				if errors.Is(err, context.DeadlineExceeded) {
					result = "timeout"
				}
				e.metrics.observeOpenReaderAcquire(result, time.Since(acquireStart))
			}
			zeroBytes(plaintext)
			return nil, storage.ObjectMeta{}, redact.WrapError("encryption", err)
		}
	}
	return &cleaningReader{
		Reader:  bytes.NewReader(plaintext),
		buf:     plaintext,
		release: releaseOpen,
	}, meta, nil
}

// cleaningReader wraps a bytes.Reader and zeros the underlying plaintext
// buffer when Close is called, preventing decrypted data from lingering
// in memory after the caller is done reading. Close also releases the
// retained-plaintext budget slot.
type cleaningReader struct {
	*bytes.Reader
	buf     []byte
	release func()
	closed  bool
}

func (c *cleaningReader) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	zeroBytes(c.buf)
	if c.release != nil {
		c.release()
		c.release = nil
	}
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
	// gcmOverhead is nonce(12)+tag(16). List sizes are adjusted under the
	// assumption that every object under this backend was written through
	// EncryptedStorage.Put — do not mix plaintext writes into the same
	// keyspace, or reported sizes will be silently 28 bytes short (review-18).
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
