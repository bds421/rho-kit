# Credential Rotation

Use short-lived or provider-managed credentials wherever the upstream SDK can
refresh them. Static `_FILE` secrets are still supported for simple
deployments, but production services should prefer the provider hooks below.

The matrix below is a **capability matrix**, not a feature parity claim:
backends differ in which rotation styles they support and in how cutovers
actually propagate to open connections. Read every cell that applies to
your wiring before assuming the kit will rotate a credential for you.

Columns:

- **Static / file** — credential is read once at construction (or from a
  `*_FILE` env path) and never re-read until the process restarts.
- **Provider callback** — a `*Provider` / `Provider*` hook the kit invokes
  on new dials, new physical connections, or reauth handshakes.
- **Hot reload on open connection** — whether already-established
  connections pick up the new credential without a reconnect.

| Surface | Static / file | Provider callback | Hot reload on open connection | Notes |
|---|---|---|---|---|
| PostgreSQL | yes (Config.Password / Config.PasswordFile) | `pgxbackend.Config.PasswordProvider` called before every new physical connection | no — call `Pool.Reset()` to drain old authenticated connections | Provider context inherits the dial context's timeout. |
| Redis | yes (Config.Password / Config.PasswordFile) | go-redis `CredentialsProvider` / `CredentialsProviderContext` / `StreamingCredentialsProvider` via `app/redis.Module` / `infra/redis.Connect` | streaming providers only — go-redis re-auths open connections when the streaming provider emits updates; non-streaming providers only fire on new connections | Choose the streaming variant for true hot-rotation. |
| RabbitMQ / AMQP | yes (Config.URL / `_FILE`) | `amqpbackend.WithURLProvider` / `app/amqp.WithURLProvider`; bounded by `WithURLProviderTimeout` | no — the provider is consulted on initial dial and on reconnect, never against an open Connection | Trigger a reconnect to apply rotated credentials. |
| NATS JetStream | yes (Config.Username/Password, NKey seed, .creds file) | `natsbackend.Config.UsernamePasswordProvider` / `TokenProvider` invoked by nats.go on auth + reauth | yes for token / username-password reauth; NKey public key is fixed at construction so prefer the provider hooks for live rotation | `.creds` files are delegated to nats.go callbacks (file path is re-read by nats.go). |
| S3 | yes (static access keys, mutually exclusive with the other two columns) | `S3Config.CredentialProvider` — rotating AWS SDK provider | yes — the AWS SDK re-resolves credentials per request via the provider chain | `S3Config.UseDefaultCredentials` is recommended (IAM role, web-identity, ECS/EKS/EC2 metadata, SSO, process). |
| Azure Blob | yes (`New` with AccountKey) | `azurebackend.NewWithTokenCredential` with managed identity / workload identity / chained Azure credentials | yes — the Azure SDK token credential refreshes against AAD | Static-key path is intentionally simple and does not rotate. |
| GCS | yes (`CredentialsFile`) | `GCSConfig.ClientOptions` accepts rotating credential options; otherwise ADC / workload identity | yes when using ADC / workload identity — the Google SDK refreshes tokens against the metadata server | Leave `CredentialsFile` empty to use ADC. |
| SFTP | yes (Config.Password / Config.PasswordFile, Config.PrivateKey / `_FILE`) | `SFTPConfig.PasswordProvider`; bounded by `PasswordProviderTimeout` (or package default) | no — SSH does not support credential rotation on an open connection; provider only runs on new SSH session establishment | Projected-key updates take effect only after reconnect. |
| CSRF | yes (single secret) | `httpx/middleware/csrf.WithSecrets(current, previous...)` and `security/csrf.NewIssuerWithSecrets` sign with current, verify previous during the overlap window | n/a — secrets are evaluated per request | Overlap window keeps pre-rotation cookies/tokens valid. |
| Signed HTTP requests | yes (single key at construction) | `httpx/sign.WrapKeyStore` so each request signs with the current key from a reloading key store | yes — receivers resolve by key ID per request | Pre-publish new key IDs before rotating senders. |
| JWT | yes (static signer key) | `security/jwtutil.Provider.Run` refreshes JWKS on schedule and on key misses | yes (verification side) — rotated keys take effect on the next JWKS refresh | Rotate issuer keys with overlapping JWKS publication; signer cutover is service-managed. |
| PASETO | yes (static V4PublicSigner / V4PublicVerifier) | `crypto/paseto.OpenProvider` (verification) and `crypto/paseto.OpenSigningProvider` (signing) both refresh keys on a schedule via callbacks | yes — atomic swap on both sides per refresh | The issuer wires `OpenSigningProvider(ctx, PrivateKeySource, interval)`; the consumer wires `OpenProvider(ctx, PublicKeySource, interval)`. Pre-publish the new verification key alongside the previous one and keep both live until the longest outstanding token TTL expires — the signing-side Provider's `WithSigningMaxStale` fails closed if the key source has been silently stalled longer than the configured staleness window. |
| Envelope KMS / Vault | yes (key handle / KEK identifier in config) | provider-managed credentials in AWS KMS / GCP KMS / Azure Key Vault / Vault clients; envelopes record KEK IDs / versions | yes — old records unwrap by recorded KEK ID while new records use the rotated KEK | KEK rotation = write new records under the new key; do not in-place re-encrypt unless you have a migration plan. |
| TLS / mTLS material | yes — static load is the default | `app.Builder.WithReloadingTLS(netutil.WithReloadInterval(d))` (or SIGHUP-driven `FilesCertificateSource.Reload`) | yes — `ReloadingServerTLS` / `ReloadingClientTLS` consult the `CertificateSource` per new connection / handshake | The Builder wires the same `CertificateSource` into the public server, the default outbound HTTP client, and `Infrastructure.TLSCertSource`. Broker / gRPC / custom adapters should read `infra.TLSCertSource` and pass it through `netutil.ReloadingServerTLS` / `netutil.ReloadingClientTLS`. `ReloadingClientTLS` fails closed with `netutil.ErrServerNameRequired` if the caller dials without `tls.Config.ServerName`. |

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
