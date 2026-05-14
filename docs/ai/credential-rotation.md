# Credential Rotation

Use short-lived or provider-managed credentials wherever the upstream SDK can
refresh them. Static `_FILE` secrets are still supported for simple
deployments, but production services should prefer the provider hooks below.

| Surface | Rotation contract |
|---|---|
| PostgreSQL | `pgxbackend.Config.PasswordProvider` is called before every new physical connection. Call `Pool.Reset()` after a password rotation event to drain old authenticated connections. |
| Redis | Pass go-redis `CredentialsProvider`, `CredentialsProviderContext`, or `StreamingCredentialsProvider` through `app/redis.Module` / `infra/redis.Connect`. Streaming providers can re-auth open connections when go-redis receives updates. |
| RabbitMQ / AMQP | Use `amqpbackend.WithURLProvider` or `app/amqp.WithURLProvider`; the provider is called before each initial dial and reconnect. `amqpbackend.WithURLProviderTimeout` bounds the provider context. |
| NATS JetStream | Use `natsbackend.Config.UsernamePasswordProvider` or `TokenProvider`; nats.go calls these during auth and reauth. `.creds` files are delegated to nats.go callbacks; NKey seed signatures are callback-based but the public key is fixed at connection construction, so prefer providers for live rotation. |
| S3 | Prefer `S3Config.UseDefaultCredentials` for IAM role, web identity, ECS/EKS/EC2 metadata, SSO, or process providers. Use `S3Config.CredentialProvider` for explicit rotating AWS SDK providers. Static access keys are mutually exclusive with both. |
| Azure Blob | Prefer `azurebackend.NewWithTokenCredential` with managed identity, workload identity, or chained Azure credentials. `New` with `AccountKey` remains the static-key path. |
| GCS | Leave `CredentialsFile` empty to use Application Default Credentials / workload identity, or pass rotating credential options through `GCSConfig.ClientOptions`. |
| SFTP | Use `SFTPConfig.PasswordProvider`; it is evaluated when a new SSH connection opens and receives a bounded context through `PasswordProviderTimeout` or the package default. Key files are read on each reconnect, so projected-key updates take effect after reconnect. |
| CSRF | `httpx/middleware/csrf.WithSecrets(current, previous...)` and `security/csrf.NewIssuerWithSecrets` sign with the current secret and verify previous secrets during the overlap window. |
| Signed HTTP requests | Receivers already resolve by key ID. Senders can use `httpx/sign.WrapKeyStore` so each request signs with the current key from a reloading key store. |
| JWT | `security/jwtutil.Provider.Run` refreshes JWKS on schedule and on key misses; rotate issuer keys with overlapping JWKS publication. |
| PASETO | **Asymmetric rotation contract.** `crypto/paseto.Provider` hot-refreshes *verification* keys on the consumer side; refreshed JWKS-style key sets take effect immediately. The kit does NOT ship a Provider for *signing* — `V4PublicSigner` is constructed from a single private key at startup. To rotate the signing key, callers must (a) construct a new signer from the rotated private key, (b) atomically swap their issuer's signer reference (typically guarded by a sync.RWMutex), (c) publish the matching new verification key alongside the previous one in the verifier-side key set, and (d) keep both verification keys live until the longest outstanding token TTL expires before retiring the old one. The kit's verifier-side `Provider` already handles step (c) at the consumer; the signer-side cutover is service-managed. |
| Envelope KMS / Vault | Envelopes record KEK IDs / versions. Configure AWS KMS, GCP KMS, Azure Key Vault, and Vault clients with provider-managed credentials; rotate KEKs by writing new records with the new key while old records unwrap by recorded ID. |
| TLS / mTLS material | Static load is the default. For hot rotation pass `app.Builder.WithReloadingTLS(netutil.WithReloadInterval(d))` (or trigger reloads from a SIGHUP handler via `FilesCertificateSource.Reload`). The Builder wires the same `CertificateSource` into the public server, the default outbound HTTP client, and `Infrastructure.TLSCertSource`; broker / gRPC / custom adapters should read `infra.TLSCertSource` and pass it through `netutil.ReloadingServerTLS` or `netutil.ReloadingClientTLS`. `ReloadingClientTLS` fails closed with `netutil.ErrServerNameRequired` if the caller dials without `tls.Config.ServerName`. |

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
