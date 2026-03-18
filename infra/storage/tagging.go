package storage

import "context"

// Tags is a set of key-value pairs associated with a stored object.
// Unlike Custom metadata (set once at Put time), tags can be modified
// after the object is created without re-uploading the content.
//
// Backends may impose limits on the number and size of tags. For
// example, S3 allows up to 10 tags with keys up to 128 chars and
// values up to 256 chars.
type Tags map[string]string

// Tagger is an optional extension for backends that support
// mutable object tags (e.g. S3 tagging, GCS labels).
// Check capability via type assertion:
//
//	if tagger, ok := backend.(storage.Tagger); ok {
//	    err := tagger.SetTags(ctx, "doc.pdf", storage.Tags{"env": "prod"})
//	}
type Tagger interface {
	// GetTags retrieves the tags for an object.
	// Returns an empty map (not nil) if the object has no tags.
	// Returns ErrObjectNotFound if the key does not exist.
	GetTags(ctx context.Context, key string) (Tags, error)

	// SetTags replaces all tags on an object with the provided set.
	// Pass an empty map to remove all tags.
	// Returns ErrObjectNotFound if the key does not exist.
	SetTags(ctx context.Context, key string, tags Tags) error

	// DeleteTags removes all tags from an object.
	// Returns nil if the key has no tags or does not exist (idempotent).
	DeleteTags(ctx context.Context, key string) error
}
