package vaultkv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/vault/api"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/secrets/v2"
)

// API is the minimal Vault surface this backend uses. A configured
// [*api.Client.KVv2(...)] satisfies it; tests provide a stub.
type API interface {
	Get(ctx context.Context, path string) (*api.KVSecret, error)
}

// Loader implements [secrets.Loader] backed by Vault KV v2.
//
// Each key passed to [Loader.Get] becomes a Vault KV path under the
// configured mount. The default secret field is "value"; override with
// [WithField] to read a different field of the JSON-shaped KV entry.
type Loader struct {
	api   API
	field string
}

// Option configures a [Loader].
type Option func(*Loader)

// WithField overrides the JSON field read from each KV entry (default
// "value"). Vault KV stores arbitrary JSON; the secret value comes from
// one named field rather than the whole JSON blob.
func WithField(name string) Option {
	if name == "" {
		panic("vaultkv: WithField requires non-empty field name")
	}
	return func(l *Loader) { l.field = name }
}

// New wraps the supplied API (typically the result of
// vaultClient.KVv2(mount)). Panics on nil api.
func New(api API, opts ...Option) *Loader {
	if api == nil {
		panic("vaultkv: New requires non-nil API")
	}
	l := &Loader{api: api, field: "value"}
	for _, opt := range opts {
		if opt == nil {
			panic("vaultkv: option must not be nil")
		}
		opt(l)
	}
	return l
}

// Get resolves the secret. Returns:
//   - (Secret, nil)                          on success
//   - (zero, secrets.ErrSecretNotFound)      when Vault reports 404
//   - (zero, wrapped ErrLoaderUnavailable)   on transport / auth errors
func (l *Loader) Get(ctx context.Context, key string) (secrets.Secret, error) {
	resp, err := l.api.Get(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return secrets.Secret{}, secrets.ErrSecretNotFound
		}
		return secrets.Secret{}, redact.WrapSentinel(secrets.ErrLoaderUnavailable,
			redact.WrapError("vaultkv: Get "+redact.StringValue(key), err))
	}
	if resp == nil || resp.Data == nil {
		return secrets.Secret{}, secrets.ErrSecretNotFound
	}
	raw, ok := resp.Data[l.field]
	if !ok {
		return secrets.Secret{}, fmt.Errorf("vaultkv: secret has no %q field: %w", l.field, secrets.ErrLoaderUnavailable)
	}
	strVal, ok := raw.(string)
	if !ok {
		return secrets.Secret{}, fmt.Errorf("vaultkv: field %q is %T, not string: %w", l.field, raw, secrets.ErrLoaderUnavailable)
	}
	version := ""
	if resp.VersionMetadata != nil {
		version = strconv.Itoa(resp.VersionMetadata.Version)
	}
	return secrets.MakeSecret([]byte(strVal), version), nil
}

// isNotFound recognises Vault shapes when a path doesn't exist or has
// been deleted. Prefer the structured *api.ResponseError status code;
// the string fallback is limited to Vault-specific wording so a
// transport/proxy error containing a generic "Code: 404" is not
// misclassified as not-found (which would skip CachedLoader stale
// fallback).
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var rerr *api.ResponseError
	if errors.As(err, &rerr) && rerr.StatusCode == 404 {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "secret not found") ||
		strings.Contains(msg, "metadata not found")
}
