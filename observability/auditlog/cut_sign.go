package auditlog

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CutSigner signs a cut for the role that applies it ([Cut.Encode]).
type CutSigner interface {
	Sign(message []byte) ([]byte, error)
}

// CutVerifier checks a cut's signature ([DecodeCut]).
type CutVerifier interface {
	Verify(message, signature []byte) bool
}

// maxCutSkew is how far in the future a cut's issue time may lie, for
// clocks of the two roles that differ a little.
const maxCutSkew = time.Minute

const cutDomain = "auditlog retention cut v1\x00"

type ed25519Signer ed25519.PrivateKey

func (k ed25519Signer) Sign(message []byte) ([]byte, error) {
	return ed25519.Sign(ed25519.PrivateKey(k), message), nil
}

type ed25519Verifier ed25519.PublicKey

func (k ed25519Verifier) Verify(message, signature []byte) bool {
	return ed25519.Verify(ed25519.PublicKey(k), message, signature)
}

// NewEd25519CutSigner signs cuts with key.
func NewEd25519CutSigner(key ed25519.PrivateKey) (CutSigner, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("auditlog: the cut signing key is not an Ed25519 private key")
	}
	return ed25519Signer(bytes.Clone(key)), nil
}

// NewEd25519CutVerifier checks cuts signed by the private half of key.
func NewEd25519CutVerifier(key ed25519.PublicKey) (CutVerifier, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, errors.New("auditlog: the cut verifying key is not an Ed25519 public key")
	}
	return ed25519Verifier(bytes.Clone(key)), nil
}

type encodedCut struct {
	Events    int64     `json:"events"`
	Watermark []byte    `json:"watermark,omitempty"`
	Digest    []byte    `json:"digest,omitempty"`
	Before    time.Time `json:"before"`
	IssuedAt  time.Time `json:"issuedAt"`
	Signature []byte    `json:"signature"`
}

// Encode signs the cut and writes it as one word of URL-safe base64, for
// the command line of the role that applies it.
func (c Cut) Encode(signer CutSigner) (string, error) {
	if signer == nil {
		return "", errors.New("auditlog: Encode needs a signer")
	}
	if err := c.wellFormed(); err != nil {
		return "", err
	}
	signature, err := signer.Sign(c.signed())
	if err != nil {
		return "", fmt.Errorf("auditlog: sign the cut: %w", err)
	}
	raw, err := json.Marshal(encodedCut{c.Events, c.Watermark, c.Digest, c.Before, c.IssuedAt, signature})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCut reads a cut [Cut.Encode] wrote and returns it only when its
// signature holds under verifier and it was issued within lifetime before
// now; otherwise [ErrCutRefused]. The chain grows while a cut waits, but
// the events it names stay the oldest until a sweep removes them, so a
// lifetime of an hour or so is plenty.
func DecodeCut(text string, verifier CutVerifier, now time.Time, lifetime time.Duration) (Cut, error) {
	if verifier == nil || lifetime <= 0 {
		return Cut{}, errors.New("auditlog: DecodeCut needs a verifier and a positive lifetime")
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return Cut{}, ErrCutRefused
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e encodedCut
	if dec.Decode(&e) != nil || dec.More() {
		return Cut{}, ErrCutRefused
	}
	c := Cut{Events: e.Events, Watermark: e.Watermark, Digest: e.Digest, Before: e.Before.UTC(), IssuedAt: e.IssuedAt.UTC()}
	if c.wellFormed() != nil || !verifier.Verify(c.signed(), e.Signature) ||
		now.Sub(c.IssuedAt) > lifetime || c.IssuedAt.After(now.Add(maxCutSkew)) {
		return Cut{}, ErrCutRefused
	}
	return c, nil
}

// wellFormed refuses a cut no [ComputeCut] makes: times finer than the
// microsecond the signature covers, a negative count, or a watermark and
// digest that do not fit the count.
func (c Cut) wellFormed() error {
	whole := func(t time.Time) bool { return t.Equal(t.Truncate(time.Microsecond)) }
	named := len(c.Watermark) > 0 && len(c.Digest) == digestSize
	switch {
	case c.Events < 0, !whole(c.Before), !whole(c.IssuedAt),
		c.Events > 0 && !named, c.Events == 0 && (len(c.Watermark) > 0 || len(c.Digest) > 0):
		return ErrCutRefused
	}
	return nil
}

const digestSize = sha256.Size

// signed is what a cut's signature covers.
func (c Cut) signed() []byte {
	b := []byte(cutDomain)
	b = binary.BigEndian.AppendUint64(b, uint64(c.Events))
	b = binary.BigEndian.AppendUint64(b, uint64(c.Before.UnixMicro()))
	b = binary.BigEndian.AppendUint64(b, uint64(c.IssuedAt.UnixMicro()))
	b = binary.BigEndian.AppendUint32(b, uint32(len(c.Watermark)))
	b = append(b, c.Watermark...)
	return append(b, c.Digest...)
}
