package encryption

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/bds421/rho-kit/crypto/encrypt"
	"github.com/bds421/rho-kit/infra/storage"
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
		panic(fmt.Sprintf("encryption: key must be 32 bytes, got %d", len(key)))
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

// Compile-time interface compliance.
var _ storage.Storage = (*EncryptedStorage)(nil)

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
// Default: runtime.NumCPU(). Pass <= 0 to disable the cap (unsafe; the
// audit-flagged DoS path uses ~512 MiB per concurrent Put at the default
// MaxEncryptableSize).
func WithMaxConcurrentEncryptions(n int) Option {
	return func(e *EncryptedStorage) {
		if n <= 0 {
			e.putSem = nil
			return
		}
		e.putSem = make(chan struct{}, n)
	}
}

// Unwrap returns the underlying storage backend.
func (e *EncryptedStorage) Unwrap() storage.Storage { return e.backend }

// New wraps backend with client-side AES-256-GCM encryption.
func New(backend storage.Storage, keys KeyProvider, opts ...Option) *EncryptedStorage {
	e := &EncryptedStorage{
		backend: backend,
		keys:    keys,
		putSem:  make(chan struct{}, runtime.NumCPU()),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
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
		return fmt.Errorf("encryption: get key: %w", err)
	}
	defer zeroBytes(keyBytes)

	plaintext, err := io.ReadAll(io.LimitReader(r, MaxEncryptableSize+1))
	if err != nil {
		return fmt.Errorf("encryption: read plaintext: %w", err)
	}
	if int64(len(plaintext)) > MaxEncryptableSize {
		return fmt.Errorf("encryption: content exceeds maximum encryptable size (%d bytes)", MaxEncryptableSize)
	}

	gcm, err := encrypt.NewGCM(keyBytes)
	if err != nil {
		return fmt.Errorf("encryption: %w", err)
	}

	ciphertext, err := encrypt.SealBytes(gcm, plaintext)
	// Zero plaintext now that encryption is complete.
	zeroBytes(plaintext)
	if err != nil {
		return fmt.Errorf("encryption: %w", err)
	}

	meta.Size = int64(len(ciphertext))
	putErr := e.backend.Put(ctx, key, bytes.NewReader(ciphertext), meta)
	zeroBytes(ciphertext)
	return putErr
}

// Get retrieves and decrypts the stored content.
func (e *EncryptedStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
	rc, meta, err := e.backend.Get(ctx, key)
	if err != nil {
		return nil, meta, err
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
	if err != nil {
		return nil, meta, fmt.Errorf("encryption: read ciphertext: %w", err)
	}
	if int64(len(ciphertext)) > int64(maxCiphertextSize) {
		return nil, meta, fmt.Errorf("encryption: ciphertext exceeds maximum size (%d bytes)", maxCiphertextSize)
	}

	keyBytes, err := e.keys.EncryptionKey(ctx)
	if err != nil {
		return nil, meta, fmt.Errorf("encryption: get key: %w", err)
	}
	defer zeroBytes(keyBytes)

	gcm, err := encrypt.NewGCM(keyBytes)
	if err != nil {
		return nil, meta, fmt.Errorf("encryption: %w", err)
	}

	plaintext, err := encrypt.OpenBytes(gcm, ciphertext)
	// Zero ciphertext now that decryption is complete.
	zeroBytes(ciphertext)
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
	return e.backend.Delete(ctx, key)
}

// Exists delegates to the underlying backend.
func (e *EncryptedStorage) Exists(ctx context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}
	return e.backend.Exists(ctx, key)
}

// zeroBytes overwrites a byte slice with zeros to scrub key material from memory.
// Uses the clear builtin (Go 1.21+) which is not eliminated by compiler optimizations.
func zeroBytes(b []byte) {
	clear(b)
}
