// Package secrets is the kit's pluggable secret-loader umbrella.
// Defines [Loader] (the contract), [CachedLoader] (TTL cache with
// stale-while-revalidate + single-flight), and [NewRotatingProvider]
// (a callback adapter for SDKs that accept rotating credentials).
//
// Backends live in sibling go modules so consumers pay only for what
// they import:
//
//   - [infra/secrets/awssm]    — AWS Secrets Manager
//   - [infra/secrets/gcpsm]    — GCP Secret Manager
//   - [infra/secrets/vaultkv]  — HashiCorp Vault KV v2
//
// # Use this package when
//
//   - You need to load runtime secrets (DB passwords, API tokens,
//     signing keys) from a managed secret-manager rather than from
//     environment variables or _FILE mounts.
//   - You want a single rotation hook the kit's pgx PasswordProvider,
//     go-redis CredentialsProvider, AMQP URL provider, etc. can call
//     ("give me the freshest secret you have") without each adapter
//     re-implementing TTL + single-flight.
//
// # Do NOT use this package for
//
//   - Data-encryption keys (DEKs). Use [crypto/envelope/*] for
//     envelope encryption — DEKs are wrapped by KEKs that THIS package
//     might load, but DEK wrapping is a separate concern.
//   - Compile-time secret embedding. Secrets that live in the binary
//     are a different threat model; this package assumes runtime fetch.
//
// # Sibling packages
//
//   - [crypto/envelope]              — envelope encryption (KEK + DEK)
//   - [security/jwtutil]             — JWKS fetch (similar caching shape
//     but baked into the JWT verifier)
//   - [core/secret]                  — zeroizable secret.String type
//     used by [Secret.Value]
//
// # Quick start
//
//	loader := awssm.New(awsClient)
//	cached, err := secrets.NewCachedLoader(loader,
//	    secrets.WithCacheTTL(10*time.Minute),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// For pgx: WithPasswordProvider(secrets.NewRotatingProvider(cached, "prod/postgres/api"))
//	// For Redis: WithCredentialsProvider similarly
//
//	// Ad-hoc fetch:
//	s, err := cached.Get(ctx, "prod/api/signing-key")
//	defer s.Value.Zero()
package secrets
