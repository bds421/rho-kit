package envelope

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Box seals values for one purpose, each bound to what it belongs to. The
// caller AAD is the purpose and the binding joined by "/", for example
// "billing/card/v1/<tenant>/<card id>": a sealed value copied to another
// record, tenant or purpose fails to open.
type Box struct {
	enc     *Encryptor
	purpose string
}

// NewBox seals under kek for purpose, a stable versioned name such as
// "billing/card/v1"; it panics on an empty purpose.
func NewBox(kek KEK, purpose string) *Box {
	if purpose == "" {
		panic("envelope: NewBox requires a purpose")
	}
	return &Box{enc: NewEncryptor(kek), purpose: purpose}
}

// Seal encrypts plain bound to the binding.
func (b *Box) Seal(ctx context.Context, plain []byte, binding ...string) ([]byte, error) {
	return b.enc.Encrypt(ctx, plain, b.aad(binding))
}

// Open decrypts what Seal sealed with the same binding.
func (b *Box) Open(ctx context.Context, sealed []byte, binding ...string) ([]byte, error) {
	return b.enc.Decrypt(ctx, sealed, b.aad(binding))
}

func (b *Box) aad(binding []string) []byte {
	return []byte(strings.Join(append([]string{b.purpose}, binding...), "/"))
}

// ErrNilBox is returned by SealJSON and OpenJSON without a Box.
var ErrNilBox = errors.New("envelope: no box")

// SealJSON seals v as JSON bound to the binding.
func SealJSON[T any](ctx context.Context, b *Box, v T, binding ...string) ([]byte, error) {
	if b == nil {
		return nil, ErrNilBox
	}
	plain, err := json.Marshal(v) //nolint:gosec // marshalled only to be encrypted
	if err != nil {
		return nil, err
	}
	defer zeroBytes(plain)
	return b.Seal(ctx, plain, binding...)
}

// OpenJSON opens a value SealJSON sealed with the same binding.
func OpenJSON[T any](ctx context.Context, b *Box, sealed []byte, binding ...string) (T, error) {
	var v T
	if b == nil {
		return v, ErrNilBox
	}
	plain, err := b.Open(ctx, sealed, binding...)
	if err != nil {
		return v, err
	}
	defer zeroBytes(plain)
	err = json.Unmarshal(plain, &v)
	return v, err
}
