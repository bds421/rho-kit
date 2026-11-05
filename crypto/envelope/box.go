package envelope

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
)

// Box seals values for one purpose, each bound to what it belongs to: a
// sealed value copied to another record, tenant or purpose fails to open.
//
// The caller AAD Box hands the [Encryptor] is
//
//	"\x00envelope.Box/v2\x00" ‖ uvarint(len(purpose)) ‖ purpose ‖
//	uvarint(len(binding[0])) ‖ binding[0] ‖ … ‖ uvarint(len(binding[n-1])) ‖ binding[n-1]
//
// so every element keeps its boundary: Seal(x, "a/b") and Seal(x, "a", "b")
// are bound to different data.
//
// Before v2.37.0 the caller AAD was purpose and binding joined by "/",
// which made those two calls interchangeable. Open still accepts values
// sealed that way, after the current layout fails; the KEK is called once
// either way. A legacy AAD starts with the purpose, which NewBox refuses
// to start with a NUL byte, so no legacy AAD equals a current one: a value
// sealed now never opens under the joined form, and a legacy value never
// opens under the current form for another binding. Reseal legacy values
// (Open, then Seal) to give them the unambiguous binding.
type Box struct {
	enc     *Encryptor
	purpose string
}

const boxAADPrefix = "\x00envelope.Box/v2\x00"

// NewBox seals under kek for purpose, a stable versioned name such as
// "billing/card/v1"; it panics on an empty purpose or one starting with a
// NUL byte.
func NewBox(kek KEK, purpose string) *Box {
	if purpose == "" {
		panic("envelope: NewBox requires a purpose")
	}
	if purpose[0] == 0 {
		panic("envelope: NewBox purpose must not start with a NUL byte")
	}
	return &Box{enc: NewEncryptor(kek), purpose: purpose}
}

// Seal encrypts plain bound to the binding.
func (b *Box) Seal(ctx context.Context, plain []byte, binding ...string) ([]byte, error) {
	return b.enc.Encrypt(ctx, plain, b.aad(binding))
}

// Open decrypts what Seal sealed with the same binding, including values
// sealed before v2.37.0 (see [Box]).
func (b *Box) Open(ctx context.Context, sealed []byte, binding ...string) ([]byte, error) {
	return b.enc.decryptFirst(ctx, sealed, b.aad(binding), b.legacyAAD(binding))
}

func (b *Box) aad(binding []string) []byte {
	size := len(boxAADPrefix) + binary.MaxVarintLen64 + len(b.purpose)
	for _, part := range binding {
		size += binary.MaxVarintLen64 + len(part)
	}
	out := make([]byte, 0, size)
	out = append(out, boxAADPrefix...)
	out = appendLengthPrefixed(out, b.purpose)
	for _, part := range binding {
		out = appendLengthPrefixed(out, part)
	}
	return out
}

func appendLengthPrefixed(out []byte, s string) []byte {
	out = binary.AppendUvarint(out, uint64(len(s)))
	return append(out, s...)
}

func (b *Box) legacyAAD(binding []string) []byte {
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
