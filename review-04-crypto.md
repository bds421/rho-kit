# Code review: Crypto & envelope encryption (stage 1 — unverified findings)

## Scope

- **Directories**: crypto/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 12 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 4 |
| **Total (deduplicated)** | **4** |

**Reviewer impressions:**

> This is unusually high-quality crypto code: modern algorithm choices throughout (AES-256-GCM via Tink, argon2id, Ed25519/XChaCha20 PASETO v4, HMAC-SHA256 with constant-time compare), consistent AAD/domain-separation discipline, fail-closed defaults, DoS bounds on attacker-controlled parameters, careful key-zeroization, and extensive security-rationale comments that mostly match the implementation. The KMS adapters correctly treat envelope headers as attacker-controlled and pin decrypt targets to configuration. The surviving findings are edge-hardening items — one real data race in V4Local.Close, a few doc/implementation mismatches, and defense-in-depth gaps — rather than exploitable flaws.

> This is unusually careful crypto code: envelope format versioning with explicit AAD domain separation, TOCTOU-aware KEK Wrap contracts, attacker-controlled keyID validation in every cloud adapter, DoS bounds on argon2 parameters, and constant-time signature comparison with a fixed-length fallback path are all handled thoughtfully, with comments that record the audit findings that motivated each hardening. The defects that remain are concentrated at the edges rather than in the core algorithms: V4Local missed the mutex treatment its sibling V4PublicSigner received, the key-refresh providers do not sanity-check interval against maxStale, and a few adapters have contract inconsistencies (bcryptcompat's empty-password gap, StaticKeyStore's nil-error close semantics, unused error-classification parameters). Nothing found undermines confidentiality or integrity of stored ciphertext.

> This is unusually high-quality crypto code: versioned self-describing formats with documented AAD derivations, misuse-resistant KEK contracts (Wrap-returns-keyID to kill TOCTOU rotation races), fail-closed parsing, DoS-bounded argon2 parameters, and consistent key-zeroization/Close semantics, all backed by dense and honest godoc. The findings are almost entirely at the edges — a shutdown-time data race in paseto.V4Local that its sibling type already solved, a zero-value escape hatch in secretcrypt, and several documentation/consistency drifts between the four KMS adapter siblings — rather than flaws in the core algorithm or format design.

> This is unusually high-quality security code: every package shows deliberate misuse-resistance (KEK keyID pinning on Unwrap, AEAD-verified idempotent re-encrypt, versioned AAD domain separation with a documented Rewrap rationale, DoS-bounded argon2 params, constant-time comparison with format-error timing equalization), and the godoc explains security decisions with wave/audit references. The findings are correspondingly modest: one real data race (V4Local.Close vs Verify/Seal, solved correctly in the sibling V4PublicSigner but missed here), one construction-invariant gap (zero-value secretcrypt.Crypter encrypts with no secret), a testability gap in gcpkms from using the concrete client type, and a handful of doc inaccuracies and cross-adapter inconsistencies (metrics only in awskms). No algorithm, nonce, or key-management flaws were found.

> This is an unusually well-hardened crypto scope: strong algorithm choices throughout (AES-256-GCM via Tink, argon2id, Ed25519 PASETO v4, HMAC-SHA256 with constant-time comparison and fallback-MAC timing equalization), careful AAD domain separation with a documented v2->v3 collision fix, KMS adapters that pin key IDs to defeat decrypt-redirect, DoS bounds on attacker-controlled cost parameters, and consistent secret-redaction in LogValue implementations. The findings that survive scrutiny are edge cases and consistency gaps — a missing bcrypt cost cap that contradicts the package's own argon2 threat model, a V4Local key race that its sibling signer type already solved, and a few key-zeroization gaps — rather than exploitable core flaws. Comments extensively document prior audit waves (FR-044/046/047, wave 66/184), and that review discipline shows in the code quality.

> This scope is unusually high quality: it has clearly been through multiple adversarial audit waves (FR-044/046/047 annotations), and the hard problems — TOCTOU-safe Wrap keyID contracts, AAD domain separation with versioned blob formats, constant-time comparison with length normalization, key zeroization under proper locking, bounded argon2 params, CRC32C transit integrity — are handled correctly and documented with rationale. The surviving findings are edge concerns: one real data race in V4Local's Close path (the one type that missed the keyMu pattern its sibling uses), a documented-but-latent availability trap in the AWS KMS alias decrypt path, and two minor startup/shutdown contract gaps.

> This is unusually careful crypto code: AEAD framing is versioned with explicit AAD domain separation and length-prefixing, nonce handling delegates to Tink/stdlib GCM with documented 2^32 random-IV ceilings, key IDs are validated before use as AAD or header material, comparisons use hmac.Equal/subtle with deliberate constant-shape fallback paths, and every KMS adapter pins/validates the envelope keyID before issuing a decrypt. Concurrency is mostly handled well (atomic snapshots in the providers, RLock-scoped key access in kekstatic, keyMu in V4PublicSigner), which makes the one genuine gap — V4Local's unlocked Close vs Seal/Verify — stand out as an oversight rather than a pattern. The remaining findings are edge-case contract violations and consistency gaps (zero-value Crypter, GCP versioned-resource config, keystore nil-error returns), not systemic design flaws.

> This is exceptionally well-hardened cryptographic code — clearly the product of multiple adversarial review waves. Algorithm choices are sound throughout (AES-256-GCM via Tink, argon2id with DoS-bounded parameters, Ed25519/XChaCha20 PASETO v4, HKDF-SHA256, RSA1_5/RSA-OAEP-SHA1 rejected), AAD discipline is rigorous (length-prefixed v3 envelope AAD with domain separation, keyID bound as AAD in kekstatic, per-KEK keyID pinning on every Unwrap path), comparisons of secrets/MACs are constant-time with deliberate fallback-buffer handling, and everything fails closed with uniform auth errors and slog redaction of key identifiers. The only substantive defect found is a data race between V4Local.Close and concurrent Seal/Verify (the asymmetric signer got a lock for exactly this; the symmetric sealer did not); the remaining findings are contract/documentation polish.

> This is an unusually high-quality crypto scope: fail-closed validation everywhere, versioned self-describing formats with careful AAD domain separation and backward compatibility, keyID pinning in every KMS adapter to stop attacker-controlled envelope headers from redirecting decrypts, constant-time comparisons, DoS caps on argon2 params, and pervasive key zeroization with honest doc comments about upstream-library limits. The findings are correspondingly modest — the only behavioral defect is a documented-safe-but-actually-racy V4Local.Close, and the rest are doc/code contradictions on security properties, cross-adapter parity gaps (metrics, error-classifier tests), and secretcrypt lagging the rest of the tree on key-hygiene and per-op derivation cost.

> This is unusually high-quality, security-conscious code: the envelope format is versioned with length-prefixed AAD domain separation, KEK adapters pin decrypt targets and reject attacker-controlled key-ID redirection, GCP adds CRC32C transit integrity, and passhash/paseto/signing show careful constant-time comparison, DoS bounding, and key-zeroization. The findings are mostly documentation/consistency polish rather than exploitable flaws; the one substantive issue is V4Local.Close lacking the mutex its sibling V4PublicSigner uses, leaving its advertised concurrency safety unbacked. No CRITICAL/HIGH cryptographic defects were found in the reviewed source.

> This is unusually high-quality, heavily-reviewed cryptographic code: AEAD everywhere via Tink/stdlib with random IVs (no nonce reuse or math/rand), crypto/rand for all key/salt/nonce generation, constant-time MAC/hash comparisons, argon2id with DoS-bounded parameters, and notably strong authorization pinning on every KMS adapter (gcpkms/vaulttransit never forward the attacker-controlled envelope keyID to the provider; awskms validates ARN scope+region; kekstatic binds keyID as GCM AAD). The main gaps are a data race on V4Local's key during Close (which its sibling V4PublicSigner already guards) and a missing bcrypt work-factor cap that the passhash package treats as a real threat elsewhere. No exploitable authz-bypass or key-redirection was found in the envelope backends.

> This is unusually careful, security-conscious code: envelope format versioning, AAD domain separation, TOCTOU-safe Wrap/KeyID contracts, constant-time comparisons, fail-closed decryption, and thorough keyID validation across all four KMS adapters. The provider rotation goroutines (paseto) handle context cancellation, shutdown races, and stale-key windows well. The one real concurrency defect is V4Local, which was not given the keyMu guard its sibling V4PublicSigner has; the remaining findings are hardening/hygiene gaps rather than exploitable flaws.

## Findings

### [LOW] KEK adapter constructors and observability diverge: only awskms has Options and Prometheus metrics

- **Where**: `crypto/envelope/gcpkms/gcpkms.go:94`
- **Dimension**: api-design
- **Detail**: awskms.NewKEK takes `opts ...Option` and ships a Metrics type recording every KMS error to a request_errors_total counter (awskms/metrics.go), while gcpkms.NewKEK (line 94), azurekeyvault.NewKEK, and vaulttransit.NewKEK take no options and record no metrics at all, despite their errors.go files claiming behavioral parity with awskms. Failure scenario: an operator who standardizes dashboards/alerts on awskms_request_errors_total migrates a service to GCP KMS or Key Vault and silently loses all KMS-error observability; separately, the constructor signature difference means adding options to the other adapters later is a breaking-ish churn for wrapper code that abstracts over the four adapters.
- **Suggestion**: Add the same variadic Option + Metrics pattern (or a shared envelope-level metrics hook) to gcpkms, azurekeyvault, and vaulttransit so the four adapters have uniform constructor shapes and error observability.

### [LOW] buildToken calls time.Now() directly for WithDefaultLifetime, bypassing the package's clock-injection convention

- **Where**: `crypto/paseto/paseto.go:485`
- **Dimension**: api-design
- **Detail**: When Claims.ExpiresAt and Claims.IssuedAt are both zero and WithDefaultLifetime is configured, buildToken derives exp from time.Now() (line 485). Everywhere else the package injects time: verification takes an explicit `now` parameter, and Provider/SigningProvider have clock fields with with*Clock options for tests. Sign-side default-lifetime behavior is therefore untestable deterministically — tests must set IssuedAt explicitly or tolerate wall-clock slop — and the inconsistency invites a future clock-related bug when someone assumes the whole package is clock-injectable.
- **Suggestion**: Thread a clock func through config (defaulting to time.Now) and use it in buildToken, mirroring the signing package's WithClock pattern.

### [LOW] Provider and SigningProvider duplicate ~200 lines of refresh/lifecycle machinery

- **Where**: `crypto/paseto/signing_provider.go:49`
- **Dimension**: smell
- **Detail**: SigningProvider (signing_provider.go) is a near-verbatim copy of Provider (provider.go): identical field sets (stop/done/stopOnce/rootCtx/rootCancel/fetchTimeout/maxStale/clock), identical loop() including the FR-046 comment, identical callOnRefreshError with panic-recover, parallel option sets (WithFetchTimeout/WithSigningFetchTimeout, WithMaxStale/WithSigningMaxStale, etc.), and structurally identical Verify/Sign staleness gates. A future fix to the refresh/shutdown machinery (e.g. the closed-race suppression logic in loop) must be applied twice and can silently drift — the two files already differ only in the payload type and the extra signer Close in SigningProvider.Close.
- **Suggestion**: Extract a shared unexported refresher[T any] (generic over the atomic.Pointer payload) owning loop/Close/staleness bookkeeping, with Provider and SigningProvider as thin wrappers.

### [LOW] Every Encrypt/Decrypt re-runs HKDF and rebuilds a Tink AEAD even for a repeated identity

- **Where**: `crypto/secretcrypt/secretcrypt.go:84`
- **Dimension**: performance
- **Detail**: Crypter.aead (line 84) performs HKDF-SHA256 extraction+expansion plus subtle.NewAESGCM — which internally builds Tink Parameters, a Key object, and the AEAD (several allocations and an AES key schedule) — on every single Encrypt and Decrypt call, with no memoization. Failure scenario: a webhook dispatcher decrypting the same tenant's signing key on every inbound request pays HKDF + full AEAD construction per request; at high QPS this is measurable CPU and allocation churn for byte-identical (label, identity) inputs. The stateless design also removes any place to zero the derived key (see the Close finding).
- **Suggestion**: Cache the constructed AEAD per identity in a bounded/synchronized map (or document that callers should hold one AEAD per identity via a new exported helper), keeping derivation on the miss path only.

