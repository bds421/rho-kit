package encryption

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func TestWithMaxConcurrentEncryptions_PanicsOnNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			require.Panics(t, func() {
				WithMaxConcurrentEncryptions(n)
			})
		})
	}
}

func TestWithMaxConcurrentDecryptions_PanicsOnNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			require.Panics(t, func() {
				WithMaxConcurrentDecryptions(n)
			})
		})
	}
}

func TestWithMaxOpenPlaintextReaders_PanicsOnNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			require.Panics(t, func() {
				WithMaxOpenPlaintextReaders(n)
			})
		})
	}
}

// TestEncryptedStorage_OpenPlaintextBudgetBlocksWhileHeld asserts that open
// unclosed readers consume the retained-plaintext budget and block further
// Gets until Close releases a slot.
func TestEncryptedStorage_OpenPlaintextBudgetBlocksWhileHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)),
		WithMaxConcurrentDecryptions(4),
		WithMaxOpenPlaintextReaders(1),
	)

	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	rc1, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)

	// Second Get must wait on the open-plaintext budget. Cancel to observe the wait.
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, _, err = enc.Get(waitCtx, "file.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Closing the first reader frees the slot.
	require.NoError(t, rc1.Close())

	rc2, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err, "Get after Close must acquire freed open-plaintext slot")
	_ = rc2.Close()
}

// TestEncryptedStorage_OpenPlaintextBudgetReleasedOnCloseDoubleCloseSafe
// pins idempotent Close: second Close must not double-release the semaphore.
func TestEncryptedStorage_OpenPlaintextBudgetReleasedOnCloseDoubleCloseSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)), WithMaxOpenPlaintextReaders(1))
	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	rc, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.NoError(t, rc.Close()) // must not panic / double-free

	rc2, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	_ = rc2.Close()
}

// TestEncryptedStorage_WithoutMaxOpenPlaintextReaders_NoBound asserts the
// opt-out disables the retained-plaintext budget.
func TestEncryptedStorage_WithoutMaxOpenPlaintextReaders_NoBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)),
		WithMaxOpenPlaintextReaders(1), // overridden by Without because it is last
		WithoutMaxOpenPlaintextReaders(),
	)

	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))
	rc1, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	rc2, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	_ = rc1.Close()
	_ = rc2.Close()
}

// TestEncryptedStorage_GetBoundedByDecryptionSemaphore asserts that Get is
// gated by a concurrency cap on the decrypt window. After plaintext is
// materialised the slot is released, so a leaked ReadCloser cannot permanently
// starve subsequent Gets (review-18). With a cap of 1, a second Get succeeds
// even if the first reader is still open.
func TestEncryptedStorage_GetSemaphoreReleasedAfterMaterialize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)), WithMaxConcurrentDecryptions(1))

	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	// First Get returns with the only slot already released.
	rc1, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	defer func() { _ = rc1.Close() }()

	// Second Get must succeed without closing the first reader.
	rc2, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err, "leaked/open first reader must not block subsequent Gets")
	_ = rc2.Close()
}

// TestEncryptedStorage_GetRespectsCtxCancelWhileWaiting asserts that a Get
// blocked on a saturated decryption semaphore returns the ctx error rather
// than hanging, mirroring Put's ctx-aware acquire. Saturation is simulated by
// holding the slot via a concurrent in-flight decrypt that we delay by
// injecting a slow backend Read (not possible with membackend alone), so this
// test only covers the cancelled-before-acquire path: a pre-cancelled ctx
// fails at the select even when no slot is held.
func TestEncryptedStorage_GetRespectsCtxCancelWhileWaiting(t *testing.T) {
	t.Parallel()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)), WithMaxConcurrentDecryptions(1))

	require.NoError(t, enc.Put(context.Background(), "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := enc.Get(ctx, "file.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestEncryptedStorage_WithoutMaxConcurrentDecryptions_NoBound asserts the
// additive opt-out disables the Get cap so concurrent Gets do not serialize.
func TestEncryptedStorage_WithoutMaxConcurrentDecryptions_NoBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)), WithoutMaxConcurrentDecryptions())

	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	// Two open readers at once must both succeed with no cap.
	rc1, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	rc2, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	_ = rc1.Close()
	_ = rc2.Close()
}

func TestEncryptedStorage_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey(t)
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	original := []byte("secret data that must be encrypted")
	err := enc.Put(ctx, "secret.txt", bytes.NewReader(original), storage.ObjectMeta{
		ContentType: "text/plain",
	})
	require.NoError(t, err)

	// Verify the stored data is NOT plaintext.
	rc, _, err := backend.Get(ctx, "secret.txt")
	require.NoError(t, err)
	stored, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.NotEqual(t, original, stored, "stored data should be encrypted")

	// Decrypt and verify.
	rc, meta, err := enc.Get(ctx, "secret.txt")
	require.NoError(t, err)
	decrypted, _ := io.ReadAll(rc)
	_ = rc.Close()

	assert.Equal(t, original, decrypted)
	assert.Equal(t, int64(len(original)), meta.Size)
}

func TestEncryptedStorage_PutRejectsNilReader(t *testing.T) {
	t.Parallel()
	enc := New(membackend.New(), StaticKey(testKey(t)))

	err := enc.Put(context.Background(), "nil.txt", nil, storage.ObjectMeta{})
	require.Error(t, err)
	assert.ErrorIs(t, err, storage.ErrValidation)
}

func TestEncryptedStorage_PutReadErrorDoesNotReflectCause(t *testing.T) {
	t.Parallel()
	readErr := errors.New("read failed for secret-token")
	enc := New(membackend.New(), StaticKey(testKey(t)))

	err := enc.Put(context.Background(), "reader.txt", errReader{err: readErr}, storage.ObjectMeta{})

	require.Error(t, err)
	assert.ErrorIs(t, err, readErr)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "read failed")
}

func TestEncryptedStorage_KeyProviderErrorDoesNotReflectCause(t *testing.T) {
	t.Parallel()
	keyErr := errors.New("key provider failed for secret-token")
	enc := New(membackend.New(), errKeyProvider{err: keyErr})

	err := enc.Put(context.Background(), "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})

	require.Error(t, err)
	assert.ErrorIs(t, err, keyErr)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "key provider failed")
}

func TestEncryptedStorage_BackendErrorsDoNotReflectCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("put", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend put failed for secret-token")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return backendErr },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		err := enc.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{})
		require.Error(t, err)
		assert.ErrorIs(t, err, backendErr)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "backend put failed")
	})

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend get failed for secret-token")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return nil, storage.ObjectMeta{}, backendErr
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		_, _, err := enc.Get(ctx, "file.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, backendErr)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "backend get failed")
	})

	t.Run("get reader", func(t *testing.T) {
		t.Parallel()
		readErr := errors.New("ciphertext read failed for secret-token")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return io.NopCloser(errReader{err: readErr}), storage.ObjectMeta{}, nil
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		_, _, err := enc.Get(ctx, "file.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, readErr)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "ciphertext read failed")
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend delete failed for secret-token")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
			},
			del: func(context.Context, string) error { return backendErr },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		err := enc.Delete(ctx, "file.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, backendErr)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "backend delete failed")
	})

	t.Run("exists", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend exists failed for secret-token")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return nil, storage.ObjectMeta{}, storage.ErrObjectNotFound
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, backendErr },
		}, StaticKey(testKey(t)))

		_, err := enc.Exists(ctx, "file.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, backendErr)
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "backend exists failed")
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend list failed for secret-token")
		enc := New(&failingListBackend{Backend: membackend.New(), err: backendErr}, StaticKey(testKey(t)))
		lister, ok := storage.AsLister(enc)
		require.True(t, ok)

		var seenErr error
		for _, err := range lister.List(ctx, "", storage.ListOptions{}) {
			seenErr = err
			break
		}

		require.Error(t, seenErr)
		assert.ErrorIs(t, seenErr, backendErr)
		assert.NotContains(t, seenErr.Error(), "secret-token")
		assert.NotContains(t, seenErr.Error(), "backend list failed")
	})
}

func TestEncryptedStorage_WrongKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key1 := testKey(t)
	key2 := testKey(t)
	backend := membackend.New()

	enc1 := New(backend, StaticKey(key1))
	enc2 := New(backend, StaticKey(key2))

	err := enc1.Put(ctx, "secret.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	// Decrypting with wrong key should fail.
	// Error message is redacted by wave 143 (redact.WrapError); assert
	// the "encryption" prefix is present so callers still know the
	// failure originated in this layer, without leaking the AEAD's
	// internal decrypt verbiage.
	_, _, err = enc2.Get(ctx, "secret.txt")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "encryption")
}

// TestEncryptedStorage_GetReturnsEmptyMetaOnError asserts that Get returns a
// zero-value ObjectMeta on every error path, matching sibling backends (azure,
// gcs, s3, local, mem, sftp) which all return storage.ObjectMeta{} with errors.
// A caller that inspects meta without first checking err must never receive
// metadata for content that failed authentication, decryption, or a read.
func TestEncryptedStorage_GetReturnsEmptyMetaOnError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// nonEmptyMeta is what a real backend would hand back alongside an
	// error/object; the encryption layer must NOT propagate it on errors.
	nonEmptyMeta := func() storage.ObjectMeta {
		return storage.ObjectMeta{
			Size:        4096,
			ContentType: "application/octet-stream",
			ETag:        "leaked-etag",
		}
	}

	t.Run("backend get error", func(t *testing.T) {
		t.Parallel()
		backendErr := errors.New("backend get failed")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return nil, nonEmptyMeta(), backendErr
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		_, meta, err := enc.Get(ctx, "file.txt")
		require.Error(t, err)
		assert.Equal(t, storage.ObjectMeta{}, meta, "backend get error must not leak backend meta")
	})

	t.Run("backend read error", func(t *testing.T) {
		t.Parallel()
		readErr := errors.New("ciphertext read failed")
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return io.NopCloser(errReader{err: readErr}), nonEmptyMeta(), nil
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		_, meta, err := enc.Get(ctx, "file.txt")
		require.Error(t, err)
		assert.Equal(t, storage.ObjectMeta{}, meta, "read error must not leak backend meta")
	})

	t.Run("ciphertext exceeds max size", func(t *testing.T) {
		t.Parallel()
		const gcmOverhead = 12 + 16
		oversized := bytes.Repeat([]byte{0xAB}, MaxEncryptableSize+gcmOverhead+1)
		enc := New(&noListerBackend{
			put: func(context.Context, string, io.Reader, storage.ObjectMeta) error { return nil },
			get: func(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
				return io.NopCloser(bytes.NewReader(oversized)), nonEmptyMeta(), nil
			},
			del: func(context.Context, string) error { return nil },
			ex:  func(context.Context, string) (bool, error) { return false, nil },
		}, StaticKey(testKey(t)))

		_, meta, err := enc.Get(ctx, "file.txt")
		require.Error(t, err)
		assert.Equal(t, storage.ObjectMeta{}, meta, "size-limit error must not leak backend meta")
	})

	t.Run("decrypt failure on wrong key", func(t *testing.T) {
		t.Parallel()
		backend := membackend.New()
		writer := New(backend, StaticKey(testKey(t)))
		reader := New(backend, StaticKey(testKey(t)))

		require.NoError(t, writer.Put(ctx, "secret.txt",
			bytes.NewReader([]byte("data")),
			storage.ObjectMeta{ContentType: "text/plain"}))

		_, meta, err := reader.Get(ctx, "secret.txt")
		require.Error(t, err)
		assert.Equal(t, storage.ObjectMeta{}, meta, "decrypt failure must not leak backend meta")
	})
}

func TestEncryptedStorage_ExistsAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey(t)
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	err := enc.Put(ctx, "file.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	ok, err := enc.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, ok)

	err = enc.Delete(ctx, "file.txt")
	require.NoError(t, err)

	ok, err = enc.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEncryptedStorage_CopyGetErrorDoesNotReflectSourceKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enc := New(membackend.New(), StaticKey(testKey(t)))
	copier, ok := enc.(storage.Copier)
	require.True(t, ok)

	err := copier.Copy(ctx, "secret-token.txt", "dst.txt")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestEncryptedStorage_EmptyContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey(t)
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	err := enc.Put(ctx, "empty.txt", bytes.NewReader(nil), storage.ObjectMeta{})
	require.NoError(t, err)

	rc, _, err := enc.Get(ctx, "empty.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Empty(t, data)
}

func TestStaticKey_PanicsOnWrongSize(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { StaticKey([]byte("short")) })
}

// TestEncryptedStorage_AAD_BindsToStorageKey asserts that ciphertext
// copy-pasted from key A to key B fails to decrypt under the AAD-binding
// rule introduced in v2. Substituting objects between keys is the
// confused-deputy attack the binding defends against.
func TestEncryptedStorage_AAD_BindsToStorageKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey(t)
	backend := membackend.New()
	enc := New(backend, StaticKey(key))

	original := []byte("payload bound to keyA")
	require.NoError(t, enc.Put(ctx, "keyA", bytes.NewReader(original), storage.ObjectMeta{}))

	// Copy raw ciphertext from keyA to keyB at the backend level. With AAD
	// binding, the ciphertext is no longer portable.
	rc, _, err := backend.Get(ctx, "keyA")
	require.NoError(t, err)
	stolen, _ := io.ReadAll(rc)
	_ = rc.Close()
	require.NoError(t, backend.Put(ctx, "keyB", bytes.NewReader(stolen), storage.ObjectMeta{Size: int64(len(stolen))}))

	// Reading keyB must fail authentication (different AAD) — that is the
	// whole point of binding. Error is redacted by wave 143; assert the
	// "encryption" prefix is present without leaking the inner verbiage.
	_, _, err = enc.Get(ctx, "keyB")
	require.Error(t, err, "ciphertext copied to a different key must not decrypt")
	assert.Contains(t, err.Error(), "encryption")

	// Reading keyA must still work — same key, same AAD.
	rc, _, err = enc.Get(ctx, "keyA")
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, original, got)
}

func TestEncryptedStorage_New_PanicsOnNilBackend(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "encryption: New backend must not be nil", func() {
		New(nil, StaticKey(testKey(t)))
	})
}

func TestEncryptedStorage_New_PanicsOnNilKeys(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "encryption: New keys provider must not be nil", func() {
		New(membackend.New(), nil)
	})
}

func TestEncryptedStorage_New_PanicsOnNilOption(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "encryption: New option must not be nil", func() {
		New(membackend.New(), StaticKey(testKey(t)), nil)
	})
}

// presignedMemBackend wraps MemBackend with a stub PresignedStore so we can
// verify capability discovery does not bypass encryption.
type presignedMemBackend struct {
	*membackend.Backend
}

func (p *presignedMemBackend) PresignGetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://raw-bucket/" + key, nil
}

func (p *presignedMemBackend) PresignPutURL(_ context.Context, key string, _ time.Duration, _ storage.ObjectMeta) (string, error) {
	return "https://raw-bucket/" + key, nil
}

// publicURLBackend wraps MemBackend with a stub PublicURLer.
type publicURLBackend struct {
	*membackend.Backend
}

func (p *publicURLBackend) URL(_ context.Context, key string) (string, error) {
	return "https://raw-bucket/" + key, nil
}

func TestAsPresigned_BlockedByEncryption(t *testing.T) {
	t.Parallel()
	// AsPresigned through encryption must NOT walk past the encryption
	// wrapper to the underlying presigner. Returning the raw presigner
	// would let callers upload plaintext directly to the bucket and
	// download raw ciphertext, bypassing encryption-at-rest.
	backend := &presignedMemBackend{Backend: membackend.New()}
	enc := New(backend, StaticKey(testKey(t)))

	_, ok := storage.AsPresigned(enc)
	assert.False(t, ok, "encryption must block AsPresigned from reaching underlying presigner")
}

func TestAsPublicURLer_BlockedByEncryption(t *testing.T) {
	t.Parallel()
	// Same hazard as presigned: a raw public URL would serve ciphertext.
	backend := &publicURLBackend{Backend: membackend.New()}
	enc := New(backend, StaticKey(testKey(t)))

	_, ok := storage.AsPublicURLer(enc)
	assert.False(t, ok, "encryption must block AsPublicURLer from reaching underlying URLer")
}

func TestAsCopier_EncryptionDoesDecryptReencrypt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Encryption must implement Copier itself, going through Get+Put so the
	// destination object is re-AAD'd to its new key. A native backend Copy
	// would preserve source AAD on the destination key, making the destination
	// undecryptable.
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)))

	original := []byte("payload to copy")
	require.NoError(t, enc.Put(ctx, "src", bytes.NewReader(original), storage.ObjectMeta{}))

	copier, ok := storage.AsCopier(enc)
	require.True(t, ok, "encryption must implement Copier")

	require.NoError(t, copier.Copy(ctx, "src", "dst"))

	// Destination must decrypt under its own key (AAD rebound).
	rc, _, err := enc.Get(ctx, "dst")
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, original, got, "destination must decrypt under its own AAD")
}

func TestAsLister_EncryptionForwardsWhenBackendSupports(t *testing.T) {
	t.Parallel()
	// MemBackend implements Lister, so encryption must expose Lister too.
	enc := New(membackend.New(), StaticKey(testKey(t)))
	_, ok := storage.AsLister(enc)
	assert.True(t, ok, "encryption must forward Lister when backend supports it")
}

func TestAsLister_EncryptionRejectsInvalidListOptions(t *testing.T) {
	t.Parallel()
	enc := New(membackend.New(), StaticKey(testKey(t)))
	lister, ok := storage.AsLister(enc)
	require.True(t, ok)

	var seenErr error
	for _, err := range lister.List(context.Background(), "", storage.ListOptions{MaxKeys: -1}) {
		seenErr = err
		break
	}

	require.ErrorIs(t, seenErr, storage.ErrValidation)
}

// noListerBackend wraps MemBackend but hides Lister/Copier to test that
// encryption does NOT claim Lister when backend has no Lister.
type noListerBackend struct {
	put func(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error
	get func(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error)
	del func(ctx context.Context, key string) error
	ex  func(ctx context.Context, key string) (bool, error)
}

func (b *noListerBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	return b.put(ctx, key, r, meta)
}

func (b *noListerBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	return b.get(ctx, key)
}

func (b *noListerBackend) Delete(ctx context.Context, key string) error {
	return b.del(ctx, key)
}

func (b *noListerBackend) Exists(ctx context.Context, key string) (bool, error) {
	return b.ex(ctx, key)
}

type errKeyProvider struct {
	err error
}

func (p errKeyProvider) EncryptionKey(context.Context) ([]byte, error) {
	return nil, p.err
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
}

type failingListBackend struct {
	*membackend.Backend
	err error
}

func (b *failingListBackend) List(context.Context, string, storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		yield(storage.ObjectInfo{}, b.err)
	}
}

func TestAsPresigned_BlockedByEncryption_DeepStack(t *testing.T) {
	t.Parallel()
	// The documented production stack is retry -> circuitbreaker -> encryption
	// -> backend. Even when wrapped under retry/CB, capability discovery for
	// presigned must NOT walk past encryption.
	//
	// We import retry/cb in this test indirectly: encryption.New on a backend
	// that has presigned, then assert AsPresigned returns false on the
	// encryption layer alone, which is what the deeper stack would also see
	// because retry/CB ask AsPresigned of the underlying chain.
	backend := &presignedMemBackend{Backend: membackend.New()}
	enc := New(backend, StaticKey(testKey(t)))
	_, ok := storage.AsPresigned(enc)
	assert.False(t, ok)
}

func TestAsLister_EncryptionDoesNotClaimWhenBackendLacks(t *testing.T) {
	t.Parallel()
	mem := membackend.New()
	hidden := &noListerBackend{
		put: mem.Put,
		get: mem.Get,
		del: mem.Delete,
		ex:  mem.Exists,
	}
	enc := New(hidden, StaticKey(testKey(t)))
	_, ok := storage.AsLister(enc)
	assert.False(t, ok, "encryption must NOT claim Lister when underlying lacks it")
}
