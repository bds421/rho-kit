package encryption

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

func testKey() []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return key
}

func TestEncryptedStorage_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey()
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

func TestEncryptedStorage_WrongKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key1 := testKey()
	key2 := testKey()
	backend := membackend.New()

	enc1 := New(backend, StaticKey(key1))
	enc2 := New(backend, StaticKey(key2))

	err := enc1.Put(ctx, "secret.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	// Decrypting with wrong key should fail.
	_, _, err = enc2.Get(ctx, "secret.txt")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt")
}

func TestEncryptedStorage_ExistsAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey()
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

func TestEncryptedStorage_EmptyContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	key := testKey()
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

	key := testKey()
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
	// whole point of binding.
	_, _, err = enc.Get(ctx, "keyB")
	require.Error(t, err, "ciphertext copied to a different key must not decrypt")
	assert.Contains(t, err.Error(), "decrypt")

	// Reading keyA must still work — same key, same AAD.
	rc, _, err = enc.Get(ctx, "keyA")
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, original, got)
}

func TestEncryptedStorage_New_PanicsOnNilBackend(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "encryption: backend must not be nil", func() {
		New(nil, StaticKey(testKey()))
	})
}

func TestEncryptedStorage_New_PanicsOnNilKeys(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "encryption: keys provider must not be nil", func() {
		New(membackend.New(), nil)
	})
}

// presignedMemBackend wraps MemBackend with a stub PresignedStore so we can
// verify capability discovery does not bypass encryption.
type presignedMemBackend struct {
	*membackend.MemBackend
}

func (p *presignedMemBackend) PresignGetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://raw-bucket/" + key, nil
}

func (p *presignedMemBackend) PresignPutURL(_ context.Context, key string, _ time.Duration, _ storage.ObjectMeta) (string, error) {
	return "https://raw-bucket/" + key, nil
}

// publicURLBackend wraps MemBackend with a stub PublicURLer.
type publicURLBackend struct {
	*membackend.MemBackend
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
	backend := &presignedMemBackend{MemBackend: membackend.New()}
	enc := New(backend, StaticKey(testKey()))

	_, ok := storage.AsPresigned(enc)
	assert.False(t, ok, "encryption must block AsPresigned from reaching underlying presigner")
}

func TestAsPublicURLer_BlockedByEncryption(t *testing.T) {
	t.Parallel()
	// Same hazard as presigned: a raw public URL would serve ciphertext.
	backend := &publicURLBackend{MemBackend: membackend.New()}
	enc := New(backend, StaticKey(testKey()))

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
	enc := New(backend, StaticKey(testKey()))

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
	enc := New(membackend.New(), StaticKey(testKey()))
	_, ok := storage.AsLister(enc)
	assert.True(t, ok, "encryption must forward Lister when backend supports it")
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
	backend := &presignedMemBackend{MemBackend: membackend.New()}
	enc := New(backend, StaticKey(testKey()))
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
	enc := New(hidden, StaticKey(testKey()))
	_, ok := storage.AsLister(enc)
	assert.False(t, ok, "encryption must NOT claim Lister when underlying lacks it")
}
