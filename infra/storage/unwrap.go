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

// AsLister walks the Unwrap chain to find a Lister implementation.
// Returns (nil, false) if no backend in the chain implements Lister.
func AsLister(s Storage) (Lister, bool) {
	for range maxUnwrapDepth {
		if l, ok := s.(Lister); ok {
			return l, true
		}
		u, ok := s.(Unwrapper)
		if !ok {
			return nil, false
		}
		s = u.Unwrap()
	}
	return nil, false
}

// AsCopier walks the Unwrap chain to find a Copier implementation.
// Returns (nil, false) if no backend in the chain implements Copier.
func AsCopier(s Storage) (Copier, bool) {
	for range maxUnwrapDepth {
		if c, ok := s.(Copier); ok {
			return c, true
		}
		u, ok := s.(Unwrapper)
		if !ok {
			return nil, false
		}
		s = u.Unwrap()
	}
	return nil, false
}

// AsPresigned walks the Unwrap chain to find a PresignedStore implementation.
// Returns (nil, false) if no backend in the chain implements PresignedStore.
func AsPresigned(s Storage) (PresignedStore, bool) {
	for range maxUnwrapDepth {
		if p, ok := s.(PresignedStore); ok {
			return p, true
		}
		u, ok := s.(Unwrapper)
		if !ok {
			return nil, false
		}
		s = u.Unwrap()
	}
	return nil, false
}

// AsPublicURLer walks the Unwrap chain to find a PublicURLer implementation.
// Returns (nil, false) if no backend in the chain implements PublicURLer.
func AsPublicURLer(s Storage) (PublicURLer, bool) {
	for range maxUnwrapDepth {
		if u, ok := s.(PublicURLer); ok {
			return u, true
		}
		w, ok := s.(Unwrapper)
		if !ok {
			return nil, false
		}
		s = w.Unwrap()
	}
	return nil, false
}
