package storage

import "context"

// PublicURLer is an optional extension for backends that can generate
// public (non-expiring) URLs for stored objects. This is meaningful for
// public S3 buckets or CDN-fronted storage.
//
// Check capability via type assertion:
//
//	if u, ok := backend.(storage.PublicURLer); ok {
//	    url, err := u.URL(ctx, "avatars/photo.jpg")
//	}
type PublicURLer interface {
	// URL returns a public, non-expiring URL for the given key.
	// The URL is constructed from the backend configuration and does not
	// verify that the object exists.
	URL(ctx context.Context, key string) (string, error)
}
