package encryption

import (
	"context"
	"errors"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// ErrNoKey is returned by Put, Get, Copy and GetVersion of a store built
// by [WithoutKey]: it holds no key to seal or open an object with.
var ErrNoKey = errors.New("encryption: store has no key")

// WithoutKey wraps backend like [New] but without a key, for a process
// that only deletes and lists the objects of an encrypted store (a purge,
// a retention sweep) and so need not hold a key that reads every object.
// Delete, Exists, List (with plaintext sizes), the exact-version metadata
// calls and DeleteVersion work as through [New]; Put, Get, Copy and
// GetVersion fail with [ErrNoKey] before the backend is called.
//
// Panics if backend is nil, as [New] does.
func WithoutKey(backend storage.Storage) storage.Storage {
	return New(backend, noKey{})
}

// noKey is the [KeyProvider] of a [WithoutKey] store.
type noKey struct{}

func (noKey) EncryptionKey(context.Context) ([]byte, error) { return nil, ErrNoKey }
