package secrets

import (
	"context"
	"errors"
	"time"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// ErrSecretNotFound is returned by Loader.Get when the backend
// reports that the requested secret does not exist (404 / NotFound /
// not-present). Distinct from a transport error so callers can switch
// on it for "no such credential" vs "couldn't reach the API".
var ErrSecretNotFound = errors.New("secrets: not found")

// ErrLoaderUnavailable is returned by Loader.Get when the backend
// could not be reached (network error, auth failure, rate limited).
// Mapped by [CachedLoader] to a stale-cache fallback when there is a
// previously cached value within the stale window.
var ErrLoaderUnavailable = errors.New("secrets: loader unavailable")

// Secret is one fetched secret value plus metadata.
type Secret struct {
	// Value is the secret bytes wrapped in [secret.String] for
	// zeroizable memory hygiene. Callers MUST Zero() when done or
	// allow the cache to manage lifetime (zeroize on eviction).
	Value *secret.String
	// Version identifies the version of the secret returned. Backends
	// supply this directly (AWS VersionId, GCP version number, Vault
	// version int as string). May be empty if the backend doesn't
	// expose versions.
	Version string
	// FetchedAt is the wall-clock time the Loader returned this value
	// (best-effort: cache fills it on miss, backends may overwrite).
	FetchedAt time.Time
}

// Loader is the kit's pluggable secret-fetch contract. Implementations
// MUST be safe for concurrent use.
//
// A successful Get returns ([Secret], nil). An unknown key returns
// (zero, [ErrSecretNotFound]). Any other error MUST wrap
// [ErrLoaderUnavailable] (so [CachedLoader] can decide stale-fallback
// without parsing backend-specific errors).
type Loader interface {
	Get(ctx context.Context, key string) (Secret, error)
}
