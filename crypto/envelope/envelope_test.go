package envelope_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
	"github.com/bds421/rho-kit/crypto/v2/envelope/kekstatic"
)

func newKEK(t *testing.T, keyID string) *kekstatic.KEK {
	t.Helper()
	mk := newMasterKey(t)
	k, err := kekstatic.NewKEK(keyID, mk)
	require.NoError(t, err)
	return k
}

func newMasterKey(t *testing.T) []byte {
	t.Helper()
	mk := make([]byte, 32)
	_, err := rand.Read(mk)
	require.NoError(t, err)
	return mk
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	pt := []byte("hello world")
	blob, err := enc.Encrypt(context.Background(), pt, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(blob), string(pt))

	got, err := enc.Decrypt(context.Background(), blob, nil)
	require.NoError(t, err)
	assert.Equal(t, pt, got)
}

func TestEncryptDecrypt_AADBound(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), []byte("tenant=acme"))
	require.NoError(t, err)

	// Correct AAD → succeeds.
	pt, err := enc.Decrypt(context.Background(), blob, []byte("tenant=acme"))
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), pt)

	// Wrong AAD → fails closed.
	_, err = enc.Decrypt(context.Background(), blob, []byte("tenant=evil"))
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)

	// Missing AAD → fails closed.
	_, err = enc.Decrypt(context.Background(), blob, nil)
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
}

func TestEncryptDecrypt_TamperedHeaderRejected(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Flip a bit in the keyID byte (offset 5+).
	blob[5] ^= 0x01
	_, err = enc.Decrypt(context.Background(), blob, nil)
	require.Error(t, err) // either unknown keyID or auth-failed
}

func TestEncryptDecrypt_EmptyPlaintextRoundTrips(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	blob, err := enc.Encrypt(context.Background(), nil, nil)
	require.NoError(t, err)

	got, err := enc.Decrypt(context.Background(), blob, nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	blob2, err := enc.Encrypt(context.Background(), []byte{}, []byte("aad"))
	require.NoError(t, err)
	got2, err := enc.Decrypt(context.Background(), blob2, []byte("aad"))
	require.NoError(t, err)
	assert.Empty(t, got2)
}

func TestDecrypt_RejectsTruncated(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	_, err = enc.Decrypt(context.Background(), blob[:8], nil)
	assert.ErrorIs(t, err, envelope.ErrTruncated)
}

func TestDecrypt_RejectsBadMagic(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)
	_, err := enc.Decrypt(context.Background(), []byte("not-an-envelope-blob"), nil)
	assert.ErrorIs(t, err, envelope.ErrMalformed)
}

func TestDecrypt_RejectsWrongVersion(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)
	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Bump version byte.
	blob[3] = 99
	_, err = enc.Decrypt(context.Background(), blob, nil)
	assert.ErrorIs(t, err, envelope.ErrUnsupportedVer)
}

func TestRotation_OldBlobsStillReadable(t *testing.T) {
	mk1 := newMasterKey(t)
	mk2 := newMasterKey(t)

	k, err := kekstatic.NewKEK("v1", mk1)
	require.NoError(t, err)
	enc := envelope.NewEncryptor(k)

	// Encrypt under v1.
	blobV1, err := enc.Encrypt(context.Background(), []byte("legacy"), nil)
	require.NoError(t, err)

	// Add v2 and rotate.
	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	// New writes use v2.
	blobV2, err := enc.Encrypt(context.Background(), []byte("fresh"), nil)
	require.NoError(t, err)
	assert.NotEqual(t, blobV1, blobV2)

	// Both still decrypt — v1 is still registered.
	got1, err := enc.Decrypt(context.Background(), blobV1, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("legacy"), got1)

	got2, err := enc.Decrypt(context.Background(), blobV2, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("fresh"), got2)
}

func TestRewrap_RewrapsUnderActiveKey(t *testing.T) {
	mk1 := newMasterKey(t)
	mk2 := newMasterKey(t)

	k, _ := kekstatic.NewKEK("v1", mk1)
	enc := envelope.NewEncryptor(k)

	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Rotate to v2.
	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	rewrapped, err := enc.Rewrap(context.Background(), blob)
	require.NoError(t, err)

	// Remove v1 — rewrapped must still decrypt under v2.
	require.NoError(t, k.RemoveKey("v1"))
	got, err := enc.Decrypt(context.Background(), rewrapped, nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), got)
}

func TestRewrap_PreservesAADBinding(t *testing.T) {
	mk1 := newMasterKey(t)
	mk2 := newMasterKey(t)

	k, _ := kekstatic.NewKEK("v1", mk1)
	enc := envelope.NewEncryptor(k)

	aad := []byte("tenant=acme,row=42")
	blob, err := enc.Encrypt(context.Background(), []byte("secret"), aad)
	require.NoError(t, err)

	require.NoError(t, k.AddKey("v2", mk2))
	require.NoError(t, k.Rotate("v2"))

	rewrapped, err := enc.Rewrap(context.Background(), blob)
	require.NoError(t, err)

	// Body ciphertext bytes must be unchanged — Rewrap rewrites only
	// the wrap header.
	_, _, _, oldBody := splitForTest(t, blob)
	_, _, _, newBody := splitForTest(t, rewrapped)
	assert.Equal(t, oldBody, newBody, "Rewrap must not touch nonce+ciphertext")

	// Same AAD still decrypts under the new wrap key.
	require.NoError(t, k.RemoveKey("v1"))
	got, err := enc.Decrypt(context.Background(), rewrapped, aad)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got)

	// Wrong/missing AAD still fails closed after rewrap.
	_, err = enc.Decrypt(context.Background(), rewrapped, []byte("tenant=evil"))
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
	_, err = enc.Decrypt(context.Background(), rewrapped, nil)
	assert.ErrorIs(t, err, envelope.ErrAuthFailed)
}

func TestRewrap_RejectsMalformedBlob(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)
	_, err := enc.Rewrap(context.Background(), []byte("not-an-envelope"))
	assert.Error(t, err)
}

func TestEncryptor_InvalidReceiverReturnsError(t *testing.T) {
	var nilEncryptor *envelope.Encryptor
	for name, enc := range map[string]*envelope.Encryptor{
		"nil":  nilEncryptor,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := enc.Encrypt(context.Background(), []byte("payload"), nil); !errors.Is(err, envelope.ErrInvalidEncryptor) {
				t.Fatalf("Encrypt error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := enc.Decrypt(context.Background(), []byte("blob"), nil); !errors.Is(err, envelope.ErrInvalidEncryptor) {
				t.Fatalf("Decrypt error = %v, want ErrInvalidEncryptor", err)
			}
			if _, err := enc.Rewrap(context.Background(), []byte("blob")); !errors.Is(err, envelope.ErrInvalidEncryptor) {
				t.Fatalf("Rewrap error = %v, want ErrInvalidEncryptor", err)
			}
		})
	}
}

func TestEncryptor_NilContextReturnsError(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)
	ctx := nilContextForTest()
	if _, err := enc.Encrypt(ctx, []byte("payload"), nil); !errors.Is(err, envelope.ErrInvalidContext) {
		t.Fatalf("Encrypt nil context error = %v, want ErrInvalidContext", err)
	}
	if _, err := enc.Decrypt(ctx, []byte("blob"), nil); !errors.Is(err, envelope.ErrInvalidContext) {
		t.Fatalf("Decrypt nil context error = %v, want ErrInvalidContext", err)
	}
	if _, err := enc.Rewrap(ctx, []byte("blob")); !errors.Is(err, envelope.ErrInvalidContext) {
		t.Fatalf("Rewrap nil context error = %v, want ErrInvalidContext", err)
	}
}

func TestEncrypt_RejectsInvalidKEKOutput(t *testing.T) {
	for name, fake := range map[string]envelope.KEK{
		"empty-key-id":     fakeKEK{keyID: "", wrapped: []byte("wrapped")},
		"control-key-id":   fakeKEK{keyID: "v1\n", wrapped: []byte("wrapped")},
		"invalid-key-id":   fakeKEK{keyID: string([]byte{'v', 0xff}), wrapped: []byte("wrapped")},
		"empty-wrapped":    fakeKEK{keyID: "v1", wrapped: nil},
		"oversize-wrapped": fakeKEK{keyID: "v1", wrapped: make([]byte, 0x1_0000)},
	} {
		t.Run(name, func(t *testing.T) {
			enc := envelope.NewEncryptor(fake)
			if _, err := enc.Encrypt(context.Background(), []byte("payload"), nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestDecrypt_RejectsEmptyHeaderFields(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	emptyKeyID := []byte{'E', 'N', 'V', 2, 0, 0, 1, 'x'}
	if _, err := enc.Decrypt(context.Background(), emptyKeyID, nil); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("empty keyID error = %v, want ErrMalformed", err)
	}

	emptyWrapped := []byte{'E', 'N', 'V', 2, 2, 'v', '1', 0, 0, 'b', 'o', 'd', 'y'}
	if _, err := enc.Decrypt(context.Background(), emptyWrapped, nil); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("empty wrapped DEK error = %v, want ErrMalformed", err)
	}

	controlKeyID := []byte{'E', 'N', 'V', 2, 2, 'v', '\n', 0, 1, 'x', 'b', 'o', 'd', 'y'}
	if _, err := enc.Decrypt(context.Background(), controlKeyID, nil); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("control keyID error = %v, want ErrMalformed", err)
	}
}

func TestDecryptAndRewrap_RejectInvalidDEKLengthWithStableError(t *testing.T) {
	enc := envelope.NewEncryptor(fakeKEK{keyID: "v1", wrapped: []byte("wrapped"), unwrapDEK: make([]byte, 16)})
	blob := []byte{'E', 'N', 'V', 2, 2, 'v', '1', 0, 7, 'w', 'r', 'a', 'p', 'p', 'e', 'd'}
	blob = append(blob, make([]byte, 12)...)

	for name, run := range map[string]func() error{
		"decrypt": func() error {
			_, err := enc.Decrypt(context.Background(), blob, nil)
			return err
		},
		"rewrap": func() error {
			_, err := enc.Rewrap(context.Background(), blob)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "16")
			assert.NotContains(t, err.Error(), "32")
		})
	}
}

// splitForTest mirrors the wire format and returns the body suffix
// (nonce || ciphertext+tag) so tests can compare it across rewraps.
// Assumes v3 blob layout: magic(3) || v(1) || kL(2 BE) || keyID(kL)
// || wL(2 BE) || wDEK(wL) || body.
func splitForTest(t *testing.T, blob []byte) (magic byte, version byte, header []byte, body []byte) {
	t.Helper()
	require.GreaterOrEqual(t, len(blob), 6)
	kL := int(blob[4])<<8 | int(blob[5])
	off := 6 + kL
	require.GreaterOrEqual(t, len(blob), off+2)
	wL := int(blob[off])<<8 | int(blob[off+1])
	off += 2 + wL
	require.GreaterOrEqual(t, len(blob), off)
	return blob[0], blob[3], blob[:off], blob[off:]
}

func TestKEKStatic_RemoveActiveKeyReturnsError(t *testing.T) {
	k := newKEK(t, "v1")
	require.Error(t, k.RemoveKey("v1"))
}

func TestKEKStatic_NilReceiverReturnsErrors(t *testing.T) {
	var k *kekstatic.KEK
	require.Empty(t, k.KeyID())

	_, _, err := k.Wrap(context.Background(), make([]byte, 32))
	require.Error(t, err)
	_, err = k.Unwrap(context.Background(), "v1", []byte("wrapped"))
	require.Error(t, err)
	require.Error(t, k.AddKey("v1", make([]byte, 32)))
	require.Error(t, k.Rotate("v1"))
	require.Error(t, k.RemoveKey("v1"))
}

func TestKEKStatic_ZeroValueAddKeyDoesNotPanic(t *testing.T) {
	var k kekstatic.KEK
	require.NoError(t, k.AddKey("v1", make([]byte, 32)))
	require.NoError(t, k.Rotate("v1"))
}

func TestKEKStatic_RejectsOversizedKeyID(t *testing.T) {
	longKeyID := string(make([]byte, 256))
	_, err := kekstatic.NewKEK(longKeyID, make([]byte, 32))
	require.Error(t, err)

	k := newKEK(t, "v1")
	require.Error(t, k.AddKey(longKeyID, make([]byte, 32)))
}

func TestKEKStatic_RejectsUnsafeKeyID(t *testing.T) {
	for _, keyID := range []string{"v1\n", string([]byte{'v', 0xff})} {
		t.Run("new", func(t *testing.T) {
			_, err := kekstatic.NewKEK(keyID, make([]byte, 32))
			require.Error(t, err)
		})
		t.Run("add", func(t *testing.T) {
			k := newKEK(t, "v1")
			require.Error(t, k.AddKey(keyID, make([]byte, 32)))
		})
	}
}

func TestKEKStatic_UnknownKeyIDRejected(t *testing.T) {
	k := newKEK(t, "v1")
	_, err := k.Unwrap(context.Background(), "secret-token", []byte("garbage"))
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestKEKStatic_RemoveUnknownKeyIDRejected(t *testing.T) {
	k := newKEK(t, "v1")
	require.Error(t, k.RemoveKey("v999"))
}

func TestKEKStatic_KeyIDBoundAsAAD(t *testing.T) {
	mk := newMasterKey(t)

	k, err := kekstatic.NewKEK("keyA", mk)
	require.NoError(t, err)
	require.NoError(t, k.AddKey("keyB", mk))

	enc := envelope.NewEncryptor(k)
	blob, err := enc.Encrypt(context.Background(), []byte("payload"), nil)
	require.NoError(t, err)

	// Tamper the blob's keyID from "keyA" to "keyB". v3 layout:
	// magic(3) || version(1) || keyIDLen(2 BE) || keyID(...) || ...
	require.Equal(t, byte(0), blob[4], "expected keyID length high byte 0")
	require.Equal(t, byte(4), blob[5], "expected keyID length low byte 4")
	require.Equal(t, byte('A'), blob[9])
	blob[9] = 'B'

	_, err = enc.Decrypt(context.Background(), blob, nil)
	assert.Error(t, err, "swapped keyID must fail to unwrap because keyID is bound as AAD")
}

// TestKEKStatic_WrapReturnsActiveKeyID verifies that the keyID returned
// by Wrap matches the active key at the moment of the seal. Decrypting
// under the returned keyID succeeds, which proves the keyID and the
// AAD-bound wrap are mutually consistent.
func TestKEKStatic_WrapReturnsActiveKeyID(t *testing.T) {
	mk := newMasterKey(t)

	k, err := kekstatic.NewKEK("v1", mk)
	require.NoError(t, err)

	dek := make([]byte, 32)
	_, err = rand.Read(dek)
	require.NoError(t, err)

	keyID, wrapped, err := k.Wrap(context.Background(), dek)
	require.NoError(t, err)
	assert.Equal(t, "v1", keyID, "Wrap must return the active key ID")

	got, err := k.Unwrap(context.Background(), keyID, wrapped)
	require.NoError(t, err)
	assert.Equal(t, dek, got)
}

// TestEncrypt_ConsistentUnderConcurrentRotation exercises the rotation
// race that motivated the Wrap interface change. Many goroutines
// Encrypt while a separate goroutine flips Rotate between two keys.
// Every produced blob must decrypt — this would fail if envelope.go
// read keyID separately from the wrap.
func TestEncrypt_ConsistentUnderConcurrentRotation(t *testing.T) {
	mk1 := newMasterKey(t)
	mk2 := newMasterKey(t)

	k, err := kekstatic.NewKEK("v1", mk1)
	require.NoError(t, err)
	require.NoError(t, k.AddKey("v2", mk2))

	enc := envelope.NewEncryptor(k)

	const writers = 8
	const perWriter = 200

	var stop atomic.Bool
	var rotWG sync.WaitGroup
	rotWG.Add(1)
	go func() {
		defer rotWG.Done()
		toggle := false
		for !stop.Load() {
			toggle = !toggle
			if toggle {
				_ = k.Rotate("v2")
			} else {
				_ = k.Rotate("v1")
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(writers)
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			pt := []byte("rotation-race-payload")
			for i := 0; i < perWriter; i++ {
				blob, err := enc.Encrypt(context.Background(), pt, nil)
				if err != nil {
					errs <- err
					return
				}
				got, err := enc.Decrypt(context.Background(), blob, nil)
				if err != nil {
					errs <- err
					return
				}
				if string(got) != string(pt) {
					errs <- assertEqualErr(pt, got)
					return
				}
			}
		}()
	}
	wg.Wait()
	stop.Store(true)
	rotWG.Wait()
	close(errs)

	for e := range errs {
		t.Fatalf("concurrent encrypt/decrypt failed: %v", e)
	}
}

// TestEncrypt_SamePlaintextDistinctCiphertexts is the adversarial DEK
// uniqueness test. Two Encrypt calls on byte-identical plaintext + AAD
// must produce byte-distinct blobs, and decryption of both must
// round-trip to the original plaintext. The deeper assertion — that the
// per-blob GCM nonces also differ — pins the property end users actually
// care about: nonce reuse under a single DEK breaks AES-GCM
// confidentiality and authenticity outright, and even with per-blob
// fresh DEKs we want to verify the nonce path is not silently
// deterministic.
//
// The blob layout (v3) is:
//
//	magic(3) | version(1) | uint16(kL) | keyID(kL) | uint16(wL) | wrapped(wL) | nonce(12) | ciphertext+tag(>=16)
//
// We re-parse the header by hand rather than reaching into the package
// internals so the test acts as a black-box check on the layout
// guarantee.
func TestEncrypt_SamePlaintextDistinctCiphertexts(t *testing.T) {
	k := newKEK(t, "v1")
	enc := envelope.NewEncryptor(k)

	pt := []byte("same-plaintext")

	blob1, err := enc.Encrypt(context.Background(), pt, nil)
	require.NoError(t, err)
	blob2, err := enc.Encrypt(context.Background(), pt, nil)
	require.NoError(t, err)

	// Whole-blob inequality is the user-observable surface: two writes
	// of "the same secret" must not produce the same ciphertext on
	// disk (otherwise equal-plaintext detection via byte compare leaks
	// information).
	assert.False(t, bytes.Equal(blob1, blob2),
		"two Encrypt calls on identical plaintext returned byte-equal blobs")

	// Both must still round-trip.
	got1, err := enc.Decrypt(context.Background(), blob1, nil)
	require.NoError(t, err)
	assert.Equal(t, pt, got1)
	got2, err := enc.Decrypt(context.Background(), blob2, nil)
	require.NoError(t, err)
	assert.Equal(t, pt, got2)

	// The deeper invariant: the GCM nonces in the two bodies must
	// differ. Equal nonces under a fresh DEK is fine cryptographically
	// (a different key means a fresh nonce space), but equal nonces
	// would also mean a deterministic nonce derivation path — exactly
	// the bug that bites callers who later swap the KEK for one whose
	// Wrap is deterministic and re-uses a DEK across writes.
	nonce1 := extractGCMNonce(t, blob1)
	nonce2 := extractGCMNonce(t, blob2)
	assert.False(t, bytes.Equal(nonce1, nonce2),
		"two Encrypt calls returned identical GCM nonces: nonce1=%x nonce2=%x", nonce1, nonce2)
}

// extractGCMNonce parses a v3 envelope blob and returns the first 12
// bytes of the body segment — the AES-GCM nonce. It deliberately
// re-implements the parse so the test does not delegate the property
// it is asserting on to the same code path that produced the blob.
//
// Layout (v3):
//
//	[0:3]   magic "ENV"
//	[3]     version (must be 3)
//	[4:6]   uint16 BE keyID length (kL)
//	[6:6+kL] keyID
//	[6+kL : 6+kL+2] uint16 BE wrapped DEK length (wL)
//	[6+kL+2 : 6+kL+2+wL] wrapped DEK
//	[6+kL+2+wL : +12] GCM nonce
//	[...]   ciphertext + 16-byte tag
func extractGCMNonce(t *testing.T, blob []byte) []byte {
	t.Helper()
	require.GreaterOrEqual(t, len(blob), 6, "blob too short for v3 header preamble")
	require.Equal(t, byte('E'), blob[0], "bad magic byte 0")
	require.Equal(t, byte('N'), blob[1], "bad magic byte 1")
	require.Equal(t, byte('V'), blob[2], "bad magic byte 2")
	require.Equal(t, byte(3), blob[3], "expected v3 blob version, got %d", blob[3])

	kL := int(binary.BigEndian.Uint16(blob[4:6]))
	off := 6 + kL
	require.GreaterOrEqual(t, len(blob), off+2, "blob truncated before wrapped-DEK length")

	wL := int(binary.BigEndian.Uint16(blob[off : off+2]))
	off += 2 + wL
	require.GreaterOrEqual(t, len(blob), off+12, "blob truncated before GCM nonce")

	// Return a copy so callers cannot accidentally mutate the blob.
	nonce := make([]byte, 12)
	copy(nonce, blob[off:off+12])
	return nonce
}

func assertEqualErr(want, got []byte) error {
	return &mismatchError{want: string(want), got: string(got)}
}

type mismatchError struct{ want, got string }

func (e *mismatchError) Error() string {
	return "plaintext mismatch: want=" + e.want + " got=" + e.got
}

type fakeKEK struct {
	keyID     string
	wrapped   []byte
	unwrapDEK []byte
}

func (f fakeKEK) KeyID() string { return f.keyID }

func (f fakeKEK) Wrap(context.Context, []byte) (string, []byte, error) {
	return f.keyID, f.wrapped, nil
}

func (f fakeKEK) Unwrap(context.Context, string, []byte) ([]byte, error) {
	if f.unwrapDEK != nil {
		return f.unwrapDEK, nil
	}
	dek := make([]byte, 32)
	return dek, nil
}

func nilContextForTest() context.Context { return nil }
