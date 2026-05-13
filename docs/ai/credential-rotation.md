# Credential Rotation

Use short-lived or provider-managed credentials wherever the upstream SDK can
refresh them. Static `_FILE` secrets are still supported for simple
deployments, but production services should prefer the provider hooks below.

| Surface | Rotation contract |
|---|---|
| PostgreSQL | `pgxbackend.Config.PasswordProvider` is called before every new physical connection. Call `Pool.Reset()` after a password rotation event to drain old authenticated connections. |
| Redis | Pass go-redis `CredentialsProvider`, `CredentialsProviderContext`, or `StreamingCredentialsProvider` through `app.WithRedis` / `infra/redis.Connect`. Streaming providers can re-auth open connections when go-redis receives updates. |
| RabbitMQ / AMQP | Use `amqpbackend.WithURLProvider` or `app.WithRabbitMQURLProvider`; the provider is called before each initial dial and reconnect. `amqpbackend.WithURLProviderTimeout` bounds the provider context. |
| NATS JetStream | Use `natsbackend.Config.UsernamePasswordProvider` or `TokenProvider`; nats.go calls these during auth and reauth. `.creds` files are delegated to nats.go callbacks; NKey seed signatures are callback-based but the public key is fixed at connection construction, so prefer providers for live rotation. |
| S3 | Prefer `S3Config.UseDefaultCredentials` for IAM role, web identity, ECS/EKS/EC2 metadata, SSO, or process providers. Use `S3Config.CredentialProvider` for explicit rotating AWS SDK providers. Static access keys are mutually exclusive with both. |
| Azure Blob | Prefer `azurebackend.NewWithTokenCredential` with managed identity, workload identity, or chained Azure credentials. `New` with `AccountKey` remains the static-key path. |
| GCS | Leave `CredentialsFile` empty to use Application Default Credentials / workload identity, or pass rotating credential options through `GCSConfig.ClientOptions`. |
| SFTP | Use `SFTPConfig.PasswordProvider`; it is evaluated when a new SSH connection opens and receives a bounded context through `PasswordProviderTimeout` or the package default. Key files are read on each reconnect, so projected-key updates take effect after reconnect. |
| CSRF | `httpx/middleware/csrf.WithSecrets(current, previous...)` and `security/csrf.NewIssuerWithSecrets` sign with the current secret and verify previous secrets during the overlap window. |
| Signed HTTP requests | Receivers already resolve by key ID. Senders can use `httpx/sign.WrapKeyStore` so each request signs with the current key from a reloading key store. |
| JWT | `security/jwtutil.Provider.Run` refreshes JWKS on schedule and on key misses; rotate issuer keys with overlapping JWKS publication. |
| PASETO | `crypto/paseto.Provider` is caller-supplied; expose the new signing key while keeping old verification keys until token TTL expiry. |
| Envelope KMS / Vault | Envelopes record KEK IDs / versions. Configure AWS KMS, GCP KMS, Azure Key Vault, and Vault clients with provider-managed credentials; rotate KEKs by writing new records with the new key while old records unwrap by recorded ID. |

Operational sequence for overlapping secrets:

1. Add the new credential as active while keeping the previous credential for
   verification or reconnect overlap.
2. Roll all service replicas.
3. Wait at least the longest token, nonce, cookie, connection, or message retry
   window for that surface.
4. Remove the previous credential and roll again.

Do not rotate by mutating bytes under the same key ID. Use a new bounded,
header-safe ID for HMAC/PASETO/JWT-style keys so audit logs and metrics can
distinguish the cutover.
