package envelope

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/tink-crypto/tink-go/v2/aead/subtle"
	"github.com/tink-crypto/tink-go/v2/tink"
)

// blobMagic is the 3-byte header prefix that identifies an envelope blob.
var blobMagic = [3]byte{'E', 'N', 'V'}

// Blob format versions. v3 length-prefixes the caller-supplied AAD
// and a 16-bit keyID length so two distinct caller-AAD inputs cannot
// collide once concatenated with the domain separator. v2 is accepted
// on the read path for blobs written before the upgrade.
const (
	blobVersionV2 uint8 = 2
	blobVersionV3 uint8 = 3
)

// blobVersion is the version Encrypt writes by default.
const blobVersion = blobVersionV3

// dekLen is the AES-256 DEK length in bytes.
const dekLen = 32

// nonceLen is the AES-GCM nonce length in bytes (the standard 96-bit nonce).
const nonceLen = 12

// aadDomainSepV2 is the fixed AAD suffix used in v2 blobs. v2 readers
// continue to use this so legacy blobs decrypt unchanged.
var aadDomainSepV2 = []byte("rho-kit/envelope/v2")

// aadDomainSepV3 is the fixed AAD prefix used in v3 blobs. The v3
// body AAD is `aadDomainSepV3 || uvarint(len(callerAAD)) || callerAAD`
// — putting the separator FIRST and length-prefixing the caller AAD
// removes the v2 collision in which caller AADs that happened to end
// with bytes resembling the separator could be canonicalised to the
// same MAC input.
var aadDomainSepV3 = []byte("rho-kit/envelope/v3")

// Sentinel errors. Verify-style checks all wrap one of these so callers
// can branch without parsing the underlying error message.
var (
	ErrMalformed        = errors.New("envelope: malformed blob")
	ErrUnsupportedVer   = errors.New("envelope: unsupported blob version")
	ErrTruncated        = errors.New("envelope: truncated blob")
	ErrAuthFailed       = errors.New("envelope: authentication failed")
	ErrInvalidEncryptor = errors.New("envelope: encryptor is not initialized")
	ErrInvalidContext   = errors.New("envelope: context must not be nil")
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

// NewEncryptor constructs an Encryptor backed by kek.
func NewEncryptor(kek KEK) *Encryptor {
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
	if err := e.validate(ctx); err != nil {
		return nil, err
	}
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
	if err := validateKeyID(keyID); err != nil {
		return nil, err
	}
	if len(wrapped) == 0 {
		return nil, fmt.Errorf("envelope: KEK returned empty wrapped DEK")
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
	sealed, err := bodyAEAD.Encrypt(plaintext, combineAAD(blobVersion, aad))
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
	if err := e.validate(ctx); err != nil {
		return nil, err
	}
	version, _, keyID, wrapped, body, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}
	if len(body) < nonceLen {
		return nil, ErrTruncated
	}

	dek, err := e.kek.Unwrap(ctx, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap DEK: %w", err)
	}
	defer zeroBytes(dek)
	if len(dek) != dekLen {
		return nil, fmt.Errorf("envelope: invalid DEK length")
	}

	bodyAEAD, err := newBodyAEAD(dek)
	if err != nil {
		return nil, err
	}

	// body has the layout Tink expects ("iv ‖ ct ‖ tag"). Decrypt
	// fails closed on tampered ciphertext, swapped wrap headers
	// (wrong DEK), or AAD mismatch — see package doc for the
	// rationale on why the wrap header is excluded from the body AAD.
	pt, err := bodyAEAD.Decrypt(body, combineAAD(version, aad))
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
	if err := e.validate(ctx); err != nil {
		return nil, err
	}
	_, _, keyID, wrapped, body, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}
	if len(body) < nonceLen {
		return nil, ErrTruncated
	}

	dek, err := e.kek.Unwrap(ctx, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("envelope: unwrap DEK: %w", err)
	}
	defer zeroBytes(dek)
	if len(dek) != dekLen {
		return nil, fmt.Errorf("envelope: invalid DEK length")
	}

	// Wrap returns (keyID, wrapped) atomically: see Encrypt for the
	// rotation race that motivates this contract.
	newKeyID, newWrapped, err := e.kek.Wrap(ctx, dek)
	if err != nil {
		return nil, fmt.Errorf("envelope: re-wrap DEK: %w", err)
	}
	if err := validateKeyID(newKeyID); err != nil {
		return nil, err
	}
	if len(newWrapped) == 0 {
		return nil, fmt.Errorf("envelope: KEK returned empty wrapped DEK")
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

// buildHeader serialises the v3 header: magic, version, uint16
// keyID length, keyID bytes, uint16 wrapped-DEK length, wrapped DEK.
// Bumping keyID's length prefix from uint8 (v2) to uint16 (v3) gives
// room for fully-qualified GCP and Azure key resources without
// touching the wrapped-DEK length prefix that already used 16 bits.
func buildHeader(keyID string, wrappedDEK []byte) []byte {
	out := make([]byte, 0, 3+1+2+len(keyID)+2+len(wrappedDEK))
	out = append(out, blobMagic[:]...)
	out = append(out, blobVersion)
	var kp [2]byte
	binary.BigEndian.PutUint16(kp[:], uint16(len(keyID)))
	out = append(out, kp[:]...)
	out = append(out, []byte(keyID)...)
	var lp [2]byte
	binary.BigEndian.PutUint16(lp[:], uint16(len(wrappedDEK)))
	out = append(out, lp[:]...)
	out = append(out, wrappedDEK...)
	return out
}

// parseBlob splits a blob into (version, header, keyID, wrappedDEK,
// body) where body is nonce || ciphertext+tag. Both v2 and v3 layouts
// parse; the returned version selects the AAD derivation. Returns
// ErrMalformed for layout errors and ErrUnsupportedVer for unknown
// versions.
func parseBlob(blob []byte) (version uint8, header []byte, keyID string, wrappedDEK, body []byte, err error) {
	if len(blob) < 3+1+1 {
		return 0, nil, "", nil, nil, ErrTruncated
	}
	if blob[0] != blobMagic[0] || blob[1] != blobMagic[1] || blob[2] != blobMagic[2] {
		return 0, nil, "", nil, nil, ErrMalformed
	}
	v := blob[3]
	switch v {
	case blobVersionV2:
		return parseBlobV2(blob)
	case blobVersionV3:
		return parseBlobV3(blob)
	default:
		return 0, nil, "", nil, nil, ErrUnsupportedVer
	}
}

func parseBlobV2(blob []byte) (uint8, []byte, string, []byte, []byte, error) {
	kL := int(blob[4])
	off := 5
	if len(blob) < off+kL+2 {
		return 0, nil, "", nil, nil, ErrTruncated
	}
	keyID := string(blob[off : off+kL])
	if err := validateKeyID(keyID); err != nil {
		return 0, nil, "", nil, nil, ErrMalformed
	}
	off += kL
	wL := int(binary.BigEndian.Uint16(blob[off : off+2]))
	off += 2
	if len(blob) < off+wL {
		return 0, nil, "", nil, nil, ErrTruncated
	}
	wrappedDEK := blob[off : off+wL]
	if len(wrappedDEK) == 0 {
		return 0, nil, "", nil, nil, ErrMalformed
	}
	off += wL
	return blobVersionV2, blob[:off], keyID, wrappedDEK, blob[off:], nil
}

func parseBlobV3(blob []byte) (uint8, []byte, string, []byte, []byte, error) {
	off := 4
	if len(blob) < off+2 {
		return 0, nil, "", nil, nil, ErrTruncated
	}
	kL := int(binary.BigEndian.Uint16(blob[off : off+2]))
	off += 2
	if len(blob) < off+kL+2 {
		return 0, nil, "", nil, nil, ErrTruncated
	}
	keyID := string(blob[off : off+kL])
	if err := validateKeyID(keyID); err != nil {
		return 0, nil, "", nil, nil, ErrMalformed
	}
	off += kL
	wL := int(binary.BigEndian.Uint16(blob[off : off+2]))
	off += 2
	if len(blob) < off+wL {
		return 0, nil, "", nil, nil, ErrTruncated
	}
	wrappedDEK := blob[off : off+wL]
	if len(wrappedDEK) == 0 {
		return 0, nil, "", nil, nil, ErrMalformed
	}
	off += wL
	return blobVersionV3, blob[:off], keyID, wrappedDEK, blob[off:], nil
}

func validateKeyID(keyID string) error {
	if keyID == "" {
		return fmt.Errorf("envelope: KEK returned empty keyID")
	}
	if len(keyID) > 0xFFFF {
		return fmt.Errorf("envelope: KEK keyID exceeds 65535 bytes")
	}
	if !utf8.ValidString(keyID) {
		return fmt.Errorf("envelope: KEK keyID must be valid UTF-8")
	}
	for _, r := range keyID {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("envelope: KEK keyID contains control characters")
		}
	}
	return nil
}

// combineAAD derives the body GCM AAD for the given blob version.
//
// v2 (legacy): `callerAAD || aadDomainSepV2` — kept on the read path
// so blobs written before the v3 upgrade decrypt unchanged.
//
// v3 (current): `aadDomainSepV3 || uvarint(len(callerAAD)) || callerAAD`.
// Putting the separator first and length-prefixing the caller AAD
// eliminates the v2 collision in which two callers could craft inputs
// whose concatenated MAC pre-image was identical (e.g. `"abc" + "xyz"`
// versus `"abcxyz" + ""`). The varint length is unbounded so a 4 GiB
// AAD is representable, but the underlying AEAD interface treats AAD
// as a byte slice so any practical caller stays well inside an int.
//
// The wrap header is intentionally not part of this AAD; see
// [Encryptor.Rewrap] for the rationale.
func combineAAD(version uint8, callerAAD []byte) []byte {
	switch version {
	case blobVersionV2:
		out := make([]byte, 0, len(callerAAD)+len(aadDomainSepV2))
		out = append(out, callerAAD...)
		out = append(out, aadDomainSepV2...)
		return out
	default:
		var lp [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(lp[:], uint64(len(callerAAD)))
		out := make([]byte, 0, len(aadDomainSepV3)+n+len(callerAAD))
		out = append(out, aadDomainSepV3...)
		out = append(out, lp[:n]...)
		out = append(out, callerAAD...)
		return out
	}
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

func (e *Encryptor) validate(ctx context.Context) error {
	if e == nil || e.kek == nil {
		return ErrInvalidEncryptor
	}
	if ctx == nil {
		return ErrInvalidContext
	}
	return nil
}
