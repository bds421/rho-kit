package apikey

import (
	"context"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// Repository persists issued keys and looks them up by their public id. The
// secret is never stored or returned — only the [Key] record (with its
// hash) is. Implementations must look up by the indexed id, never by hash.
type Repository interface {
	// Insert stores a newly issued key. It returns a conflict error if a
	// key with the same ID already exists.
	Insert(ctx context.Context, key Key) error
	// FindByID returns the key with the given public id, or a not-found
	// error ([apperror] NotFound) when none exists.
	FindByID(ctx context.Context, id string) (Key, error)
	// Revoke marks the key revoked at the given time. It is idempotent and
	// returns a not-found error when the key does not exist.
	Revoke(ctx context.Context, id string, at time.Time) error
	// ListByOwner returns all keys belonging to owner, in unspecified order.
	ListByOwner(ctx context.Context, owner string) ([]Key, error)
}

// MemoryRepository is an in-memory [Repository] for tests and small
// single-instance deployments. It is safe for concurrent use and returns
// copies so callers cannot mutate stored records.
type MemoryRepository struct {
	mu   sync.RWMutex
	keys map[string]Key
}

// NewMemoryRepository returns an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{keys: make(map[string]Key)}
}

// Insert implements [Repository].
func (r *MemoryRepository) Insert(_ context.Context, key Key) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.keys[key.ID]; exists {
		return apperror.NewConflict("apikey: key already exists")
	}
	r.keys[key.ID] = cloneKey(key)
	return nil
}

// FindByID implements [Repository].
func (r *MemoryRepository) FindByID(_ context.Context, id string) (Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key, ok := r.keys[id]
	if !ok {
		return Key{}, apperror.NewNotFound("api key", id)
	}
	return cloneKey(key), nil
}

// Revoke implements [Repository].
func (r *MemoryRepository) Revoke(_ context.Context, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[id]
	if !ok {
		return apperror.NewNotFound("api key", id)
	}
	if key.RevokedAt.IsZero() {
		revoked := cloneKey(key)
		revoked.RevokedAt = at
		r.keys[id] = revoked
	}
	return nil
}

// ListByOwner implements [Repository].
func (r *MemoryRepository) ListByOwner(_ context.Context, owner string) ([]Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Key
	for _, key := range r.keys {
		if key.Owner == owner {
			out = append(out, cloneKey(key))
		}
	}
	return out, nil
}

func cloneKey(k Key) Key {
	k.Scopes = cloneScopes(k.Scopes)
	return k
}
