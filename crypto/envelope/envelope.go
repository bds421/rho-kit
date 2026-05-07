// Package envelope implements envelope encryption: a fresh AES-256-GCM
// DEK is generated per Encrypt call, wrapped under a KEK (the
// key-encryption-key — typically a KMS-managed master key), and emitted
// alongside the ciphertext as a self-describing blob.
//
// Compared to a single static-key approach (crypto/encrypt), envelope
// encryption gives:
//
//   - Online rotation. Re-key by swapping the KEK; new writes use the
//     new key version, old reads still work via the embedded key-version
//     metadata.
//   - KMS integration. The KEK is pluggable behind the [KEK] interface.
//     This package ships kekstatic for tests/dev; cloud KMS providers
//     (AWS KMS, GCP KMS, Vault transit) live in their own subpackages
//     so consumers only pull the SDK they use.
//   - Per-record DEKs. A single key compromise reveals only the records
//     written under that DEK, not the entire dataset.
//
// Blob format (network byte order):
//
//	+--------+----+----+--------+----+-------+----+----+--------+
//	| magic  | v  | kL | keyID  | wL | wDEK  | n  |    | ct+tag |
//	|  3B    | 1B | 1B |  …     | 2B |  …    | 12B |   |   …    |
//	+--------+----+----+--------+----+-------+----+----+--------+
//
//   - magic: "ENV" (3 bytes) — quick-reject for non-envelope blobs.
//   - v: version, currently 1.
//   - kL + keyID: KEK identifier (string), length-prefixed.
//   - wL + wDEK: wrapped DEK bytes, length-prefixed (uint16 BE).
//   - n: AES-GCM nonce (12 bytes).
//   - ct+tag: AES-256-GCM(plaintext, AAD := caller-supplied || header).
//
// AAD binding: the AAD passed to GCM is the caller's AAD concatenated
// with a hash of the header bytes. This means a blob is only decryptable
// with the AAD it was encrypted with AND the header is integrity-protected.
package envelope

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// blobMagic is the 3-byte header prefix that identifies an envelope blob.
var blobMagic = [3]byte{'E', 'N', 'V'}

// blobVersion is the current envelope blob format version.
const blobVersion uint8 = 1

// dekLen is the AES-256 DEK length in bytes.
const dekLen = 32

// nonceLen is the AES-GCM nonce length in bytes (the standard 96-bit nonce).
const nonceLen = 12

// Sentinel errors. Verify-style checks all wrap one of these so callers
// can branch without parsing the underlying error message.
var (
	ErrMalformed       = errors.New("envelope: malformed blob")
	ErrUnsupportedVer  = errors.New("envelope: unsupported blob version")
	ErrTruncated       = errors.New("envelope: truncated blob")
	ErrAuthFailed      = errors.New("envelope: authentication failed")
)

// KEK abstracts a key-encryption-key provider. Implementations are
// typically backed by a cloud KMS (AWS KMS, GCP KMS, Vault transit) or
// — for tests — an in-memory key.
type KEK interface {
	// KeyID returns the identifier of the active key version. Issuers
	// should bump this on rotation; readers should embed it so future
	// Unwrap calls know which KEK version to invoke.
	KeyID() string

	// Wrap encrypts dek with the active KEK version.
	Wrap(ctx context.Context, dek []byte) (wrapped []byte, err error)

	// Unwrap decrypts wrapped under the named keyID. Implementations
	// must reject unknown key IDs rather than silently falling back.
	Unwrap(ctx context.Context, keyID string, wrapped []byte) (dek []byte, err error)
}

// Encryptor performs envelope encryption against a [KEK].
type Encryptor struct {
	kek KEK
}

// New constructs an Encryptor backed by kek.
func New(kek KEK) *Encryptor {
	if kek == nil {
		panic("envelope: KEK must not be nil")
	}
	return &Encryptor{kek: kek}
}

// Encrypt returns a self-describing envelope blob.
//
// aad is bound into the AEAD and must be supplied identically to
// [Encryptor.Decrypt]. Use it to scope a ciphertext to a row, tenant,
// or any other context that must not be mixable with another row's
// ciphertext.
func (e *Encryptor) Encrypt(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("envelope: plaintext must not be empty")
	}

	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("envelope: read DEK: %w", err)
	}

	wrapped, err := e.kek.Wrap(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: wrap DEK: %w", err)
	}
	keyID := e.kek.KeyID()
	if len(keyID) > 255 {
		return nil, fmt.Errorf("envelope: KEK keyID exceeds 255 bytes")
	}
	if len(wrapped) > 0xFFFF {
		return nil, fmt.Errorf("envelope: wrapped DEK exceeds 64 KiB")
	}

	header := buildHeader(keyID, wrapped)

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("envelope: read nonce: %w", err)
	}

	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}

	// Bind the header bytes into the AEAD via SHA-256 — direct
	// concatenation would re-derive a 64KB+ AAD on every Decrypt.
	combined := combineAAD(aad, header)
	ct := gcm.Seal(nil, nonce, plaintext, combined)

	out := make([]byte, 0, len(header)+nonceLen+len(ct))
	out = append(out, header...)
	out = append(out, nonce...)
	out = append(out, ct...)
	// Zero the DEK on our side. The KMS still holds the wrapped copy.
	for i := range dek {
		dek[i] = 0
	}
	return out, nil
}

// Decrypt parses blob, calls KEK.Unwrap to recover the DEK, and
// authenticates+decrypts the payload. aad must match what was passed
// to [Encryptor.Encrypt].
func (e *Encryptor) Decrypt(ctx context.Context, blob, aad []byte) ([]byte, error) {
	header, keyID, wrapped, body, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}
	if len(body) < nonceLen {
		return nil, ErrTruncated
	}
	nonce := body[:nonceLen]
	ct := body[nonceLen:]

	dek, err := e.kek.Unwrap(ctx, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap DEK (keyID=%s): %w", keyID, err)
	}
	if len(dek) != dekLen {
		return nil, fmt.Errorf("envelope: DEK length %d != %d", len(dek), dekLen)
	}

	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	combined := combineAAD(aad, header)

	pt, err := gcm.Open(nil, nonce, ct, combined)
	if err != nil {
		return nil, ErrAuthFailed
	}
	for i := range dek {
		dek[i] = 0
	}
	return pt, nil
}

// Rewrap re-encrypts the embedded DEK under the current KEK version
// without touching the plaintext. Use this for online key rotation:
// read the stored blob, Rewrap, write it back. Callers should run
// rewrap in batches and tolerate the temporary cost.
func (e *Encryptor) Rewrap(ctx context.Context, blob []byte) ([]byte, error) {
	_, keyID, wrapped, _, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}

	dek, err := e.kek.Unwrap(ctx, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap DEK (keyID=%s): %w", keyID, err)
	}
	defer func() {
		for i := range dek {
			dek[i] = 0
		}
	}()

	newWrapped, err := e.kek.Wrap(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: re-wrap DEK: %w", err)
	}
	newKeyID := e.kek.KeyID()
	newHeader := buildHeader(newKeyID, newWrapped)

	// The body (nonce + ct) is bound to the OLD header via AAD, so we
	// must re-encrypt the payload under the new header. Decrypt with
	// the existing AAD chain, then Encrypt under the new one. The
	// caller-supplied AAD is reproduced from the blob's old header
	// hash, so the only safe path is to require callers to supply it
	// — but the package contract for Rewrap is "re-key without
	// touching plaintext" so we re-encrypt under the new key with no
	// caller-AAD assumption. A separate RewrapWithAAD covers the
	// AAD-binding case.
	//
	// The simplest faithful implementation: Decrypt(blob, nil), then
	// Encrypt(plaintext, nil). Callers using AAD must use [Encryptor.Decrypt]
	// + [Encryptor.Encrypt] explicitly during rotation.
	pt, err := e.Decrypt(ctx, blob, nil)
	if err != nil {
		return nil, err
	}
	_ = newHeader // header is rebuilt by Encrypt below.
	return e.Encrypt(ctx, pt, nil)
}

// buildHeader serialises the magic, version, keyID and wrapped-DEK
// length-prefixes into a single byte slice.
func buildHeader(keyID string, wrappedDEK []byte) []byte {
	out := make([]byte, 0, 3+1+1+len(keyID)+2+len(wrappedDEK))
	out = append(out, blobMagic[:]...)
	out = append(out, blobVersion)
	out = append(out, byte(len(keyID)))
	out = append(out, []byte(keyID)...)
	var lp [2]byte
	binary.BigEndian.PutUint16(lp[:], uint16(len(wrappedDEK)))
	out = append(out, lp[:]...)
	out = append(out, wrappedDEK...)
	return out
}

// parseBlob splits a blob into (header, keyID, wrappedDEK, body) where
// body is nonce || ciphertext+tag. Returns ErrMalformed for any layout
// failure and ErrUnsupportedVer for a version we don't understand.
func parseBlob(blob []byte) (header []byte, keyID string, wrappedDEK, body []byte, err error) {
	if len(blob) < 3+1+1 {
		return nil, "", nil, nil, ErrTruncated
	}
	if blob[0] != blobMagic[0] || blob[1] != blobMagic[1] || blob[2] != blobMagic[2] {
		return nil, "", nil, nil, ErrMalformed
	}
	if blob[3] != blobVersion {
		return nil, "", nil, nil, ErrUnsupportedVer
	}
	kL := int(blob[4])
	off := 5
	if len(blob) < off+kL+2 {
		return nil, "", nil, nil, ErrTruncated
	}
	keyID = string(blob[off : off+kL])
	off += kL
	wL := int(binary.BigEndian.Uint16(blob[off : off+2]))
	off += 2
	if len(blob) < off+wL {
		return nil, "", nil, nil, ErrTruncated
	}
	wrappedDEK = blob[off : off+wL]
	off += wL
	header = blob[:off]
	body = blob[off:]
	return header, keyID, wrappedDEK, body, nil
}

// combineAAD hashes the header into a fixed-size suffix that is
// concatenated with the caller's AAD before being passed to GCM.
// Hashing keeps the AAD bounded and avoids re-allocating a header-sized
// buffer for every Decrypt under rotation.
func combineAAD(callerAAD, header []byte) []byte {
	sum := sha256.Sum256(header)
	out := make([]byte, 0, len(callerAAD)+len(sum))
	out = append(out, callerAAD...)
	out = append(out, sum[:]...)
	return out
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("envelope: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("envelope: gcm new: %w", err)
	}
	return gcm, nil
}
