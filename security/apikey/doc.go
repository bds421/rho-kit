// Package apikey issues and verifies opaque, prefixed API keys for
// external/customer-facing access.
//
// The design follows the convention used by GitHub, Stripe and OpenAI:
// a key is a high-entropy random secret shown to the caller exactly once
// at issuance; only a fast, deterministic hash of the secret is persisted.
// Because the secret carries 256 bits of entropy from [crypto/rand], a
// plain SHA-256 lookup hash is sufficient — a slow password KDF (argon2,
// bcrypt) would add cost without security and, with a per-row salt, would
// make lookup-by-hash impossible. Use [github.com/bds421/rho-kit/crypto/v2/passhash]
// for low-entropy human passwords, never for keys minted here.
//
// # Token format
//
//		<prefix>_<id>_<secret>      e.g.  rho_018f...c3_3kQ9...rT
//
//	  - prefix: a short, fixed, registrable string (default "rho") so secret
//	    scanners (GitHub secret scanning, GitGuardian) can detect leaked keys.
//	  - id: the public, indexed lookup key ([core/id] UUID v7). It is NOT a
//	    secret; it identifies the row so verification is a single indexed read
//	    followed by one constant-time hash compare — never a table scan.
//	  - secret: 256 bits of [core/randstr] entropy. Hashed with SHA-256; the
//	    plaintext is never stored.
//
// # Layering
//
// This package is transport- and storage-agnostic. It returns sentinel
// errors ([ErrExpired], [ErrRevoked], [ErrInvalidSecret], [ErrMalformedToken])
// rather than HTTP/apperror types so each transport maps them as it sees
// fit. Persistence is abstracted behind [Repository]; an in-memory
// implementation ([NewMemoryRepository]) ships here for tests and small
// deployments, with a Postgres implementation in the data/apikey/postgres
// module.
//
// A stateless alternative exists for services that prefer signed,
// self-describing tokens over an opaque-key lookup table: see the
// app/paseto and crypto/paseto modules. Opaque keys are preferred here
// because they are instantly revocable and match the external-API
// convention customers and AI agents expect.
//
// # Scoped keys
//
// [GenerateScoped] and [ScopedResolver] cover tenant-scoped credentials
// with an optional UUID-shaped [ScopedKey.SubjectUserID] (omit for
// unbound integration keys with tenant-wide machine access; when set it
// must be UUID-shaped). Wire prefixes are configurable (defaults
// [ScopedTokenPrefixAPI] / [ScopedTokenPrefixOAuth]); secrets are hashed
// with [github.com/bds421/rho-kit/crypto/v2/passhash]. Verification
// accepts legacy bcrypt via
// [github.com/bds421/rho-kit/crypto/v2/passhash/bcryptcompat].
package apikey
