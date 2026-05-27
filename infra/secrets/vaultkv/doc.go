// Package vaultkv is the HashiCorp Vault KV v2 backend for the kit's
// [infra/secrets.Loader] contract. Construct a *vault.Client out of
// band (with whatever auth method your deployment uses — AppRole,
// Kubernetes auth, token) and hand it to [New].
//
// # Use this package when
//
//   - Your service uses HashiCorp Vault for secret distribution.
//   - You want the kit's [infra/secrets.CachedLoader] in front of
//     Vault so per-connection password rotations don't hammer the API.
//
// # Do NOT use this package for
//
//   - KEK wrapping. Use [crypto/envelope/vaulttransit] for Vault
//     Transit-backed envelope encryption.
//   - KV v1 mounts. This backend targets KV v2 (versioned) only.
package vaultkv
