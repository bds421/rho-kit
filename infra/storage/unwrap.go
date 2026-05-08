package storage

// maxUnwrapDepth prevents infinite loops from buggy Unwrap implementations
// that return themselves or create cycles.
const maxUnwrapDepth = 32

// Unwrapper is implemented by storage decorators that wrap another Storage.
// This enables walking the wrapper chain to discover optional interfaces
// (Lister, Copier, PresignedStore) on the underlying backend.
type Unwrapper interface {
	Unwrap() Storage
}

// OpaqueDecorator is implemented by semantic decorators (encryption, retry,
// circuit breaker) that must NOT be bypassed by capability discovery via
// [AsLister]/[AsCopier]/[AsPresigned]/[AsPublicURLer].
//
// Why this exists: a decorator like encryption sits between callers and the
// backend to enforce a semantic contract (data at rest is ciphertext). If the
// As* helpers walked past the decorator to find the raw backend's presigner,
// callers would receive a presigned URL that uploads plaintext directly to
// the bucket — silently bypassing encryption. Same hazard for retry/circuit
// breaker: an open circuit must block optional ops, not just the four core
// ones.
//
// Contract: when As* encounters a node that implements OpaqueDecorator and
// does NOT itself implement the requested optional capability, As* returns
// (nil, false) WITHOUT unwrapping further. Decorators that genuinely
// preserve a capability's semantics opt in by implementing that capability
// directly on the decorator type.
//
// Decorators implement [OpaqueStorageDecorator] with a no-op body. The
// method exists only so the type system can identify opaque wrappers.
type OpaqueDecorator interface {
	OpaqueStorageDecorator()
}

// asImpl is the shared traversal helper. It walks the Unwrap chain looking
// for a node that implements C. If it encounters an OpaqueDecorator that
// does not itself implement C, traversal stops and (zero, false) is
// returned — the decorator's semantics must not be bypassed.
func asImpl[C any](s Storage) (C, bool) {
	var zero C
	for range maxUnwrapDepth {
		if c, ok := s.(C); ok {
			return c, true
		}
		if _, opaque := s.(OpaqueDecorator); opaque {
			return zero, false
		}
		u, ok := s.(Unwrapper)
		if !ok {
			return zero, false
		}
		s = u.Unwrap()
	}
	return zero, false
}

// AsLister walks the Unwrap chain to find a Lister implementation.
// Returns (nil, false) if no backend in the chain implements Lister, or if
// traversal hits an [OpaqueDecorator] that does not itself implement Lister.
func AsLister(s Storage) (Lister, bool) {
	return asImpl[Lister](s)
}

// AsCopier walks the Unwrap chain to find a Copier implementation.
// Returns (nil, false) if no backend in the chain implements Copier, or if
// traversal hits an [OpaqueDecorator] that does not itself implement Copier.
func AsCopier(s Storage) (Copier, bool) {
	return asImpl[Copier](s)
}

// AsPresigned walks the Unwrap chain to find a PresignedStore implementation.
// Returns (nil, false) if no backend in the chain implements PresignedStore,
// or if traversal hits an [OpaqueDecorator] that does not itself implement
// PresignedStore.
func AsPresigned(s Storage) (PresignedStore, bool) {
	return asImpl[PresignedStore](s)
}

// AsPublicURLer walks the Unwrap chain to find a PublicURLer implementation.
// Returns (nil, false) if no backend in the chain implements PublicURLer, or
// if traversal hits an [OpaqueDecorator] that does not itself implement
// PublicURLer.
func AsPublicURLer(s Storage) (PublicURLer, bool) {
	return asImpl[PublicURLer](s)
}
