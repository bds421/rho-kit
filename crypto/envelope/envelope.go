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
//     metadata. [Encryptor.Rewrap] re-keys an existing blob without
//     touching plaintext or AAD.
//   - KMS integration. The KEK is pluggable behind the [KEK] interface.
//     This package ships kekstatic for tests/dev; cloud KMS providers
//     (AWS KMS, GCP KMS, Vault transit) live in their own subpackages
//     so consumers only pull the SDK they use.
//   - Per-record DEKs. A single key compromise reveals only the records
//     written under that DEK, not the entire dataset.
//
// Blob format (network byte order, version 2):
//
//		+--------+----+----+--------+----+-------+----+--------+
//		| magic  | v  | kL | keyID  | wL | wDEK  | n  | ct+tag |
//		|  3B    | 1B | 1B |  …     | 2B |  …    | 12B|   …    |
//		+--------+----+----+--------+----+-------+----+--------+
//
//	  - magic: "ENV" (3 bytes) — quick-reject for non-envelope blobs.
//	  - v: version, currently 2.
//	  - kL + keyID: KEK identifier (string), length-prefixed.
//	  - wL + wDEK: wrapped DEK bytes, length-prefixed (uint16 BE).
//	  - n: AES-GCM nonce (12 bytes).
//	  - ct+tag: AES-256-GCM(plaintext, AAD := caller-supplied || domainSep).
//
// AAD binding (v2): the AAD passed to the body GCM is the caller's AAD
// concatenated with a fixed domain-separator constant. The wrap header
// (keyID, wDEK) is NOT included in the body AAD — this is what makes
// [Encryptor.Rewrap] work without re-encrypting the plaintext. Tampering
// with the wrap header is detected by the KEK's own AEAD tag on the
// wrapped DEK: an attacker who swaps wDEK either gets rejected by the
// KEK or recovers a wrong DEK that fails the body's GCM-Open. Tampering
// with keyID is detected the same way (unknown keyID rejected by KEK,
// or wrong DEK fails body open).
package envelope

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/tink-crypto/tink-go/v2/aead/subtle"
	"github.com/tink-crypto/tink-go/v2/tink"
)

// blobMagic is the 3-byte header prefix that identifies an envelope blob.
var blobMagic = [3]byte{'E', 'N', 'V'}

// blobVersion is the current envelope blob format version.
const blobVersion uint8 = 2

// dekLen is the AES-256 DEK length in bytes.
const dekLen = 32

// nonceLen is the AES-GCM nonce length in bytes (the standard 96-bit nonce).
const nonceLen = 12

// aadDomainSep is a fixed AAD suffix that scopes a body GCM seal to
// this package and version. It is NOT a substitute for caller-supplied
// AAD — its job is to prevent cross-context confusion (a DEK reused
// across formats wouldn't decrypt a payload from another format).
var aadDomainSep = []byte("rho-kit/envelope/v2")

// Sentinel errors. Verify-style checks all wrap one of these so callers
// can branch without parsing the underlying error message.
var (
	ErrMalformed      = errors.New("envelope: malformed blob")
	ErrUnsupportedVer = errors.New("envelope: unsupported blob version")
	ErrTruncated      = errors.New("envelope: truncated blob")
	ErrAuthFailed     = errors.New("envelope: authentication failed")
)

// KEK abstracts a key-encryption-key provider. Implementations are
// typically backed by a cloud KMS (AWS KMS, GCP KMS, Vault transit) or
// — for tests — an in-memory key.
type KEK interface {
	// KeyID returns the identifier of the active key version. This is
	// for telemetry/debug only; it MUST NOT be used to decide which
	// keyID to embed in an envelope, because rotation between this
	// call and a subsequent Wrap can produce undecryptable blobs. Use
	// the keyID returned by Wrap for envelope writes.
	KeyID() string

	// Wrap encrypts dek with the active KEK version and returns both
	// the wrapped bytes and the keyID under which they were
	// authenticated. Implementations MUST select keyID and seal under
	// the same lock/snapshot so the returned (keyID, wrapped) pair is
	// internally consistent even if rotation happens concurrently.
	// Callers must use the returned keyID for the envelope header,
	// never KeyID(), to avoid a TOCTOU rotation race.
	Wrap(ctx context.Context, dek []byte) (keyID string, wrapped []byte, err error)

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
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("envelope: read DEK: %w", err)
	}
	defer zeroBytes(dek)

	// Wrap returns (keyID, wrapped) atomically: a separate KeyID() call
	// could observe a different active key after rotation, leaving the
	// header and the AAD-bound wrap inconsistent.
	keyID, wrapped, err := e.kek.Wrap(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: wrap DEK: %w", err)
	}
	if len(keyID) > 255 {
		return nil, fmt.Errorf("envelope: KEK keyID exceeds 255 bytes")
	}
	if len(wrapped) > 0xFFFF {
		return nil, fmt.Errorf("envelope: wrapped DEK exceeds 64 KiB")
	}

	header := buildHeader(keyID, wrapped)

	bodyAEAD, err := newBodyAEAD(dek)
	if err != nil {
		return nil, err
	}

	// Tink's Encrypt prepends a fresh 12-byte IV to its output, so
	// the returned slice already has the layout the envelope stores
	// in its body segment ("iv ‖ ct ‖ tag"). No separate nonce write
	// is needed — the format on disk is identical to the pre-Tink
	// implementation that called gcm.Seal(nonce, …).
	sealed, err := bodyAEAD.Encrypt(plaintext, combineAAD(aad))
	if err != nil {
		return nil, fmt.Errorf("envelope: seal body: %w", err)
	}

	out := make([]byte, 0, len(header)+len(sealed))
	out = append(out, header...)
	out = append(out, sealed...)
	return out, nil
}

// Decrypt parses blob, calls KEK.Unwrap to recover the DEK, and
// authenticates+decrypts the payload. aad must match what was passed
// to [Encryptor.Encrypt].
func (e *Encryptor) Decrypt(ctx context.Context, blob, aad []byte) ([]byte, error) {
	_, keyID, wrapped, body, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}
	if len(body) < nonceLen {
		return nil, ErrTruncated
	}

	dek, err := e.kek.Unwrap(ctx, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap DEK (keyID=%s): %w", keyID, err)
	}
	defer zeroBytes(dek)
	if len(dek) != dekLen {
		return nil, fmt.Errorf("envelope: DEK length %d != %d", len(dek), dekLen)
	}

	bodyAEAD, err := newBodyAEAD(dek)
	if err != nil {
		return nil, err
	}

	// body has the layout Tink expects ("iv ‖ ct ‖ tag"). Decrypt
	// fails closed on tampered ciphertext, swapped wrap headers
	// (wrong DEK), or AAD mismatch — see package doc for the
	// rationale on why the wrap header is excluded from the body AAD.
	pt, err := bodyAEAD.Decrypt(body, combineAAD(aad))
	if err != nil {
		return nil, ErrAuthFailed
	}
	return pt, nil
}

// Rewrap re-encrypts the embedded DEK under the current KEK version
// without touching the plaintext. Use this for online key rotation:
// read the stored blob, Rewrap, write it back. AAD-bound blobs are
// preserved unchanged — Rewrap does NOT need the caller's AAD because
// it does not decrypt or re-encrypt the body, only the wrapped DEK.
//
// In v2 of the format, the wrap header (keyID + wrapped DEK) is
// deliberately NOT bound to the body GCM AAD. This makes a true rewrap
// possible: unwrap the DEK with the old key, wrap that same DEK under
// the active key, and emit a new blob with the new wrap header and
// the original nonce+ciphertext. The wrap is itself authenticated by
// the KEK's AEAD tag, so swapping wraps is detected: a forged wrap
// either fails KEK.Unwrap or yields a wrong DEK that fails the body's
// GCM-Open at decrypt time.
func (e *Encryptor) Rewrap(ctx context.Context, blob []byte) ([]byte, error) {
	_, keyID, wrapped, body, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}
	if len(body) < nonceLen {
		return nil, ErrTruncated
	}

	dek, err := e.kek.Unwrap(ctx, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap DEK (keyID=%s): %w", keyID, err)
	}
	defer zeroBytes(dek)
	if len(dek) != dekLen {
		return nil, fmt.Errorf("envelope: DEK length %d != %d", len(dek), dekLen)
	}

	// Wrap returns (keyID, wrapped) atomically: see Encrypt for the
	// rotation race that motivates this contract.
	newKeyID, newWrapped, err := e.kek.Wrap(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: re-wrap DEK: %w", err)
	}
	if len(newKeyID) > 255 {
		return nil, fmt.Errorf("envelope: KEK keyID exceeds 255 bytes")
	}
	if len(newWrapped) > 0xFFFF {
		return nil, fmt.Errorf("envelope: wrapped DEK exceeds 64 KiB")
	}
	newHeader := buildHeader(newKeyID, newWrapped)

	out := make([]byte, 0, len(newHeader)+len(body))
	out = append(out, newHeader...)
	out = append(out, body...)
	return out, nil
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

// combineAAD prefixes the caller's AAD with a fixed domain separator
// so the body GCM seal is scoped to this format and version. The wrap
// header is intentionally not part of this AAD; see [Encryptor.Rewrap]
// for the rationale.
func combineAAD(callerAAD []byte) []byte {
	out := make([]byte, 0, len(callerAAD)+len(aadDomainSep))
	out = append(out, callerAAD...)
	out = append(out, aadDomainSep...)
	return out
}

// zeroBytes wipes b in place. It is used in defer to scrub key material
// regardless of how the surrounding function returns.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// newBodyAEAD returns a Tink-backed AES-256-GCM primitive over the
// supplied DEK. Tink uses RFC 5116 §5.1 layout with a 12-byte IV and
// 16-byte tag — byte-identical to stdlib cipher.AEAD output, so blobs
// produced by the pre-Tink implementation decrypt unchanged.
func newBodyAEAD(dek []byte) (tink.AEAD, error) {
	a, err := subtle.NewAESGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: build AEAD: %w", err)
	}
	return a, nil
}
