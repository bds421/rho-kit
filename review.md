# rho-kit — full-repository code review

> **Coverage: complete.** Every one of the 65 review units across the whole workspace has now been
> reviewed line-by-line. The sweep ran in several passes (rate limits forced restarts), so findings
> come in two tiers:
>
> - **Adversarially verified** (the later pass): findings from `security`, `infra/sqldb`,
>   `infra/messaging/nats|redis`, `infra/storage/sftp|http|uploadsec`, `observability`, `runtime`,
>   `resilience`, `realtime`, `io`, `testing`, the CI/build/release tooling, SQL migrations, dashboards,
>   docs, and the cross-cutting sweep — each was independently re-checked by a second agent trying to
>   refute it (3 were refuted and moved to the appendix).
> - **Reviewer-reported (unverified)**: findings from the first pass (`crypto`, `core`, `httpx`,
>   `grpcx`, `data`, `app`, `auth`, `flags`, `cmd`, `examples`) — the verification stage there was
>   killed by rate limits, so treat these as leads to confirm.
>
> Each finding is tagged with its tier and the reviewer's **confidence** (0–1). Line numbers are
> approximate; confirm against current `main` before acting. v2 is shipped, so API-breaking fixes
> are flagged as v3 candidates where noticed.

## Summary

**752 findings** (114 adversarially verified, 638 reviewer-reported): `2` critical · `68` high · `247` medium · `356` low · `79` info. 3 candidate findings were refuted (appendix).

| Area | crit | high | med | low | info | total |
|------|----:|----:|----:|----:|----:|----:|
| cross-cutting | 0 | 2 | 3 | 4 | 1 | 10 |
| crypto | 1 | 3 | 7 | 14 | 5 | 30 |
| core | 0 | 2 | 9 | 19 | 1 | 31 |
| security | 0 | 1 | 3 | 4 | 2 | 10 |
| httpx | 0 | 14 | 46 | 84 | 19 | 163 |
| grpcx | 0 | 2 | 7 | 6 | 1 | 16 |
| data | 0 | 6 | 27 | 50 | 7 | 90 |
| infra | 0 | 20 | 57 | 72 | 15 | 164 |
| app | 0 | 0 | 7 | 10 | 6 | 23 |
| runtime+io+resilience | 0 | 1 | 11 | 15 | 4 | 31 |
| observability | 0 | 0 | 4 | 6 | 4 | 14 |
| auth+authz | 0 | 1 | 2 | 4 | 0 | 7 |
| flags | 0 | 0 | 1 | 2 | 1 | 4 |
| cmd+tools | 0 | 4 | 9 | 18 | 2 | 33 |
| testing | 0 | 0 | 0 | 8 | 2 | 10 |
| examples | 0 | 0 | 3 | 5 | 0 | 8 |
| build+ci | 0 | 1 | 7 | 15 | 4 | 27 |
| sql+templates+dashboards | 1 | 2 | 9 | 9 | 4 | 25 |
| docs | 0 | 9 | 35 | 11 | 1 | 56 |
| **TOTAL** | **2** | **68** | **247** | **356** | **79** | **752** |

By dimension: correctness 152 · docs 137 · testing 95 · api-design 89 · consistency 69 · error-handling 66 · security 40 · concurrency 40 · build-ci 23 · performance 22 · resource-leak 19

## Priority issues to fix first (critical + high)

70 items. ✓ = adversarially verified; ⚠ = reviewer-reported, confirm before acting. Full detail in the area sections below.

- ⚠ **[CRITICAL]** `crypto/envelope/envelope.go:244` — Rewrap of a legacy v2 blob emits a v3 header, making the blob permanently undecryptable <sub>(correctness · conf 0.90)</sub>
- ✓ **[CRITICAL]** `data/saga/pgstore/store.go:126` — Optimistic-concurrency check on updated_at breaks every multi-step saga under Postgres <sub>(correctness · conf 0.83)</sub>
- ✓ **[HIGH]** `AGENTS.md:39` — Golden-path snippet claims to compile/copy-paste but does not, and contradicts its own 'illustrative' disclaimer <sub>(docs · conf 0.92)</sub>
- ✓ **[HIGH]** `auth/oauth2/AGENTS.md:55` — AGENTS.md falsely claims OAuth2 client does NOT verify ID-token signature/aud/iss <sub>(docs · conf 0.97)</sub>
- ⚠ **[HIGH]** `auth/oauth2/client.go:322` — Open redirect: post-login redirect_to is attacker-controlled and unvalidated <sub>(security · conf 0.85)</sub>
- ⚠ **[HIGH]** `cmd/kit-doctor/rules/centrifuge_missing_jwt_auth.go:29` — Critical-severity centrifuge-missing-jwt-auth rule never fires: wrong module path <sub>(correctness · conf 0.95)</sub>
- ⚠ **[HIGH]** `cmd/kit-doctor/rules/websocket_any_origin_unsafe.go:30` — Both websocket rules are dead: import path does not exist <sub>(correctness · conf 0.95)</sub>
- ⚠ **[HIGH]** `cmd/kit-migrate/main.go:34` — auditlog and outbox migrations share goose version 20260514000001; publish-all yields a directory goose refuses to run <sub>(correctness · conf 0.78)</sub>
- ⚠ **[HIGH]** `core/config/watcher.go:219` — FileWatcher debounce channel never re-armed — only the first change ever reloads <sub>(correctness · conf 0.90)</sub>
- ⚠ **[HIGH]** `core/validate/schema.go:100` — Nil slice/map/pointer fields marshal as JSON null and always fail validation <sub>(correctness · conf 0.75)</sub>
- ⚠ **[HIGH]** `crypto/envelope/awskms/awskms.go:213` — Alias-configured KEK pins Decrypt to the alias, so repointing the alias bricks decryption AND rewrap of all old envelopes <sub>(correctness · conf 0.70)</sub>
- ⚠ **[HIGH]** `crypto/envelope/gcpkms/gcpkms.go:170` — Unwrap sends a CryptoKeyVersion name as DecryptRequest.Name — GCP KMS rejects it, so every Unwrap fails <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `crypto/paseto/paseto.go:368` — V4Local uses paseto.NewParser(), whose NotExpired rule overrides kit exp validation <sub>(correctness · conf 0.92)</sub>
- ⚠ **[HIGH]** `data/actionlog/actionlog.go:579` — OccurredAt signed at ns precision but persisted at µs — every postgres entry fails verification <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `data/actionlog/postgres/store.go:297` — scanEntry decodes metadata numbers as float64 — int64 > 2^53 breaks signatures permanently <sub>(correctness · conf 0.80)</sub>
- ⚠ **[HIGH]** `data/cron/pgstore/store.go:217` — ApplyTo passes DB-sourced spec/name to Scheduler.Add, which panics on invalid input <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `data/lock/pgadvisory/pgadvisory.go:173` — Failed Release returns pooled conn with advisory lock still held — permanent lock leak <sub>(resource-leak · conf 0.78)</sub>
- ⚠ **[HIGH]** `data/queue/redisqueue/queue.go:546` — Queue.Close always returns a non-nil error <sub>(error-handling · conf 0.93)</sub>
- ✓ **[HIGH]** `data/saga/pgstore/doc.go:22` — Docs claim executor re-reads on ErrConcurrentUpdate, but it never handles that error <sub>(docs · conf 0.80)</sub>
- ⚠ **[HIGH]** `data/stream/redisstream/consumer.go:686` — Every handler invocation gets a hidden, unconfigurable 30s deadline <sub>(correctness · conf 0.78)</sub>
- ✓ **[HIGH]** `docs/ai/messaging.md:32` — Messaging Quick Start uses removed Infrastructure adapter fields (infra.Broker/Publisher/Consumer) <sub>(docs · conf 0.97)</sub>
- ✓ **[HIGH]** `docs/ai/messaging.md:65` — amqpbackend.Dial and WithAllowPlaintext do not exist <sub>(docs · conf 0.97)</sub>
- ✓ **[HIGH]** `docs/ai/redis.md:120` — Stream section uses non-existent redisstream symbol names <sub>(docs · conf 0.96)</sub>
- ✓ **[HIGH]** `docs/ai/sqldb.md:84` — Builder.WithMigrations and infra.DB do not exist (stale pre-v2 wiring) <sub>(docs · conf 0.96)</sub>
- ⚠ **[HIGH]** `grpcx/client/interceptor/deadline.go:37` — DeadlineStream docstring falsely claims setup-only bounding; default 30s deadline kills all long-lived streams <sub>(api-design · conf 0.80)</sub>
- ⚠ **[HIGH]** `grpcx/interceptor/stream_limits.go:142` — StreamIdleTimeout cannot terminate streams blocked in RecvMsg — cancellation is cooperative-only <sub>(correctness · conf 0.75)</sub>
- ⚠ **[HIGH]** `httpx/budget/budget.go:329` — Hard-enforcement reconcile refunds the full pre-charge despite real upstream spend <sub>(correctness · conf 0.65)</sub>
- ⚠ **[HIGH]** `httpx/middleware/auth/scope.go:81` — HTTP scope parsing (comma-separated) contradicts gRPC interceptor and in-package docs (space-separated) <sub>(consistency · conf 0.85)</sub>
- ⚠ **[HIGH]** `httpx/middleware/clientip/clientip.go:74` — X-Real-IP from any trusted peer is blindly preferred over X-Forwarded-For, enabling IP spoofing behind non-nginx proxies <sub>(security · conf 0.55)</sub>
- ⚠ **[HIGH]** `httpx/middleware/compress/compress.go:173` — Deferred finalize commits 200 OK on handler panic, defeating outer recover middleware <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `httpx/middleware/compress/writer.go:92` — Write returns n > len(p) when commitCompressed flushes previously buffered bytes <sub>(correctness · conf 0.90)</sub>
- ⚠ **[HIGH]** `httpx/middleware/signedrequest/signedrequest.go:418` — Spooled-body temp file never closed: net/http does not close a replaced r.Body <sub>(resource-leak · conf 0.75)</sub>
- ⚠ **[HIGH]** `httpx/middleware/tracing/tracing.go:69` — Span never renamed to route pattern: r.Pattern read from original request, mux mutates the copy <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `httpx/openapi/openapi.go:111` — Options.ErrorMapper is dead API: errorMapperMiddleware discards the mapper and returns next unchanged <sub>(api-design · conf 0.93)</sub>
- ⚠ **[HIGH]** `httpx/openapigen/handle.go:69` — Handle mounts dead mux route for non-uppercase methods while spec registers successfully <sub>(correctness · conf 0.75)</sub>
- ⚠ **[HIGH]** `httpx/problemdetails/problem.go:332` — mapStatus drifts from httpx.HTTPStatus: TIMEOUT and PAYLOAD_TOO_LARGE map to 500 <sub>(correctness · conf 0.88)</sub>
- ⚠ **[HIGH]** `httpx/resilient.go:226` — WithCBShouldTrip cannot exclude transport errors from tripping the breaker <sub>(correctness · conf 0.80)</sub>
- ⚠ **[HIGH]** `httpx/webhook/webhook.go:177` — Dispatcher posts to caller-supplied URLs with no SSRF defenses <sub>(security · conf 0.70)</sub>
- ⚠ **[HIGH]** `httpx/websocket/handler.go:89` — Per-connection ctx is never cancelled on connection close, contradicting documented contract <sub>(api-design · conf 0.80)</sub>
- ⚠ **[HIGH]** `httpx/websocket/heartbeat.go:48` — Heartbeat ping requires a concurrently-reading handler; push-only handlers get killed <sub>(correctness · conf 0.70)</sub>
- ⚠ **[HIGH]** `infra/leaderelection/etcd/election.go:353` — Drain watchdog starts at term start, not at leadership end <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `infra/leaderelection/k8slease/lease.go:405` — Run is one-shot, returns nil on leadership loss, and the started guard makes the documented retry loop impossible <sub>(api-design · conf 0.80)</sub>
- ✓ **[HIGH]** `infra/leaderelection/pgadvisory/pgadvisory.go:266` — pgadvisory Run re-acquires leadership after callback-drain timeout (guard present only in redislock) <sub>(concurrency · conf 0.85)</sub>
- ⚠ **[HIGH]** `infra/leaderelection/pgadvisory/pgadvisory.go:267` — Drain timeout: Run retries acquire instead of returning, enabling in-process double leader <sub>(correctness · conf 0.92)</sub>
- ⚠ **[HIGH]** `infra/leaderelection/pgadvisory/pgadvisory.go:390` — Health-check Extend has no deadline; hung probe defeats lost-leader detection <sub>(concurrency · conf 0.75)</sub>
- ⚠ **[HIGH]** `infra/messaging/amqpbackend/topology.go:167` — Retry redelivery via original exchange duplicates messages into sibling queues <sub>(correctness · conf 0.80)</sub>
- ⚠ **[HIGH]** `infra/messaging/buffered_publisher.go:30` — State-file persistence silently drops Message.Headers on restart replay <sub>(correctness · conf 0.85)</sub>
- ⚠ **[HIGH]** `infra/messaging/buffered_publisher.go:658` — Drain throughput hard-capped at ~20 msg/s; buffer can never recover under moderate load <sub>(performance · conf 0.80)</sub>
- ✓ **[HIGH]** `infra/messaging/kafkabackend/AGENTS.md:18` — Kafka NewConsumer and WithSASL APIs do not exist <sub>(docs · conf 0.96)</sub>
- ⚠ **[HIGH]** `infra/messaging/kafkabackend/subscriber.go:371` — Multi-topic Subscriber delivers every topic's messages to a single binding's handler <sub>(correctness · conf 0.88)</sub>
- ⚠ **[HIGH]** `infra/messaging/kafkabackend/subscriber.go:440` — Non-permanent handler errors lose the message once any later record on the partition commits <sub>(correctness · conf 0.82)</sub>
- ✓ **[HIGH]** `infra/messaging/natsbackend/natsbackend.go:921` — Consumer merges kit-internal X-* headers into user headers; can exceed MaxMessageHeaders and drop valid messages <sub>(correctness · conf 0.82)</sub>
- ⚠ **[HIGH]** `infra/outbox/relay.go:409` — Heartbeat covers only the in-flight entry; queued claimed batch entries can be stale-reset mid-batch, causing duplicate publishes <sub>(concurrency · conf 0.75)</sub>
- ⚠ **[HIGH]** `infra/secrets/cache.go:148` — Doc-mandated caller Zero() corrupts the shared cache entry <sub>(correctness · conf 0.90)</sub>
- ⚠ **[HIGH]** `infra/secrets/cache.go:199` — fetchAndStore/Invalidate zero secrets still held by concurrent Get callers <sub>(concurrency · conf 0.85)</sub>
- ⚠ **[HIGH]** `infra/secrets/gcpsm/gcpsm.go:20` — Real *secretmanager.Client does not satisfy gcpsm.API — package unusable with real client <sub>(api-design · conf 0.95)</sub>
- ⚠ **[HIGH]** `infra/storage/azurebackend/azure.go:386` — Exists fails for existing zero-length blobs (416 InvalidRange) <sub>(correctness · conf 0.70)</sub>
- ⚠ **[HIGH]** `infra/storage/circuitbreaker/breaker.go:281` — listImpl records a phantom breaker success on dispatch; stream failures never count <sub>(correctness · conf 0.80)</sub>
- ⚠ **[HIGH]** `infra/storage/localbackend/list.go:43` — List treats prefix as a directory path, not a string prefix <sub>(correctness · conf 0.85)</sub>
- ✓ **[HIGH]** `infra/storage/sftpbackend/list.go:148` — SFTP List yields unsorted keys, breaking StartAfter/ListPage pagination <sub>(correctness · conf 0.82)</sub>
- ✓ **[HIGH]** `infra/storage/storagehttp/uploadsec/uploadsec.go:721` — JPEG polyglot bypass: trailing-bytes check only inspects last two bytes <sub>(security · conf 0.83)</sub>
- ✓ **[HIGH]** `infra/storage/storagehttp/uploadsec/uploadsec.go:779` — GIF polyglot bypass: trailing-bytes check only inspects last byte <sub>(security · conf 0.82)</sub>
- ✓ **[HIGH]** `observability/dashboards/grafana/redis.json:29` — redis/storage dashboards select on `instance` label that collides with Prometheus reserved target label <sub>(correctness · conf 0.82)</sub>
- ✓ **[HIGH]** `realtime/centrifuge/node.go:74` — Node metrics are never wired: WithMetricsRegisterer is a silent no-op and n.metrics is always nil <sub>(api-design · conf 0.95)</sub>
- ✓ **[HIGH]** `runtime/cron/AGENTS.md:62` — Cron metric names and labels are entirely fictional <sub>(docs · conf 0.98)</sub>
- ✓ **[HIGH]** `runtime/saga/AGENTS.md:17` — Saga Key APIs reference non-existent Workflow type and method <sub>(docs · conf 0.97)</sub>
- ✓ **[HIGH]** `runtime/saga/executor.go:218` — saga Resume goroutine has no panic recovery; a panicking step crashes the host process <sub>(concurrency · conf 0.80)</sub>
- ✓ **[HIGH]** `security/jwtutil/signing_provider.go:649` — Rotation to a wrong-shape key silently poisons SigningProvider; no callback, no stale fallback <sub>(correctness · conf 0.82)</sub>
- ✓ **[HIGH]** `tools/check-doc-rot/main.go:83` — Shipped-wave detection far looser than documented contract <sub>(correctness · conf 0.78)</sub>
- ✓ **[HIGH]** `tools/release-version.sh:78` — go mod tidy failure swallowed with an echo; release proceeds to commit and tag a module with wrong requires <sub>(error-handling · conf 0.85)</sub>

---

## Findings by area

Each finding shows severity, `file:line`, dimension, reviewer confidence, and verification status (**verified** = independently re-checked; **reviewer-reported / low/info** = unverified).

### cross-cutting — repo-wide consistency sweep (errorf %w, context, clock, goroutine safety, sibling-family drift)

_10 findings — 2 high, 3 medium, 4 low, 1 info_

#### 1. [HIGH] pgadvisory Run re-acquires leadership after callback-drain timeout (guard present only in redislock)
`infra/leaderelection/pgadvisory/pgadvisory.go:266` — concurrency — conf 0.85 — _verified_

redislock.Run line 272 has `if errors.Is(holdErr, ErrCallbackDrainTimeout) { return }` to refuse re-acquire while an orphan OnAcquired goroutine is still running. pgadvisory.Run has no such check: when holdErr is ErrCallbackDrainTimeout it logs and loops to re-acquire, letting the same process enter leadership twice — defeating the no-overlap invariant its own holdLeadership doc (L320) promises.

**Suggested fix:** Mirror redislock L272: after holdLeadership, if errors.Is(holdErr, ErrCallbackDrainTimeout) log and return holdErr instead of looping.

#### 2. [HIGH] saga Resume goroutine has no panic recovery; a panicking step crashes the host process
`runtime/saga/executor.go:218` — concurrency — conf 0.80 — _verified_

Resume drives instances concurrently (L218 goroutine calling executeInstance -> step.Forward/step.Compensate, which are user callbacks). No recover() exists anywhere in the saga package. A panic in a user step during startup recovery crashes the whole process, breaking the documented promise (L197) that individual failures do not short-circuit the batch. Sibling families (leaderelection, eventbus runAsync, messaging consumers, cache executeCompute, concurrency fanout) all guard callbacks with recover.

**Suggested fix:** Wrap step.Forward/step.Compensate (or executeInstance in the Resume goroutine) with recover() that converts the panic into results[i].Err using redact.PanicValue.

#### 3. [MEDIUM] Raw OIDC verify error rendered into the HTTP response body
`auth/oauth2/client.go:290` — security — conf 0.72 — _verified_

http.Error(w, fmt.Sprintf("%s: %v", ErrCodeExchange, err), 400) writes the raw verifier error to the client. OIDC verifier and token-exchange errors can embed token claims, signature details, or endpoint response bodies. Lines 278/289 also log err.Error() raw rather than redact.Error. This leaks across a trust boundary in a request path while the rest of the kit redacts.

**Suggested fix:** Return only ErrCodeExchange.Error() in the response body; log the detail with redact.Error(err) instead of err.Error().

#### 4. [MEDIUM] CachedLoader logs raw err.Error() from arbitrary loaders, bypassing redact
`infra/secrets/cache.go:173` — security — conf 0.70 — _verified_

Lines 173 and 227 log slog.String("error", err.Error()) for errors from c.inner (any user-supplied secrets.Loader). The kit's redact package exists precisely because backend error strings 'often include request URLs, object keys, ... payload data'. 102 kit call sites use redact.Error(err); these 2 do not. A non-kit loader's error can leak topology/secrets into logs.

**Suggested fix:** Use redact.Error(err) instead of slog.String("error", err.Error()) at both sites; this redacts unconditionally regardless of error source.

#### 5. [MEDIUM] gcpsm.Get can panic in the request path; sibling loaders return errors
`infra/secrets/gcpsm/gcpsm.go:100` — api-design — conf 0.72 — _verified_

resolveName (called from Get) panics with 'bare secret name requires WithProject' when a non-projects/ key is passed and no project configured. awssm.Get and vaultkv.Get never panic — they always return errors. A dynamic/per-tenant bare key against a misconfigured loader crashes the caller's goroutine instead of returning an error, inconsistent across the secrets family.

**Suggested fix:** Return an error from resolveName/Get instead of panicking, matching awssm and vaultkv.

#### 6. [LOW] pgadvisory.Acquire does not validate the lock key; redislock does
`data/lock/pgadvisory/pgadvisory.go:96` — consistency — conf 0.60 — _low/info (unverified)_

redislock.doAcquire calls validateLockKey (L175) — added to reject empty/control-byte/NUL/over-long keys that 'corrupt logs and ACL evaluation'. pgadvisory.Acquire performs no key validation; an empty key hashes to a valid int64 and silently acquires, and unvalidated bytes flow into spans/logs. Lower impact than redislock (key never hits raw SQL, it's SHA-256 hashed) but a sibling-consistency gap.

**Suggested fix:** Add an equivalent validateLockKey guard (reject empty, control bytes, over-length) to pgadvisory.Acquire/AcquireTx for parity.

#### 7. [LOW] redislock/pgadvisory skip drainStateDrained metric on the happy path
`infra/leaderelection/redislock/redislock.go:397` — consistency — conf 0.70 — _low/info (unverified)_

holdLeadership's normal-completion branch (`case result := <-cbDone` L397) returns without recording the drainStateDrained observation that awaitCallbackDrain emits — only the parent.Done/extend paths route through awaitCallbackDrain. pgadvisory L384 has the identical gap. etcd (L353) and k8slease (L371) always route through awaitCallbackDrain, so they record it. The awaitCallbackDrain doc claims 'terminal duration is always recorded'.

**Suggested fix:** Record drainStateDrained on the immediate cbDone success path too, or always route completion through awaitCallbackDrain as etcd/k8slease do.

#### 8. [LOW] amqpbackend panics on disallowed TLS config while kafka/redis return errors
`infra/messaging/amqpbackend/connection.go:359` — api-design — conf 0.60 — _low/info (unverified)_

cloneTLSConfigWithFloor panics on ErrInsecureSkipVerifyNotPermitted (L359) and on a bad MaxVersion (L361). kafkabackend (client.go L85) and redis (config.go L119) return errors for the same condition. Both fail fast at startup, but the panic-vs-error split is a cross-backend API inconsistency for the same misconfiguration class.

**Suggested fix:** Consider returning an error from the WithTLS path for parity, or document that amqpbackend deliberately panics at option-application time.

#### 9. [LOW] gcpsm exposes full resource name as Secret version; siblings expose bare version
`infra/secrets/gcpsm/gcpsm.go:89` — consistency — conf 0.70 — _low/info (unverified)_

Code sets version = resp.Name (full 'projects/P/secrets/S/versions/N') despite the adjacent comment saying 'expose N'. awssm exposes *out.VersionId and vaultkv exposes strconv.Itoa(Version) — bare version identifiers. Callers comparing Secret.Version across loaders get inconsistent shapes from gcpsm.

**Suggested fix:** Parse and expose just the trailing version segment from resp.Name to match awssm/vaultkv, or update the comment to reflect the full-name contract intentionally.

#### 10. [INFO] TODO/FIXME/XXX/HACK inventory is effectively empty repo-wide
`runtime/saga/executor.go:360` — docs — conf 0.95 — _low/info (unverified)_

A repo-wide grep finds zero TODO/FIXME/XXX/HACK in non-test Go code; the only 10 matches are context.TODO() in two test files (legitimate). This is a notably clean state for a ~105-module kit and reflects disciplined backlog hygiene. (Line cited is the saga compensate-error log, the nearest reviewed line; no marker present.)

**Suggested fix:** No action needed; noting for completeness of the cross-cutting sweep.


### crypto — encryption, envelope/KMS, paseto, signing, passhash, masking

_30 findings — 1 critical, 3 high, 7 medium, 14 low, 5 info_

#### 11. [CRITICAL] Rewrap of a legacy v2 blob emits a v3 header, making the blob permanently undecryptable
`crypto/envelope/envelope.go:244` — correctness — conf 0.90 — _reviewer-reported (unverified)_

Rewrap discards the parsed version (line 212) and buildHeader hardcodes blobVersion=3, but the v2 body stays sealed under the v2 AAD (callerAAD||sepV2). Decrypt of the rewrapped blob computes v3 AAD (sepV3||uvarint||callerAAD) and always fails with ErrAuthFailed — the package's own TestDecrypt_V3BlobSealedWithV2AADFails proves this combination fails. Rotation flows that write the rewrapped blob back over the original corrupt every legacy v2 record. No test covers Rewrap of a v2 blob.

**Suggested fix:** Thread the parsed version into header serialization (emit a v2 header for v2 blobs) or return an explicit error from Rewrap for v2 blobs; add a Rewrap-v2 round-trip test.

#### 12. [HIGH] Alias-configured KEK pins Decrypt to the alias, so repointing the alias bricks decryption AND rewrap of all old envelopes
`crypto/envelope/awskms/awskms.go:213` — correctness — conf 0.70 — _reviewer-reported (unverified)_

decryptKeyIDFor ignores the envelope's embedded key ARN for alias-configured KEKs and always sends the alias as DecryptInput.KeyId. After standard manual rotation (repoint alias to a new key), KMS Decrypt with KeyId=alias fails with IncorrectKeyException for every blob wrapped under the old key. Rewrap also calls Unwrap first, so the migration path itself breaks once the alias moves — contradicting the package doc (lines 3-6) claiming the returned KeyId is passed through so 'Decrypt later targets the same version'.

**Suggested fix:** For alias config, forward the embedded key ARN when its partition/region matches the client (KeyId pinning still prevents redirect), or document that operators must Rewrap before repointing the alias.

#### 13. [HIGH] Unwrap sends a CryptoKeyVersion name as DecryptRequest.Name — GCP KMS rejects it, so every Unwrap fails
`crypto/envelope/gcpkms/gcpkms.go:170` — correctness — conf 0.85 — _reviewer-reported (unverified)_

Wrap returns EncryptResponse.Name, which per kmspb is the CryptoKeyVersion resource (.../cryptoKeyVersions/N), and that string is embedded as the envelope keyID. Unwrap forwards it verbatim as DecryptRequest.Name, but kmspb documents Name as 'the resource name of the CryptoKey to use for decryption. The server will choose the appropriate version' — version-qualified names are rejected with INVALID_ARGUMENT. Every real-KMS decrypt of a Wrap-produced envelope fails. Verified against cloud.google.com/go/kms v1.31.0 (the module's pinned version).

**Suggested fix:** After allowsKeyID validation, send k.key (the parent CryptoKey) as DecryptRequest.Name; KMS selects the version from ciphertext metadata.

#### 14. [HIGH] V4Local uses paseto.NewParser(), whose NotExpired rule overrides kit exp validation
`crypto/paseto/paseto.go:368` — correctness — conf 0.92 — _reviewer-reported (unverified)_

NewV4Local sets parser to paseto.NewParser(), which preloads upstream NotExpired() (verified in go-paseto@v1.6.0 parser.go:16, claims.go:90-103). That rule errors when exp is missing and compares exp to time.Now(). Consequences for v4.local only: WithoutExpiration tokens always fail; WithClockSkewTolerance is ignored for exp (tokens inside the skew window rejected — docs recommend 30s in production); caller-supplied now is ignored; expired tokens return ErrTokenInvalid instead of ErrTokenExpired. NewV4PublicVerifier correctly uses paseto.Parser{} (line 234). Tests avoid all these V4Local cases.

**Suggested fix:** Use paseto.NewParserWithoutExpiryCheck() (or Parser{}) in NewV4Local so validate() owns exp/nbf checks; add V4Local tests for expired/no-exp/skew paths.

#### 15. [MEDIUM] EncryptIfPlain passes legacy v1/v2-prefixed ciphertext through unchanged, so re-save flows never migrate rows to v3
`crypto/encrypt/encrypt.go:273` — api-design — conf 0.60 — _reviewer-reported (unverified)_

isAuthenticatedCiphertext uses stripEncryptedPrefix, which accepts enc:v1:, \x00enc:v2:, and enc:v3:. A valid legacy ciphertext is returned unchanged by EncryptIfPlain, so the advertised migration path ('drop legacy reads once stored data is fully re-encrypted', encrypt.go lines 28-30) silently never completes via idempotent re-save paths. Operators who later drop legacy-prefix support lose access to rows they believed were upgraded.

**Suggested fix:** In EncryptIfPlainWithContext, re-encrypt to v3 when the verified ciphertext carries a legacy prefix (decrypt-then-encrypt), or document loudly that legacy rows are never auto-upgraded.

#### 16. [MEDIUM] NewMetrics silently swallows non-AlreadyRegistered registration errors, leaving an invisible unregistered counter
`crypto/envelope/awskms/metrics.go:65` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

If cfg.registerer.Register fails with anything other than AlreadyRegisteredError (e.g. a name collision with a differently-shaped collector), the error is dropped and the returned Metrics increments a collector that is never scraped. The same silent gap occurs when ExistingCollector is not a *prometheus.CounterVec. This is exactly the failure mode the WithRegisterer nil-panic comment claims to prevent ('operator would discover the gap only at scrape time').

**Suggested fix:** Use errors.As; on any other registration error, panic or return an error instead of silently continuing with an unregistered collector.

#### 17. [MEDIUM] gcpkms and awskms KEKs depend on concrete SDK clients, leaving Wrap/Unwrap request construction completely untested
`crypto/envelope/gcpkms/gcpkms.go:63` — testing — conf 0.85 — _reviewer-reported (unverified)_

KEK holds *kms.KeyManagementClient (and awskms holds *kms.Client) rather than a narrow interface like azurekeyvault's KeyClient. Consequently gcpkms tests never exercise Wrap/Unwrap happy paths, CRC32C verification, or the DecryptRequest.Name construction — which is precisely where the version-qualified-name bug (separate finding) lives. awskms has the same gap for Encrypt/Decrypt request shape.

**Suggested fix:** Define a minimal Encrypt/Decrypt interface (as azurekeyvault does), accept it in NewKEK, and add fake-client round-trip tests covering request fields and checksum paths.

#### 18. [MEDIUM] V4PublicSigner.Close zeroes live ed25519 key bytes racing with in-flight Sign
`crypto/paseto/paseto.go:329` — concurrency — conf 0.75 — _reviewer-reported (unverified)_

ExportBytes on V4AsymmetricSecretKey shares the backing array with s.priv.material (slice field), so Close's zero loop writes bytes that a concurrent Sign (which passed the closed.Load() check before Close ran) is reading inside ed25519.Sign — an unsynchronized read/write data race that can emit tokens signed with partially zeroed key material. SigningProvider.Close (signing_provider.go:223) triggers exactly this against in-flight SigningProvider.Sign, despite its docs claiming both are 'safe for concurrent use'. The refresh path (signing_provider.go:299-306) explicitly acknowledges this race class but Close does not prevent it.

**Suggested fix:** Guard key bytes with an RWMutex (Sign RLock, Close Lock), or document that Close must not run concurrently with Sign.

#### 19. [MEDIUM] V4Local.Close never zeroes the actual working key; doc claims (and cited tripwire test) are false
`crypto/paseto/paseto.go:429` — security — conf 0.88 — _reviewer-reported (unverified)_

Upstream V4SymmetricKey stores material as [32]byte; V4SymmetricKeyFromBytes copies input into a fresh array, and ExportBytes() has a value receiver returning a slice over a receiver copy (go-paseto v4_keys.go:180-182, 197-208). So zeroing v.keyBytes and v.key.ExportBytes() leaves v.key.material intact until GC. The Close doc (lines 401-410) asserts in-place wiping 'covered by a tripwire test' — no such test exists in the package; V4Local.Close/ErrV4LocalClosed are entirely untested.

**Suggested fix:** Document that upstream key material cannot be wiped in place, or hold the key only as kit-owned bytes; add the missing Close test.

#### 20. [MEDIUM] StaticKeyStore.Key races with Close, returning (nil, nil) success
`crypto/signing/keystore.go:154` — concurrency — conf 0.70 — _reviewer-reported (unverified)_

Key checks closed.Load() and k.IsEmpty(), then calls k.Reveal(). A concurrent Close can zero the secret between IsEmpty and Reveal, so Key returns (nil, nil) — a success-shaped result with no key, violating the documented "returns ErrUnknownKeyID after Close" contract. CurrentKeyID has the same window (returns currentID with nil secret, nil error). Kit consumers (httpx/sign validateSigningKey) reject short secrets so this fails safe there, but custom KeyStore consumers following the interface contract may not nil-check.

**Suggested fix:** Reveal once and branch on the result: b := k.Reveal(); if len(b) == 0 { return nil, ErrUnknownKeyID }. Same pattern in CurrentKeyID.

#### 21. [MEDIUM] CanonicalContext binding has no round-trip or mismatch test
`crypto/signing/signing_test.go:307` — testing — conf 0.80 — _reviewer-reported (unverified)_

The v2 context-binding feature (SignContext/VerifyContext with non-empty Method/Path/Domain) is tested only for CR/LF rejection. There is no test that a sign/verify round-trip with a non-empty context succeeds, none that verifying with a different Method/Path/Domain yields ErrInvalidSignature, and none that a v2-context signature is rejected by legacy Verify. The feature's core security property — signature non-portability across endpoints — is entirely unasserted; a regression in buildSignedPayload would pass the suite.

**Suggested fix:** Add tests: SignContext/VerifyContext round-trip success; mismatched Method, Path, and Domain each return ErrInvalidSignature; v2-context signature fails plain Verify and vice versa.

#### 22. [LOW] encryptOptional/encryptOptionalWithContext are dead code — unexported with no production callers
`crypto/encrypt/encrypt.go:283` — correctness — conf 0.80 — _reviewer-reported (unverified)_

Both helpers are package-private and only referenced by their own tests (verified via repo-wide grep). Their doc comments address 'callers' that cannot exist outside the package, suggesting leftovers from a pre-v2 design.

**Suggested fix:** Delete both helpers and their tests, or export them if the optional-encryption pattern is intended for consumers.

#### 23. [LOW] Wrap algorithm is not recorded in the envelope; changing Config.Algorithm breaks decryption of old blobs
`crypto/envelope/azurekeyvault/azurekeyvault.go:189` — api-design — conf 0.60 — _reviewer-reported (unverified)_

Unwrap always uses the currently configured k.algorithm. The envelope keyID records key name/version but not the wrap algorithm, so an operator who migrates from e.g. RSA-OAEP-256 to A256KW (or vice versa) can no longer unwrap previously written envelopes without reverting config. Neither code nor docs warn about this.

**Suggested fix:** Document that Algorithm changes require rewrapping all blobs first, or encode the algorithm into the fallback keyID scheme.

#### 24. [LOW] TamperedHeaderRejected test flips the keyID length byte, not a keyID byte — weaker than intended
`crypto/envelope/envelope_test.go:79` — testing — conf 0.70 — _reviewer-reported (unverified)_

In the v3 layout, blob[5] is the low byte of the uint16 keyID length, not 'the keyID byte (offset 5+)' as the comment claims (true only for the old v2 layout). Flipping it shifts the parse rather than corrupting the keyID, so the test no longer pins keyID-tamper detection specifically.

**Suggested fix:** Mutate a byte inside the keyID region (offset 6..6+kL) and assert the unknown-keyID/auth failure explicitly.

#### 25. [LOW] t.Fatal/t.Fatalf called from httptest handler goroutines
`crypto/envelope/vaulttransit/vaulttransit_test.go:171` — testing — conf 0.60 — _reviewer-reported (unverified)_

newTransitHandler and the guard handlers in TestNewDefaultsAndCopiesContext/TestNewRejectsUnsafePaths/TestKEKRejectsNilContextAndUnknownKey call t.Fatal from the HTTP server goroutine. testing.T.FailNow is documented as only valid from the test goroutine; from a handler it runtime.Goexits the server goroutine mid-response, producing confusing secondary client-side errors and, in unlucky orderings, masking the real assertion message.

**Suggested fix:** Record handler-side failures (t.Error or an error channel) and assert from the test goroutine after the request completes.

#### 26. [LOW] TestMaskURL_Empty asserts nothing
`crypto/masking/masking_test.go:68` — testing — conf 0.90 — _reviewer-reported (unverified)_

The test body is `if got != "://**"+"*" { _ = got }` — the branch is a no-op, so the test passes regardless of what MaskURL("") returns (actual behavior: "***" because Host is empty, contradicting the test's own comment claiming '://***'). The empty-input contract documented on MaskURL is effectively untested.

**Suggested fix:** Assert MaskURL("") == "***" explicitly.

#### 27. [LOW] OpenProvider skips rootCancel on init failure/nil-option panic; OpenSigningProvider does not
`crypto/paseto/provider.go:173` — consistency — conf 0.85 — _reviewer-reported (unverified)_

OpenSigningProvider calls rootCancel() before its nil-option panic (line 159) and on initial-refresh failure (line 169); OpenProvider does neither (lines 165, 173-175). Harmless today only because the parent is context.Background (no registration), but the asymmetry invites a real leak if the parent ever changes, and the two constructors are intended to mirror each other.

**Suggested fix:** Call rootCancel() in OpenProvider's error and panic paths, matching OpenSigningProvider.

#### 28. [LOW] Calling Close from the onRefreshErr callback self-deadlocks
`crypto/paseto/provider.go:225` — concurrency — conf 0.80 — _reviewer-reported (unverified)_

callOnRefreshError runs on the loop goroutine. If the callback invokes Close (e.g. 'shut down after N failures'), Close blocks on <-p.done, which only closes when loop returns — but loop is blocked inside the callback. Permanent deadlock. Applies identically to SigningProvider (signing_provider.go:222). Not documented anywhere.

**Suggested fix:** Document that the callback must not call Close, or make Close non-blocking when invoked from the loop goroutine.

#### 29. [LOW] Close triggers spurious onRefreshErr(context.Canceled) for in-flight refresh
`crypto/paseto/provider.go:247` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

Provider.Close cancels rootCtx; a refresh in flight then fails with context.Canceled and callOnRefreshError fires. Operators who wire the callback to an alert (as the docs instruct) get a false 'rotation stalled' signal on every shutdown that coincides with a tick. Same pattern in SigningProvider.loop (signing_provider.go:247).

**Suggested fix:** Skip the callback when p.closed.Load() is true or errors.Is(err, context.Canceled).

#### 30. [LOW] SigningProvider has no fetch-timeout option, unlike Provider's WithFetchTimeout
`crypto/paseto/signing_provider.go:64` — api-design — conf 0.80 — _reviewer-reported (unverified)_

Provider exposes WithFetchTimeout to override the 10s per-refresh deadline; SigningProvider hardcodes defaultFetchTimeout with no SigningProviderOption to change it. A genuinely slow KMS/HSM key source (the use case WithFetchTimeout documents) cannot be accommodated on the signing side. Also note the initial load in both constructors uses the caller ctx with no timeout applied.

**Suggested fix:** Add WithSigningFetchTimeout mirroring WithFetchTimeout (additive, non-breaking).

#### 31. [LOW] Panic-swallow test self-recovers, never exercising callOnRefreshError's recover wrapper
`crypto/paseto/signing_provider_test.go:231` — testing — conf 0.85 — _reviewer-reported (unverified)_

TestSigningProvider_OnRefreshErrorCallbackPanicSwallowed's callback does `defer func() { _ = recover() }()` before panicking, so the panic never escapes the callback and SigningProvider.callOnRefreshError's recover/slog path (signing_provider.go:258-265) is never reached. The test would pass even if that wrapper were deleted. Contrast with provider_test.go's TestProvider_OnRefreshErrorPanicDoesNotCrashLoop, which panics without self-recovery and genuinely exercises the wrapper.

**Suggested fix:** Remove the callback's self-recover so the panic reaches the provider's recovery wrapper, mirroring the Provider-side test.

#### 32. [LOW] Hash limit violations return ad-hoc fmt.Errorf instead of a sentinel
`crypto/passhash/passhash.go:286` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

Verify rejects out-of-bounds stored params with the typed ErrParamsOutOfBounds, but Hash's five limit checks (lines 286-300) return bare fmt.Errorf strings ('passhash: Memory exceeds limit', etc.) with no format args and no sentinel, so callers cannot errors.Is-branch on a config-typo rejection. Inconsistent with the package's own sentinel-error convention stated at the top of the file.

**Suggested fix:** Introduce ErrHashParamsOutOfBounds (or reuse ErrParamsOutOfBounds) and wrap with %w; use errors.New for static text.

#### 33. [LOW] parsePHC round-trip check skips the version segment, accepting 'v=19junk'
`crypto/passhash/passhash.go:414` — correctness — conf 0.85 — _reviewer-reported (unverified)_

The params segment gets a strict re-encode comparison (line 429) precisely because Sscanf tolerates trailing garbage, but the version segment does not: 'v=19junk', 'v=019', or 'v= 19' all parse as version 19 and are accepted. No security impact (version still compared to argon2.Version), but it contradicts the strict-parse intent documented at lines 424-428 and accepts non-canonical PHC strings.

**Suggested fix:** Apply the same round-trip check to parts[2], or require parts[2] == fmt.Sprintf("v=%d", argon2.Version).

#### 34. [LOW] CurrentKeyID returns success-shaped (id, nil, nil) after Close, violating interface contract
`crypto/signing/keystore.go:167` — api-design — conf 0.70 — _reviewer-reported (unverified)_

The KeyStore interface documents exactly two shapes: (id, secret, nil) on success or ("", nil, err) on failure. StaticKeyStore.CurrentKeyID returns ("", nil, nil) after Close and (s.currentID, nil, nil) when the wrapped key is empty — neither shape. Combined with "static stores never return an error from CurrentKeyID", contract-following callers can proceed to sign with a nil secret and only fail later via length checks, with a confusing error.

**Suggested fix:** Return a sentinel (e.g. ErrKeyStoreClosed or ErrUnknownKeyID) from CurrentKeyID after Close instead of a success-shaped nil-secret result; update the interface doc.

#### 35. [LOW] fallbackMAC comment is wrong: slicing does not protect the array from mutation
`crypto/signing/signing.go:224` — docs — conf 0.85 — _reviewer-reported (unverified)_

The comment claims the fixed-size array "cannot be accidentally mutated (slicing creates a copy header)". False — fallbackMAC[:] (line 285) aliases the package-level array's backing storage; only the slice header is copied. hmac.Equal doesn't write today, but any future write through gotRaw would silently corrupt the shared fallback buffer used by all subsequent verifications. The stated safety rationale does not hold.

**Suggested fix:** Use a local var zero [sha256.Size]byte inside VerifyContext (gotRaw := zero[:]) and delete the misleading comment; cost is one stack array per call.

#### 36. [INFO] Package doc claims KMS Encrypt returns a 'version-qualified key ARN' — AWS KMS has no version-qualified ARNs
`crypto/envelope/awskms/awskms.go:4` — docs — conf 0.80 — _reviewer-reported (unverified)_

AWS KMS Encrypt's response KeyId is the plain key ARN; key rotation is internal and versions are never exposed in ARNs (unlike GCP/Azure). The doc creates a false expectation that old key versions are addressable via the envelope header.

**Suggested fix:** Reword to 'the key ARN of the KMS key used; KMS resolves rotated key material internally'.

#### 37. [INFO] Classified errors returned bare in awskms but %w-wrapped with package prefix in azurekeyvault/gcpkms
`crypto/envelope/awskms/awskms.go:147` — consistency — conf 0.75 — _reviewer-reported (unverified)_

awskms.Wrap/Unwrap return the classified apperror without an 'awskms:' prefix (only unclassified errors get fmt.Errorf wrapping), while the azure and gcp adapters wrap classified errors as 'azurekeyvault: wrap key: %w' / 'gcpkms: encrypt: %w'. Cross-adapter log messages and error strings are inconsistent; also the `classified != err` interface comparison is a fragile pattern.

**Suggested fix:** Wrap classified errors uniformly (fmt.Errorf("awskms: encrypt: %w", classified)) — apperror matching still works through %w.

#### 38. [INFO] classifyGCPError's operation parameter is unused; gcpkms/azurekeyvault lack the error metrics awskms has
`crypto/envelope/gcpkms/errors.go:19` — consistency — conf 0.85 — _reviewer-reported (unverified)_

awskms records request errors to a Prometheus counter inside classification; gcpkms's classifyGCPError takes an operation string but never uses it (no metrics in the package), and azurekeyvault likewise has none. Operators get error-rate visibility for one of three KMS adapters only.

**Suggested fix:** Either add matching Metrics to gcpkms/azurekeyvault or drop the dead operation parameter.

#### 39. [INFO] Rotate doc links to nonexistent [New]; constructor is NewKEK
`crypto/envelope/kekstatic/kekstatic.go:109` — docs — conf 0.90 — _reviewer-reported (unverified)_

The doc comment says 'must already be registered via [New] or [KEK.AddKey]' but the package exports NewKEK, so the godoc link renders broken.

**Suggested fix:** Change [New] to [NewKEK].

#### 40. [INFO] classifyVaultError omits Vault HA/replication transient statuses 412/472/473
`crypto/envelope/vaulttransit/errors.go:31` — error-handling — conf 0.50 — _reviewer-reported (unverified)_

The transient set covers 408/429/5xx, but Vault-specific retryable statuses — 412 (eventual-consistency precondition on Enterprise), 472 (DR replication secondary), 473 (performance standby) — fall through to the default branch unclassified, so apperror.IsUnavailable-driven retry/backoff in callers will not engage during routine HA failovers. The comment says HTTP status is 'the only reliable classification surface', which makes the omission more visible.

**Suggested fix:** Add 412, 472, 473 to the DependencyUnavailable branch.


### core — config, validate, id, secret, tenant, redact, safecast, etc.

_31 findings — 2 high, 9 medium, 19 low, 1 info_

#### 41. [HIGH] FileWatcher debounce channel never re-armed — only the first change ever reloads
`core/config/watcher.go:219` — correctness — conf 0.90 — _reviewer-reported (unverified)_

debounceCh is assigned only when debounceTimer==nil (lines 215-217) and set to nil after the timer fires (line 229), but debounceTimer is never reset to nil. After the first reload, every later event takes the else branch and calls Reset on a timer whose channel nobody selects (debounceCh stays nil), so reload() never runs again. All file changes after the first are silently ignored for the watcher's lifetime. No test exercises a second change-reload cycle, which is why this survived.

**Suggested fix:** In the else branch also set debounceCh = debounceTimer.C (or set debounceTimer = nil after firing). Add a test asserting two sequential change→reload cycles.

#### 42. [HIGH] Nil slice/map/pointer fields marshal as JSON null and always fail validation
`core/validate/schema.go:100` — correctness — conf 0.75 — _reviewer-reported (unverified)_

schemaForReflect emits {"type":"string"/"array"/"object"} for pointer/slice/map fields but never admits null. Struct() validates the json.Marshal round-trip, and encoding/json emits null for nil slices, maps, and pointers when the json tag lacks omitempty. So an optional, untagged `Tags []string` or `Nick *string` field that is nil is rejected with "must be array"/"must be string". This fires through httpx/typed.go:26 and httpx/mcp/mcp.go:941 on every request whose struct has such a field.

**Suggested fix:** For pointer/slice/map-typed fields, emit a type union including null (or anyOf with {"type":"null"}) unless the field is required; or honor the omitempty flag jsonFieldName already parses.

#### 43. [MEDIUM] Stub.Advance is not atomic — concurrent Advance calls lose increments despite 'goroutine-safe' claim
`core/clock/clock.go:90` — concurrency — conf 0.88 — _reviewer-reported (unverified)_

Advance does s.now.Store(s.now.Load().Add(d)) — a non-atomic read-modify-write over atomic.Pointer. Two concurrent Advance calls can read the same base and both store base+d, losing one increment. Struct doc claims "goroutine-safe mutable clock". TestStub_ConcurrentReadsAndWrites (clock_test.go:82) runs 8x1000 concurrent Advances but asserts nothing about the final value, so the lost-update behavior is invisible to the suite.

**Suggested fix:** Use a CAS loop or mutex in Advance; have the concurrent test assert the final time equals start+N*d.

#### 44. [MEDIUM] Self-referential config struct causes unbounded recursion / stack-overflow crash in Load
`core/config/load.go:141` — correctness — conf 0.80 — _reviewer-reported (unverified)_

loadWithEnvTracking recurses into pointer-to-struct fields unconditionally: it allocates reflect.New(field.Type.Elem()) and recurses BEFORE knowing whether any env var was read. For a type like `type Node struct { F string `env:"X"`; Next *Node }` the recursion never terminates, producing an uncatchable stack-overflow fatal. hasEnvTags (line 101-105) has the same unguarded cycle for pointer fields, so even the pre-check crashes when the cyclic field precedes a tagged field.

**Suggested fix:** Track visited reflect.Types (or cap recursion depth) in both hasEnvTags and loadWithEnvTracking; return a typed error on cycles.

#### 45. [MEDIUM] Secret-zeroing claim is defeated by immediate string conversion — documented hygiene property does not hold
`core/config/load.go:255` — docs — conf 0.80 — _reviewer-reported (unverified)_

doc.go claims `_FILE` secrets "do not linger on the heap as immutable strings" because the []byte is zeroed. But resolveWithSource does `return string(b), b, ...` — an immutable heap copy of the full secret is created before b is zeroed, and that string flows through val into parsing for every field type (and is stored permanently for string fields). GetSecret (envutil.go:45) has the identical pattern. The zeroBytes calls are effectively security theater; the documented property is false.

**Suggested fix:** Fix the doc to say only the intermediate file buffer is zeroed, or plumb []byte end-to-end for string-typed secret fields.

#### 46. [MEDIUM] FileWatcher misses Kubernetes ConfigMap/Secret volume updates (symlink swap events filtered out by base-name match)
`core/config/watcher.go:208` — correctness — conf 0.55 — _reviewer-reported (unverified)_

The watcher watches the parent directory but only reacts to events whose Base equals the config file name. Kubernetes updates mounted ConfigMaps/Secrets via the ..data symlink swap: events fire for "..data_tmp"/"..data" and the timestamped dir, never for "config.yaml" itself. All such updates are silently dropped, so reload never fires in the deployment style the package's own docs target (the FileWatcher doc example is "config.yaml"; the secrets docs reference Kubernetes mounts). Atomic-rename editors work; k8s volumes do not.

**Suggested fix:** Resolve the real path via filepath.EvalSymlinks and reload when it changes (viper's approach), or also react to ..data rename events.

#### 47. [MEDIUM] ErrorValue has an unbounded Unwrap loop while sibling ErrorChainTypes explicitly bounds the same threat
`core/redact/redact.go:79` — correctness — conf 0.60 — _reviewer-reported (unverified)_

ErrorValue walks errors.Unwrap with no iteration cap; a cyclic unwrap chain (buggy custom error whose Unwrap returns an ancestor) spins this loop forever on the logging path — ErrorValue is called from redact.Error, wrappedError.Error(), and sentinelWrappedError.Error(), i.e. on virtually every kit log line. ErrorChainTypes 30 lines below caps at 16 frames specifically "so a pathological wrap-loop cannot exhaust memory", so the package acknowledges this threat model but leaves its hottest function unprotected.

**Suggested fix:** Apply the same maxFrames bound (16) to ErrorValue's unwrap loop.

#### 48. [MEDIUM] ErrorValue/ErrorChainTypes are blind to Unwrap() []error — including the package's own WrapSentinel wrapper
`core/redact/redact.go:110` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

Both helpers use errors.Unwrap, which returns nil for multi-error wrappers (Unwrap() []error): sentinelWrappedError (line 213), errors.Join, and fmt.Errorf with two %w. So ErrorValue(WrapSentinel(s, cause)) renders "<redacted error: *redact.sentinelWrappedError>" instead of the deepest cause type, and ErrorChainTypes returns a one-element chain. WrapSentinel is used by infra/secrets/*, data/queue, data/lock, messaging — and app/serviceboot.go:44 logs redact.ErrorChain(err) on fatal exit, the function's stated canonical use case, where the triage info is lost.

**Suggested fix:** Handle interface{ Unwrap() []error } in both walkers (DFS first branch for ErrorValue, bounded BFS/DFS for ErrorChainTypes).

#### 49. [MEDIUM] [N]byte arrays wrongly schematized as base64 string
`core/validate/schema.go:117` — correctness — conf 0.85 — _reviewer-reported (unverified)_

The reflect.Array branch treats any uint8 element type as "[]byte marshals as a base64 string", but encoding/json only base64-encodes byte slices; byte arrays ([16]byte UUIDs, [32]byte hashes) marshal as JSON arrays of numbers. The schema says type string, so any struct with a [N]byte field fails validation unconditionally.

**Suggested fix:** Restrict the base64-string shortcut to reflect.Slice; let reflect.Array of uint8 fall through to the array-of-integer schema.

#### 50. [MEDIUM] Tag splitting on ',' breaks pattern= regexes containing commas
`core/validate/schema.go:401` — correctness — conf 0.85 — _reviewer-reported (unverified)_

applyStringConstraints splits the jsonschema tag on every comma, so `pattern=^[a-z]{2,5}$` becomes Pattern "^[a-z]{2" (invalid regex) and "5}$" silently becomes the field description. Bounded quantifiers {m,n} are extremely common. The truncated pattern either fails schema compilation (every Struct call for that type returns an OperationFailed error) or silently validates the wrong expression. Same limitation hits contains=/excludesall= values with commas. Undocumented.

**Suggested fix:** Support escaping or quoting commas in tag values (e.g. backslash-comma), or at minimum document the limitation and reject patterns containing '{' digits ',' at schema build time.

#### 51. [MEDIUM] SchemaFor/SchemaForType compile and cache schemas without freezing the format registry
`core/validate/validate.go:198` — concurrency — conf 0.60 — _reviewer-reported (unverified)_

Only Struct() sets frozen. SchemaFor[T]/SchemaForType call schemaForType, which compiles with the current format snapshot and caches the result permanently. Sequence: SchemaFor[Req]() during startup (e.g. OpenAPI export) → RegisterFormat("custom") succeeds (not frozen) → Struct(Req) uses the stale cached compiled schema in which the unknown format was ignored, so the custom validation silently never runs for that type. The documented freeze invariant is bypassable.

**Suggested fix:** Freeze the registry in schemaForType (shared by both paths), or key/invalidate the cache on the format-set generation.

#### 52. [LOW] Package doc says 'nine Code values' but twelve exist; constructor list omits the three newer families
`core/apperror/doc.go:16` — docs — conf 0.95 — _reviewer-reported (unverified)_

doc.go enumerates nine codes and omits CodeStorageFull, CodeTimeout, and CodePayloadTooLarge (all defined in errors.go and returned by AllCodes, which has twelve entries). The Constructors section likewise omits NewStorageFull*, NewTimeout*, and NewPayloadTooLarge*. Godoc consumers get a stale picture of the error alphabet.

**Suggested fix:** Update doc.go to list all twelve codes and the missing constructors.

#### 53. [LOW] AppError documented as 'sealed' but the interface is freely implementable externally
`core/apperror/errors.go:88` — api-design — conf 0.85 — _reviewer-reported (unverified)_

The doc claims "This interface is sealed: it is only implemented by types within the apperror package", but AppError contains only exported methods (Error, ErrorCode, Retryable), so any external type can implement it and will satisfy ShouldRetry's errors.As. Code elsewhere that assumes the closed set of concrete types (e.g. transport adapters type-switching) can silently mishandle external implementations.

**Suggested fix:** Fix the doc to say 'by convention', or add an unexported marker method (BREAKING — v3 candidate).

#### 54. [LOW] Retryable/interface tests omit the newer error types (Timeout, PayloadTooLarge, StorageFull, Forbidden)
`core/apperror/errors_test.go:281` — testing — conf 0.85 — _reviewer-reported (unverified)_

TestRetryable's table covers 13 constructors but lacks NewTimeout (retryable=true) and NewPayloadTooLarge (false); ShouldRetry tests also never exercise them. TestAppErrorInterface compile-asserts eight types but omits ForbiddenError, StorageFullError, TimeoutError, and PayloadTooLargeError. A future edit flipping TimeoutError.Retryable would pass the suite.

**Suggested fix:** Add the four missing types to TestAppErrorInterface and Timeout/PayloadTooLarge rows to TestRetryable/TestShouldRetry.

#### 55. [LOW] parseEnvTag accepts fieldName but never uses it — tag errors do not identify the offending field
`core/config/load.go:213` — error-handling — conf 0.60 — _reviewer-reported (unverified)_

Both error returns ("field env tag must name an environment variable", "field env tag has unknown option") omit the field name and tag content; the fieldName parameter is dead. In a config struct with dozens of fields the operator gets no indication which field's tag is malformed. Tests even assert the field name is absent (load_test.go:94,105), so it appears deliberate — but Go struct field names are not secrets, and the message as shipped is unactionable.

**Suggested fix:** Include fieldName in the error message (it is developer-controlled, not secret), or delete the unused parameter.

#### 56. [LOW] Subscriber calling Watchable.Set deadlocks (setMu held during synchronous callbacks, non-reentrant)
`core/config/watchable.go:61` — concurrency — conf 0.70 — _reviewer-reported (unverified)_

Set holds setMu for the entire synchronous notification pass. A subscriber that reacts by calling Set on the same Watchable (e.g. normalizing/clamping the new config and writing it back — a plausible OnChange pattern) self-deadlocks on setMu instantly and permanently blocks all future Set callers. The doc explicitly blesses re-entrant OnChange but is silent about re-entrant Set, and the panic-recovery wrapper cannot help since the goroutine never returns.

**Suggested fix:** Document that subscribers must not call Set, or detect re-entrancy (goroutine-local flag) and panic with a clear message.

#### 57. [LOW] WithSignalChannel silently disables SIGHUP unless the caller wires signal.Notify themselves
`core/config/watcher.go:288` — api-design — conf 0.55 — _reviewer-reported (unverified)_

When cfg.signalCh is provided, EnvReloader.Start never calls signal.Notify on it — only the internally-created channel is subscribed. The WithSignalChannel doc ("allows fine-grained control when multiple EnvReloader instances or other SIGHUP listeners coexist") never states the caller must call signal.Notify; a caller passing a fresh channel gets a reloader that ignores real SIGHUPs forever, with no error or log. Tests only inject signals directly into the channel, so the gap is invisible.

**Suggested fix:** Document that the caller owns signal.Notify/Stop for injected channels, or Notify the provided channel for SIGHUP too.

#### 58. [LOW] Security-relevant rejection paths of SetRequestID/SetCorrelationID are untested
`core/contextutil/request_id_test.go:13` — testing — conf 0.85 — _reviewer-reported (unverified)_

The setters' whole security story (per their doc comments, closing a "Wave 68 hostile-review finding") is silently dropping empty IDs, IDs >128 bytes, and IDs with control/non-printable bytes via isValidContextID. Neither request_id_test.go nor correlation_id_test.go contains a single test feeding an invalid ID; id_validation_test.go only tests the unrelated, stricter IsValidCorrelationToken. A regression in isValidContextID (e.g. dropping the length cap) would pass the suite.

**Suggested fix:** Add table tests asserting SetRequestID/SetCorrelationID return ctx unchanged for control chars, >128-byte, and empty inputs.

#### 59. [LOW] id.Generator is an unsynchronized exported package-level var — swap pattern races with concurrent readers
`core/id/id.go:15` — concurrency — conf 0.50 — _reviewer-reported (unverified)_

Generator is a plain `var func() string` that the docs instruct tests (and implicitly callers) to reassign. Any write to it concurrent with a read from another goroutine is a data race (function values are word-sized but unsynchronized per the Go memory model); under `go test -race` with t.Parallel tests, or any runtime swap in a live service, this fires. The package offers no atomic accessor and no guidance against parallel-test swaps.

**Suggested fix:** Back it with atomic.Pointer[func() string] behind Set/Get accessors (additive), or document that swaps are test-only and must not run in parallel.

#### 60. [LOW] Equal leaves unzeroed plaintext copies on the heap
`core/secret/secret.go:200` — security — conf 0.85 — _reviewer-reported (unverified)_

Equal copies both secrets' bytes into temporary slices (append) and never wipes them after constantTimeEqual, leaving plaintext in GC-managed heap memory. This contradicts the package's own lifetime-bounding rationale: Use() zeroes its temporary copy even on panic, but every Equal call leaks two unwiped copies.

**Suggested fix:** defer-zero both temporary slices after the comparison, mirroring Use's wipe semantics.

#### 61. [LOW] Redundant identical OR assertion; String.Equal method untested
`core/secret/secret_test.go:258` — testing — conf 0.90 — _reviewer-reported (unverified)_

The slog assertion ORs strings.Contains(logOut, redactedValue) with strings.Contains(logOut, `<redacted>`) — both operands are the identical literal, so the second arm is dead (the comment suggests an HTML-escaped form was intended). Separately, the exported String.Equal method has zero test coverage (only the unexported constantTimeEqual helper is tested), leaving its nil-receiver/nil-inner/shared-inner paths unverified.

**Suggested fix:** Assert the escaped form (<redacted>) in the second arm, and add Equal tests covering nil receivers, zeroed secrets, and self-comparison.

#### 62. [LOW] MaxKeyParts and MaxKeyTotalLen enforcement paths untested
`core/tenant/key.go:79` — testing — conf 0.85 — _reviewer-reported (unverified)_

tenant_test.go covers per-part validation and collision resistance but never exercises len(parts) > MaxKeyParts (line 67) or an assembled key exceeding MaxKeyTotalLen (line 79) — the wave-68 hostile-review cap. The total-length check is also performed after full assembly, so the bound is only verified post-hoc; a regression silently dropping either guard would pass the current suite.

**Suggested fix:** Add tests with 33 parts and with 16+ parts of 1024 bytes asserting ErrKeyInvalid.

#### 63. [LOW] MustNewID panic discards the validation failure reason
`core/tenant/tenant.go:152` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

MustNewID panics with the fixed string "tenant: MustNewID invalid input", dropping the ValidateID error that identifies which rule failed (empty, overlong, whitespace at offset N, forbidden byte at offset N). Those messages already avoid echoing input content, so including them leaks nothing and would make startup-crash diagnosis direct.

**Suggested fix:** panic(fmt.Sprintf("tenant: MustNewID: %v", err)) — the wrapped messages are already redaction-safe.

#### 64. [LOW] Doc references nonexistent symbol and stale 'only legitimate caller' claim
`core/tlsclone/tlsclone.go:39` — docs — conf 0.90 — _reviewer-reported (unverified)_

Package doc and ConfigWithFloor doc reference [AllowInsecureSkipVerify], a symbol that does not exist (the option is WithAllowInsecureSkipVerify), so the godoc links are dead. The claim that "kit-verify is the only legitimate caller" is false: infra/messaging kafkabackend, amqpbackend (unconditionally in WithReloadingTLS), and natsbackend all pass the opt-in. Reviewers relying on this doc to audit InsecureSkipVerify inheritance get a wrong picture of the blast radius.

**Suggested fix:** Fix the symbol references to [WithAllowInsecureSkipVerify] and update the caller inventory (or drop the exclusivity claim).

#### 65. [LOW] requiredNonEmpty/fieldOrder paths miss array indices and map keys
`core/validate/errors.go:121` — correctness — conf 0.75 — _reviewer-reported (unverified)_

requiredNonEmpty and fieldOrder are keyed by dotted paths without indices ("items.name"), but santhosh-tekuri InstanceLocation includes element indices/keys ("items.0.name"). For required fields inside slice or map elements, the lookup misses: the message degrades to "must be at least 1 characters" (also grammatically wrong for N=1) instead of "is required", and ordering falls back to alphabetical.

**Suggested fix:** Normalize instance paths before lookup by stripping numeric segments (and map-key segments under additionalProperties) to match the schema-side path grammar.

#### 66. [LOW] email format accepts RFC5322 display-name forms
`core/validate/formats.go:56` — security — conf 0.80 — _reviewer-reported (unverified)_

validateEmail uses mail.ParseAddress, which accepts "Bob <bob@example.com>" and comment syntax, so a field validated as "must be a valid email address" can store a full display-name address. Downstream systems treating the value as a bare address (SMTP RCPT, dedup keys, header injection surfaces) get a looser input class than the v1 go-playground `email` tag allowed.

**Suggested fix:** Reject inputs where mail.ParseAddress returns an Address whose String()/Address differs from the input (addr.Address != s), or parse and require addr.Name == "".

#### 67. [LOW] []byte fields silently ignore all jsonschema tag constraints
`core/validate/schema.go:119` — api-design — conf 0.85 — _reviewer-reported (unverified)_

The []byte branch returns Schema{Type:"string"} before applyStringConstraints runs, so min=/max=/pattern=/format= on a []byte field are silently dropped (only `required` survives via markRequiredNonEmpty in the caller). No error, no documentation of the gap.

**Suggested fix:** Call applyStringConstraints on the base64-string schema (documenting that lengths count base64 characters), or error on unsupported constraints.

#### 68. [LOW] Embedded-before-parent field shadowing leaves stale Required and duplicate PropertyOrder
`core/validate/schema.go:197` — correctness — conf 0.60 — _reviewer-reported (unverified)_

Dedup only runs in the embedded branch. If the embedded struct is declared before a parent field with the same JSON name, the embedded property is added first (including its `required` entry), then the parent field overwrites Properties but appends the name to order again. Result: duplicate PropertyOrder entry and a field marked required even when the winning (shallower) parent field is optional. doc.go describes only the opposite ordering.

**Suggested fix:** Track seen names across both loops; on shadow, drop the embedded sibling's required entry and skip the duplicate order append.

#### 69. [LOW] uuid4 tag silently collapses to generic uuid; version not enforced
`core/validate/schema.go:546` — correctness — conf 0.80 — _reviewer-reported (unverified)_

canonicalFormatName maps uuid4 → uuid, and validateUUID uses google/uuid.Parse, which accepts any UUID version plus urn:uuid: and brace-wrapped forms. Callers migrating v1 `uuid4` tags lose the version-4 constraint with no warning, and `uuid` accepts non-canonical encodings.

**Suggested fix:** Register a distinct uuid4 format that checks uuid.Version()==4, and constrain validateUUID to the canonical 36-char hyphenated form.

#### 70. [LOW] Struct acquires the shared mutex on every call just to freeze
`core/validate/validate.go:95` — performance — conf 0.70 — _reviewer-reported (unverified)_

Every Struct call locks v.mu to CAS frozen, including all calls after the first. For the package-level singleton used by httpx/typed and MCP handlers, this is a process-wide mutex acquisition on every request validation — a needless serialization point under high parallelism.

**Suggested fix:** Fast-path: if v.frozen.Load() { skip }; only take the mutex for the first transition.

#### 71. [INFO] Two divergent ID-validation policies in one package: setters accept all printable ASCII, exported helper only [A-Za-z0-9._-]
`core/contextutil/id_validation.go:21` — consistency — conf 0.65 — _reviewer-reported (unverified)_

SetRequestID/SetCorrelationID gate on isValidContextID (0x21–0x7e, any printable ASCII: ';', '"', '=', '{', '\\' all pass), while the exported IsValidCorrelationToken — used by httpx/grpcx middleware at the boundary — rejects everything outside [A-Za-z0-9._-]. A direct caller of SetRequestID can store "token=secret;x" that the kit's own boundary code would reject, and that value flows into logs/metric labels. No CRLF risk (control bytes rejected), but the dual policy is surprising and undocumented.

**Suggested fix:** Either unify the setters on isCorrelationTokenByte, or document that the setters intentionally accept a looser superset as a second line of defense.


### security — jwtutil (+revocation), apikey, asvs, csrf, mtlsidentity, netutil

_10 findings — 1 high, 3 medium, 4 low, 2 info_

#### 72. [HIGH] Rotation to a wrong-shape key silently poisons SigningProvider; no callback, no stale fallback
`security/jwtutil/signing_provider.go:649` — correctness — conf 0.82 — _verified_

refresh() imports the rotator key, sets AlgorithmKey, and Stores it WITHOUT checking the key type matches p.alg. An RSA key with ES256 alg (e.g. KMS misconfig) imports fine, so refresh returns nil, updates lastSuccessfulRefresh, fires no OnSigningRefreshError callback, and overwrites the previously-good key. Every later Sign then fails (jwt.Sign rejects the mismatch) forever; maxStale never triggers because the refresh 'succeeded'. The KeyRotator doc (lines 31-34) promises the opposite: validate at construction, fail closed via callback, retain previous key.

**Suggested fix:** In refresh(), after jwk.Import, verify the key type is compatible with p.alg before Store; return an error on mismatch so the callback fires and the prior key is retained.

#### 73. [MEDIUM] Rotate silently inherits old key's absolute ExpiresAt, producing dead/short-lived replacements
`security/apikey/manager.go:100` — api-design — conf 0.80 — _verified_

Rotate copies ExpiresAt: old.ExpiresAt (an absolute time) into the new key. The Rotate docstring (lines 81-87) only promises the new key inherits owner, kind, and scopes — it never mentions expiry. Rotating a key shortly before or after its expiry yields a replacement that is already expired or expires almost immediately, defeating the overlap window the method exists to provide. No test covers rotating an expiring key.

**Suggested fix:** Document the expiry-inheritance behavior and/or recompute the new key's ExpiresAt relative to now (e.g. preserve remaining TTL or accept an explicit ExpiresAt). Add a rotate-near-expiry test.

#### 74. [MEDIUM] ScanDir does not skip testdata directories while sibling ScanImports does
`security/asvs/scan.go:63` — consistency — conf 0.78 — _verified_

ScanDir's dir filter (line 63) skips vendor, hidden, and node_modules but NOT testdata. The sibling ScanImports (imports.go line 198) additionally skips testdata, and TestScanImports_SkipsVendorAndTestdata pins that. Consequently ScanDir will harvest `// asvs:` annotations from Go fixtures under testdata/ (golden files, example sources), polluting the documentation report with controls the service does not actually claim — the exact false-positive class the package warns about.

**Suggested fix:** Add `|| name == "testdata"` to the ScanDir dir-skip condition to match ScanImports, and add a ScanDir testdata-skip test.

#### 75. [MEDIUM] ReloadingClientTLS chain verification has no positive/negative-path test
`security/netutil/tls_reload_test.go:207` — testing — conf 0.82 — _verified_

The only test of VerifyConnection (the core mTLS server-cert verification with InsecureSkipVerify=true) checks the empty-ServerName fail-closed branch. There is no test that a chain signed by a trusted CA is accepted, nor that an untrusted-CA or wrong-hostname chain is rejected. A regression that made Verify a no-op (e.g. dropping the err check) would pass CI while silently disabling peer authentication.

**Suggested fix:** Add a handshake-level test: a leaf signed by the source's CA verifies; a leaf from a foreign CA and a hostname mismatch both fail.

#### 76. [LOW] Exported mutable KeySet.ExpectedIssuer/ExpectedAudience are a concurrency footgun with no external consumer
`security/jwtutil/jwtutil.go:70` — api-design — conf 0.60 — _low/info (unverified)_

KeySet.Verify reads the exported ExpectedIssuer/ExpectedAudience fields. Mutating them on a *KeySet shared across goroutines (the documented pattern for KeySet.Verify, vs Provider.Verify) is an unsynchronized data race that silently changes verification policy. A repo-wide grep shows no code outside jwtutil/tests sets these fields, so they add risk without serving callers; the safe path is Provider.Verify with per-provider policy.

**Suggested fix:** Consider documenting these as set-once-before-use only, or (v3 candidate) unexport them and provide functional options on ParseKeySet. BREAKING — v3 candidate for unexporting.

#### 77. [LOW] Documented construction-time key/alg compatibility check does not exist; misconfig fails per-Sign instead of fast
`security/jwtutil/signing_provider.go:31` — api-design — conf 0.85 — _verified_

KeyRotator doc claims 'The provider validates compatibility once at construction.' NewSigningProvider->refresh only validates the algorithm string (validateSigningAlg) and stores the key; it never checks the loaded key matches p.alg. A rotator returning an RSA key under the default ES256 makes NewSigningProvider succeed, then every Sign returns 'jwtutil: sign token' errors at runtime — a startup misconfig degraded into a runtime outage rather than a fail-fast constructor error.

**Suggested fix:** Validate key type vs p.alg inside refresh so the initial load (and thus the constructor) returns an error for a mismatched key shape.

#### 78. [LOW] WithSigningAllowAnyIssuer doc contradicts Sign behavior on Claims.Issuer
`security/jwtutil/signing_provider.go:210` — docs — conf 0.90 — _low/info (unverified)_

WithSigningAllowAnyIssuer doc says tokens 'omit the "iss" claim unless [Claims.Issuer] is populated by the caller.' Sign never reads claims.Issuer (lines 529-531 use only p.expectedIssuer), and the Sign doc at line 449 explicitly states caller Claims.Issuer values are ignored. So with allow-any-issuer the iss is always omitted regardless of Claims.Issuer, and two doc comments in the same file contradict each other.

**Suggested fix:** Fix the WithSigningAllowAnyIssuer comment to state iss is always omitted in allow-any mode (Claims.Issuer is ignored), matching the Sign doc and code.

#### 79. [LOW] Fuzz target asserts only no-panic, not its documented invariant
`security/netutil/ssrf_fuzz_test.go:28` — testing — conf 0.65 — _low/info (unverified)_

The doc comment promises 'every input either yields a *url.URL with non-empty scheme+hostname OR returns an error', but the fuzz body discards both return values and only checks for panics. A regression that returned a URL with an empty host and nil error (the exact 'http://' footgun parseSSRFURL exists to prevent) would not be caught by the fuzzer.

**Suggested fix:** On nil error, assert u.Scheme is http/https and u.Hostname() != "".

#### 80. [INFO] ErrSessionMismatch is effectively unreachable for legitimately-decoded tokens
`security/csrf/csrf.go:233` — correctness — conf 0.55 — _low/info (unverified)_

Verify computes prefixOK against sessionPrefix(sessionID) and returns ErrSessionMismatch when macOK && !prefixOK. Because computeMAC binds the full length-prefixed sessionID and the prefix is just sha256(sessionID)[:8], a matching MAC implies a matching prefix (absent a SHA-256/HMAC collision). The mismatch branch is dead for real tokens — the test itself acknowledges it is 'only reachable by an attacker already in possession of the secret'. Harmless but the prefix short-circuit adds code/maintenance surface with no practical effect.

**Suggested fix:** Consider dropping the redundant prefix field/check, or document it purely as defense-in-depth so future readers don't assume it provides session binding the MAC doesn't already give.

#### 81. [INFO] newFixtureProvider comment misdescribes how it builds the provider
`security/jwtutil/metrics_test.go:166` — testing — conf 0.85 — _low/info (unverified)_

The helper's comment says it 'constructs a provider with no JWKS URL via NewProviderWithKeySet' and that 'The keyset is wiped'; the body actually returns a bare &Provider{clock, maxStale} struct literal and never calls NewProviderWithKeySet nor touches a keyset. Misleading for future maintainers reasoning about which constructor invariants apply.

**Suggested fix:** Update the comment to reflect that it returns a hand-built zero-keyset Provider (bypassing the constructors), or switch to the constructor it claims to use.


### httpx — HTTP server, middleware stack, pagination, websocket, mcp, openapi

_163 findings — 14 high, 46 medium, 84 low, 19 info_

#### 82. [HIGH] Hard-enforcement reconcile refunds the full pre-charge despite real upstream spend
`httpx/budget/budget.go:329` — correctness — conf 0.65 — _reviewer-reported (unverified)_

Under EnforcementHard, when the actual-cost delta is denied (L329) or the delta charge errors (L320), reconcile refunds the entire pre-charged estimate. The upstream verifiably performed paid work (it returned an actual-cost header), so the net recorded spend is 0. A caller whose estimate is below remaining budget can loop: pre-charge OK, upstream does arbitrary paid work, delta denied, estimate refunded — unbounded real spend with zero budget depletion. Contradicts the package's own transport-error rationale; budget_test.go L510 pins this behavior.

**Suggested fix:** Retain the pre-charge (or charge all remaining headroom) on hard reject; refund nothing once an actual-cost header proves paid work occurred.

#### 83. [HIGH] HTTP scope parsing (comma-separated) contradicts gRPC interceptor and in-package docs (space-separated)
`httpx/middleware/auth/scope.go:81` — consistency — conf 0.85 — _reviewer-reported (unverified)_

hasScope splits the JWT scopes claim on commas, but grpcx/interceptor/auth.go checkScope (line ~770) parses the identical jwtutil Claims.Scopes string with strings.Fields (space-separated), and strategy.go:39 documents Identity.Scopes as "OAuth2-style space-separated" (its test uses "read write"). A multi-scope token "read write" passes gRPC RequireScopeUnary("read") but is denied 403 by HTTP RequireScope("read"), and vice versa for comma form. Fail-closed, so legit callers get spurious 403s in any mixed HTTP/gRPC service.

**Suggested fix:** Pick one grammar (OAuth2 space-separated) and accept it in both stacks; additively, make hasScope split on both comma and whitespace, and fix strategy.go/scope.go docs.

#### 84. [HIGH] X-Real-IP from any trusted peer is blindly preferred over X-Forwarded-For, enabling IP spoofing behind non-nginx proxies
`httpx/middleware/clientip/clientip.go:74` — security — conf 0.55 — _reviewer-reported (unverified)_

When RemoteAddr is in the trusted set, a syntactically valid singleton X-Real-IP short-circuits the right-to-left XFF walk. Many common ingresses (AWS ALB, Envoy, Traefik defaults) neither set nor strip X-Real-IP, so a client-supplied X-Real-IP passes through verbatim and wins. ratelimit.go:371, auditlog, and logging all key on this value — trivial rate-limit bypass and audit-log poisoning. No option exists to disable X-Real-IP trust.

**Suggested fix:** Add an additive option to select which forwarding header is trusted (default XFF-only, or cross-check X-Real-IP against the XFF chain); document the ALB/Envoy passthrough risk.

#### 85. [HIGH] Deferred finalize commits 200 OK on handler panic, defeating outer recover middleware
`httpx/middleware/compress/compress.go:173` — correctness — conf 0.85 — _reviewer-reported (unverified)_

Middleware defers cw.finalize() which, during panic unwinding, calls WriteHeader(200)/commitPassthrough on the underlying writer before the outer recover middleware (outermost in stack.Default, compress innermost per stack.go:157/225) sees the panic. recover's handlePanic then finds wroteHeader=true and only logs — the client receives a committed 200 (possibly with partial buffered body, or a cleanly-closed gzip stream) instead of a 500. Every panic under WithCompress becomes a 200.

**Suggested fix:** Only flush/commit in finalize on normal return (set a flag after next.ServeHTTP); on panic, release the encoder without writing headers or body.

#### 86. [HIGH] Write returns n > len(p) when commitCompressed flushes previously buffered bytes
`httpx/middleware/compress/writer.go:92` — correctness — conf 0.90 — _reviewer-reported (unverified)_

In modeUndecided, Write appends p to buf then returns commitCompressed(), whose n is the count for the entire buffer (prior writes + p). E.g. Write(600) then Write(600) returns (1200,nil) for a 600-byte input — violating io.Writer. io.Copy treats nw>nr as errInvalidWrite and fails; bufio.Writer accounting corrupts. Fires whenever a handler writes a small prefix then streams (fmt.Fprintf + io.Copy) across the minSize threshold.

**Suggested fix:** Return (len(p), err) from the commit path; only propagate the error from flushing the buffer, never its byte count.

#### 87. [HIGH] Spooled-body temp file never closed: net/http does not close a replaced r.Body
`httpx/middleware/signedrequest/signedrequest.go:418` — resource-leak — conf 0.75 — _reviewer-reported (unverified)_

On success verify() sets r.Body = spooled.Body(), commenting "net/http closes r.Body for us". That is false: http.Server captures reqBody := req.Body before the handler runs and closes only that original in finishRequest. The spooledReader's *os.File (bodies > inMemoryBodyMax, default 64KiB) is closed only by GC finalizer — fd-exhaustion risk under load on Unix; on Windows the create-time os.Remove fails on an open file, so orphaned temp files accumulate permanently.

**Suggested fix:** In the Middleware wrapper, capture the installed body and defer Close after next.ServeHTTP returns (additive internal fix); delete the wrong comment.

#### 88. [HIGH] Span never renamed to route pattern: r.Pattern read from original request, mux mutates the copy
`httpx/middleware/tracing/tracing.go:69` — correctness — conf 0.85 — _reviewer-reported (unverified)_

Line 63 passes r.WithContext(ctx) (a shallow copy) to next; ServeMux sets Pattern on the request pointer it receives (server.go: `h, r.Pattern, ... = mux.findHandler(r)`). finishHTTPSpan reads the original r.Pattern, which is never updated when the middleware wraps the router — the documented placement. Spans stay named just "GET". Sibling metrics middleware passes the same pointer and works. The test masks this by presetting req.Pattern before ServeHTTP instead of routing through a real ServeMux.

**Suggested fix:** Capture r2 := r.WithContext(ctx), pass r2 to next, and read r2.Pattern in finishHTTPSpan. Add a test dispatching through a real http.ServeMux.

#### 89. [HIGH] Options.ErrorMapper is dead API: errorMapperMiddleware discards the mapper and returns next unchanged
`httpx/openapi/openapi.go:111` — api-design — conf 0.93 — _reviewer-reported (unverified)_

Mount's Options.ErrorMapper is documented as translating strict-server errors into Problem Details, and the package doc claims Mount applies "error-translation middleware (apperror → RFC 7807)". errorMapperMiddleware ignores its argument and returns next, so setting ErrorMapper (or relying on DefaultErrorMapper) has zero effect — Mount never invokes either. No test exercises the option. Consumers wiring a custom mapper silently get nothing.

**Suggested fix:** Wire the mapper (e.g. expose a StrictHTTPMiddleware/ResponseErrorHandler for NewStrictHandler) or deprecate the field with explicit no-op docs; correct package docs. Removing the field is BREAKING — v3 candidate.

#### 90. [HIGH] Handle mounts dead mux route for non-uppercase methods while spec registers successfully
`httpx/openapigen/handle.go:69` — correctness — conf 0.75 — _reviewer-reported (unverified)_

Spec.Register normalizes the verb (documented "case-insensitive"), but mux.Handle uses the raw caller string: Handle("post", "/widgets", ...) registers pattern "post /widgets". stdlib parsePattern accepts any token as an extension method, and ServeMux matches methods exactly ("the method must match exactly"), so real POST requests never match — silent 405/404 outage with a fully documented spec entry. Same applies to all five Handle* helpers.

**Suggested fix:** Use the normalized method for the mux pattern too (expose normaliseMethod result or re-normalize in handle.go); add a test that dispatches a lowercase-registered route through the mux.

#### 91. [HIGH] mapStatus drifts from httpx.HTTPStatus: TIMEOUT and PAYLOAD_TOO_LARGE map to 500
`httpx/problemdetails/problem.go:332` — correctness — conf 0.88 — _reviewer-reported (unverified)_

mapStatus claims to mirror httpx.HTTPStatus (line 301) but lacks cases for apperror.CodeTimeout (408 in httpx/apperror_status.go:22) and CodePayloadTooLarge (413 at line 23). Both fall through to 500, turning client-class errors into server errors in problem+json responses and breaking retry semantics. Same drift class as the wave-68 CodeStorageFull fix. SafeDetail also lacks branches for these codes. No tests cover either code.

**Suggested fix:** Add CodeTimeout->408 and CodePayloadTooLarge->413 cases (and SafeDetail strings); add a test iterating all apperror codes asserting parity with httpx.HTTPStatus.

#### 92. [HIGH] WithCBShouldTrip cannot exclude transport errors from tripping the breaker
`httpx/resilient.go:226` — correctness — conf 0.80 — _reviewer-reported (unverified)_

In circuitBreakerTransport.RoundTrip, when shouldTrip(resp, rtErr) returns false but rtErr != nil, the closure still returns rtErr. The breaker is built with WithIsSuccessful(err == nil) (resilient.go:182), so every non-nil transport error counts as a failure regardless of the predicate. A predicate excluding e.g. context.Canceled or DNS errors silently does nothing, contradicting the WithCBShouldTrip doc ('deciding whether a response/error should count toward the failure threshold'). Verified gobreaker returns closure errors unwrapped.

**Suggested fix:** When shouldTrip is false and rtErr != nil, wrap rtErr in a non-counting sentinel, treat it as success in IsSuccessful, and unwrap before returning to the caller.

#### 93. [HIGH] Dispatcher posts to caller-supplied URLs with no SSRF defenses
`httpx/webhook/webhook.go:177` — security — conf 0.70 — _reviewer-reported (unverified)_

Delivery.URL (per docs, customer-supplied endpoints) is dispatched verbatim: no scheme allowlist, no private/link-local IP blocking, and the kit's own security/netutil.SSRFSafeTransport is neither used nor mentioned. Default http.Client redirect-following lets a malicious endpoint bounce the signed POST (with X-Kit-* headers) to internal/metadata addresses. doc.go contains no SSRF warning despite the kit's ASVS posture elsewhere.

**Suggested fix:** Validate scheme is http/https in Send; document SSRF risk and wire security/netutil.SSRFSafeTransport (additive option, e.g. WithSSRFProtection) for customer-URL deployments.

#### 94. [HIGH] Per-connection ctx is never cancelled on connection close, contradicting documented contract
`httpx/websocket/handler.go:89` — api-design — conf 0.80 — _reviewer-reported (unverified)_

doc.go, conn.go (Context()), and options.go (HandlerFunc) all claim ctx 'is cancelled when the connection closes for any reason (peer close, server shutdown, idle timeout, panic)'. In code, cancel() only runs after the user handler returns, and r.Context() is detached via WithoutCancel. Conn.Close, heartbeat ping-timeout close, and peer disconnect do not cancel ctx. A handler that blocks on <-ctx.Done() (or passes ctx to downstream RPCs expecting disconnect cancellation) hangs/leaks: the cancellation it waits for only fires once it has already returned.

**Suggested fix:** Store the CancelFunc in Conn; invoke it in Close and in recordCloseFromError, so ctx actually cancels on connection death. Or fix all three doc claims.

#### 95. [HIGH] Heartbeat ping requires a concurrently-reading handler; push-only handlers get killed
`httpx/websocket/heartbeat.go:48` — correctness — conf 0.70 — _reviewer-reported (unverified)_

coder/websocket v1.8.14 documents: 'Ping must be called concurrently with Reader as it does not read from the connection but instead waits for a Reader call to read the pong.' A handler that only writes (server-push pattern — exactly where keepalives are wanted) never pumps the pong, so every ping times out and runHeartbeat closes each connection with StatusPolicyViolation after interval+pongTimeout. Neither WithPingInterval nor doc.go mentions the read requirement; heartbeat tests use a fake conn whose Ping returns instantly, so the constraint is untested.

**Suggested fix:** Document the must-be-reading requirement on WithPingInterval/doc.go, or internally drive a reader (e.g. coderws CloseRead-style pump) for handlers that do not read.

#### 96. [MEDIUM] RoundTrip early-error paths never close req.Body (RoundTripper contract violation)
`httpx/budget/budget.go:241` — resource-leak — conf 0.75 — _reviewer-reported (unverified)_

net/http documents that RoundTrip must always close the request Body, including on errors, and http.Client.do relies on it ("c.send() always closes req.Body"). The wrapper returns at L232 (nil req), L238 (pre-charge error), and L241 (ErrBudgetExceeded) without calling base.RoundTrip or closing req.Body. Bodies backed by files/pipes leak. Sibling wrappers (httpx/sign sign.go:298, circuitBreakerTransport on open circuit) share the same defect — systemic pattern.

**Suggested fix:** On every return path that skips base.RoundTrip, close req.Body if non-nil (e.g. defer-guarded helper). Apply the same fix to sibling transports.

#### 97. [MEDIUM] Estimate of 0 bypasses the budget gate entirely
`httpx/budget/budget.go:274` — correctness — conf 0.70 — _reviewer-reported (unverified)_

estimate() accepts n==0 from the header (only err!=nil || n<0 falls back), and WithDefaultAmount(0) is allowed. Budget.Consume documents amount==0 as a no-op probe; memory backend returns allowed=true even when remaining==0 (verified data/budget/memory/memory.go L292). So a request carrying "X-Estimated-Tokens: 0" (or default 0) is always admitted even with an exhausted budget — free upstream calls when no actual header is configured or in audit-only mode.

**Suggested fix:** Treat n <= 0 in estimate() as fallback-to-default, and either reject WithDefaultAmount(0) or document the reconcile-only mode explicitly.

#### 98. [MEDIUM] WriteJSON marshal failure is silent, contradicting its own doc
`httpx/httpx.go:363` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

The doc (lines 348-349) says 'The error is also logged at Warn level via the request-scoped logger; most handlers can ignore the return value.' But the marshal-error branch writes the 500 body and returns err WITHOUT logging — only the two socket-write branches log. All typed handlers discard the return (`_ = WriteJSON`, typed.go:36/75/93), so a response that fails to marshal (e.g. NaN/Inf float field, chan in an interface) produces a 500 INTERNAL with zero log trace in production.

**Suggested fix:** Add logger.Warn("httpx: response marshal failed", redact.Error(err)) in the marshal-error branch before returning.

#### 99. [MEDIUM] HTTP/2 frame-size test does not exercise the kit's HTTP/2 pins
`httpx/httpx_test.go:807` — testing — conf 0.85 — _reviewer-reported (unverified)_

TestNewServer_HTTP2FrameSizeViolationClosesConnection serves via a freshly constructed default `&http2.Server{}` — not the kit-configured one — so MaxReadFrameSize/MaxConcurrentStreams pins from NewServer are unused (ConfigureServer attaches them to srv.TLSNextProto, which ServeConn bypasses; BaseConfig only supplies handler/timeouts). The test sends no oversized frame and closes no connection; it only asserts ProtoMajor==2 on a default server. The G-03 hardening claimed by docs/THREAT_MODEL is effectively untested, under a name implying it is.

**Suggested fix:** Rename to reflect actual coverage, or drive the kit-pinned server over TLS and assert advertised SETTINGS MaxFrameSize == 1MiB.

#### 100. [MEDIUM] Audit Reason not sanitised for NUL/invalid UTF-8 — entry rejected, executed-call audit lost
`httpx/mcp/actionlog.go:133` — correctness — conf 0.65 — _reviewer-reported (unverified)_

truncateReason only fixes the trailing rune boundary. actionlog validate() (data/actionlog/actionlog.go:864) rejects Reason containing \x00 or invalid UTF-8 anywhere. A handler error embedding such bytes (e.g. wrapping raw caller input) makes Append fail for every such failing call: in strict sync mode the caller gets "internal error" instead of the mapped message and the failure entry is silently lost despite the tool having executed.

**Suggested fix:** Sanitise reason before building the entry: strings.ToValidUTF8 plus stripping NUL, then truncate.

#### 101. [MEDIUM] Register panics via SDK AddTool for non-object schemas instead of returning error
`httpx/mcp/mcp.go:680` — api-design — conf 0.85 — _reviewer-reported (unverified)_

SDK v1.6.1 Server.AddTool panics unless the schema's "type" is exactly "object". validate.SchemaFor emits {"type":"string"} etc. for non-struct In/Out (string, int, slice, time.Time, json.RawMessage), and validateSchemaOverride accepts overrides lacking a "type" key (e.g. {} or {"type":123}). All these panic in AddTool — after the name was already reserved in s.toolMeta/s.tools, leaving the kit catalog inconsistent with the SDK. Register's doc promises error returns for unsupported types.

**Suggested fix:** Validate generated/override schemas have type "object" before reserving the slot; return an error otherwise. Additive fix, no API break.

#### 102. [MEDIUM] Tool handler panics bypass the strict-audit invariant
`httpx/mcp/mcp.go:946` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

wrapToolHandler recovers panics in tenant/actor extractors and async audit jobs, but h(ctx, in) is called bare. SDK v1.6.1 has no recover in its dispatch path (verified: no recover() in non-test mcp sources), so a panicking handler unwinds to net/http after side effects may have occurred — no failure actionlog entry is written, breaking the documented "every executed tool call produces a signed entry" strict-audit guarantee.

**Suggested fix:** Wrap h(ctx, in) with recover(): record an Outcome=failure audit entry with a generic reason, log the panic, return errorResult("internal error").

#### 103. [MEDIUM] safeAuditIPAddress accepts control characters hidden in an IPv6 zone
`httpx/middleware/auditlog/safe.go:82` — security — conf 0.70 — _reviewer-reported (unverified)_

Validation is only len<=64 + utf8.ValidString + netip.ParseAddr. Go's netip accepts any non-empty zone after '%' with zero character validation (netip.go parseAddr only checks zone non-empty), so "fe80::1%a\nb" passes and is stored verbatim in Event.IPAddress; the store (observability/auditlog.go:157) also checks only length/utf8 for IPAddress. This defeats the sanitizer's documented intent (test rejects "203.0.113.10\n") and enables audit-log injection via WithClientIPFunc resolvers that echo header data.

**Suggested fix:** Reject addresses where Addr.Zone() != "" (or strip zone), or additionally run the control/space scan used by isSafeAuditToken over the IP string.

#### 104. [MEDIUM] NewAPIKeyAuthenticator discards verifier error cause — infra outages become 401 invalid credentials
`httpx/middleware/auth/strategy.go:283` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

Any error from v.VerifyAPIKey (DB outage, context canceled, timeout) is replaced by bare ErrInvalidCredentials: no %w wrapping, no logging. Strategy's comment (line 104-109) says "the strategy is responsible for logging the underlying cause", but this kit-provided strategy doesn't, so a backend outage is indistinguishable from forged keys on the wire AND in logs, and Chain stops. Contrast: the budget middleware surfaces backend errors as 503.

**Suggested fix:** Wrap: fmt.Errorf("%w: %w", ErrInvalidCredentials, err) (Strategy behavior unchanged via errors.Is) and/or log via httpx.Logger; document the flattening.

#### 105. [MEDIUM] statusRecorder drops Flusher/Hijacker/Pusher/ReaderFrom, breaking SSE flush and WebSocket upgrades via direct type assertion
`httpx/middleware/circuitbreaker/circuitbreaker.go:242` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Unlike sibling middleware.ResponseRecorder (which forwards Flush/Hijack/Push/ReadFrom precisely because 'gorilla/websocket asserts http.Hijacker'), this wrapper only adds Unwrap. Handlers behind the breaker doing w.(http.Flusher) (typical SSE) or w.(http.Hijacker) silently lose streaming/upgrade capability; only http.ResponseController paths survive via Unwrap. ReadFrom loss also disables the sendfile fast path on every wrapped request.

**Suggested fix:** Mirror ResponseRecorder's Flush/Hijack/Push/ReadFrom passthroughs on statusRecorder (the import-cycle excuse doesn't prevent re-implementing four small methods).

#### 106. [MEDIUM] 1xx informational status latches wroteHead, swallowing the real final WriteHeader
`httpx/middleware/circuitbreaker/circuitbreaker.go:252` — correctness — conf 0.60 — _reviewer-reported (unverified)_

WriteHeader(103) (Early Hints) sets wroteHead=true and forwards 103; the handler's subsequent WriteHeader(500/404) is silently dropped, so net/http emits an implicit 200 on first body write — wrong final status to the client — and shouldTrip evaluates status 103, so genuine 5xx failures after Early Hints never count against the breaker. compress.compressWriter (writer.go:54) and the parent middleware.ResponseRecorder share the same flaw.

**Suggested fix:** For codes in [100,199], forward to the underlying writer without setting wroteHead/status, matching net/http's informational-response semantics.

#### 107. [MEDIUM] XFF entries containing ports are skipped as garbage, breaking Azure AGW-style proxies and continuing into client-controlled entries
`httpx/middleware/clientip/clientip.go:98` — security — conf 0.60 — _reviewer-reported (unverified)_

net.ParseIP fails on 'ip:port' forms that some proxies (Azure Application Gateway, IIS/ARR) append to X-Forwarded-For. The loop 'continue's past the unparseable real-client entry and may return an earlier, fully attacker-supplied entry (e.g. client sends 'XFF: 6.6.6.6', proxy appends '203.0.113.5:35123' → returns 6.6.6.6) or falls back to RemoteAddr. Skipping unparseable hops instead of failing closed walks into untrusted territory.

**Suggested fix:** Try net.SplitHostPort on each candidate before ParseIP; on a still-unparseable entry, return RemoteAddr (fail closed) instead of continuing left.

#### 108. [MEDIUM] WithLogger is dead: cfg.logger is never used, documented diagnostics don't exist
`httpx/middleware/compress/compress.go:115` — docs — conf 0.90 — _reviewer-reported (unverified)_

WithLogger docs promise warn-level diagnostics for 'oversized prefix bypass, handler panics during compression'. grep shows cfg.logger is only assigned (compress.go:116,148-149) and never referenced in writer.go or anywhere else — no log statement exists in the package. The buffer-ceiling bypass and panic paths are silent. Also inconsistent with sibling circuitbreaker.WithLogger, which panics on nil; this one silently accepts nil.

**Suggested fix:** Either emit the documented warn logs in the bail/finalize paths or remove the option's claims; align nil-handling with sibling middlewares.

#### 109. [MEDIUM] Single write larger than maxBuffer is served uncompressed despite exceeding minSize
`httpx/middleware/compress/writer.go:82` — performance — conf 0.85 — _reviewer-reported (unverified)_

The maxBuffer bail (buf.Len()+len(p) > maxBuffer → commitPassthrough) is checked before the minSize check. With defaults (minSize 1KiB, maxBuffer 256KiB) the buffer never exceeds ~1KiB across writes, so the only trigger is one write > ~256KiB — e.g. json.Marshal of a large payload written once — which is exactly the response that should compress. It is sent uncompressed; no need to buffer since size is already known to exceed minSize. Untested.

**Suggested fix:** Check buf.Len()+len(p) >= minSize first and commitCompressed, streaming p through the encoder without buffering; keep the maxBuffer bail only for minSize > maxBuffer configs.

#### 110. [MEDIUM] Session-bound mode 403s safe methods for anonymous users, contradicting WithSessionExtractor docs
`httpx/middleware/csrf/csrf.go:546` — correctness — conf 0.80 — _reviewer-reported (unverified)_

sessionBoundMiddleware enforces 'session required' (lines 545-549) before the safe-method exemption (line 590). WithSessionExtractor docs say a non-empty session is required 'for every authenticated state-changing request', but GET/HEAD/OPTIONS from anonymous users are also 403'd. A consumer mounting this globally (the normal CSRF pattern) blocks every anonymous page load. Only the extractor-panic path is tested (csrf_test.go:1287); the plain empty-session GET case has no test asserting intent.

**Suggested fix:** Pass safe-method requests through without issuing a cookie when session is empty (changes behavior — gate behind option if needed), or fix WithSessionExtractor docs to state ALL requests require a session.

#### 111. [MEDIUM] WithSkipCheck never bypasses the session-required gate in session-bound mode
`httpx/middleware/csrf/csrf.go:604` — api-design — conf 0.80 — _reviewer-reported (unverified)_

The skip predicate is evaluated at line 604, after the session check at 545-549. WithSkipCheck docs promise 'If skip returns true for a request, CSRF token validation is skipped' with bearer/API-key clients as the documented use case. Combining WithSessionExtractor with WithSkipCheck(HasBearerToken) — a plausible mixed browser+API service config — 403s every header-authenticated request lacking a session with 'csrf: session required', silently breaking the documented skip contract.

**Suggested fix:** Evaluate skipCheck (after the origin allowlist) before requiring a session in sessionBoundMiddleware, or document that session extraction overrides skip predicates.

#### 112. [MEDIUM] Package doc references nonexistent identifiers and misstates middleware behavior
`httpx/middleware/csrf/doc.go:12` — docs — conf 0.95 — _reviewer-reported (unverified)_

doc.go links [Middleware], [RequireJSON], and [WithTTL] — none exist; actual names are New, RequireJSONContentType, WithSessionTTL, so godoc links are broken. It also claims the cookie is 'Secure outside loopback' (code defaults Secure unconditionally; no loopback detection) and that the default flow matches header to cookie 'under an HMAC binding to the session' (session binding only exists with WithSessionExtractor). Hostile-review verified against csrf.go: all three claims are false.

**Suggested fix:** Rewrite doc.go to reference New/RequireJSONContentType/WithSessionTTL and accurately describe the Secure default and the optional, not default, session binding.

#### 113. [MEDIUM] Session-bound flow untested with WithAllowedOrigins, WithSkipCheck, and TTL expiry
`httpx/middleware/csrf/sessionbound_test.go:20` — testing — conf 0.75 — _reviewer-reported (unverified)_

No test combines WithSessionExtractor with WithAllowedOrigins or WithSkipCheck, so the security-relevant ordering at csrf.go:599-607 (origin before skip, both after session gate) is entirely unverified — unlike the legacy flow, which has dedicated ordering tests (TestCSRF_OriginCheckBeforeSkipCheck). WithSessionTTL is exercised only by panic-validation tests; no test drives token expiry through the reissue+403 path (csrf.go:556, 609-612), despite securitycsrf.WithClock existing for exactly this.

**Suggested fix:** Add session-bound tests for origin-allowlist rejection, skip-predicate behavior, and TTL expiry via a fake clock injected through the issuer.

#### 114. [MEDIUM] http.MaxBytesError from upstream maxbody middleware yields 400 instead of 413
`httpx/middleware/idempotency/idempotency.go:439` — correctness — conf 0.75 — _reviewer-reported (unverified)_

The kit's golden path installs maxbody.MaxBodySize on every public mux. When that cap is below 1 MiB, readAndFingerprintBody's io.ReadAll returns *http.MaxBytesError, which matches neither errBodyTooLarge nor errInvalidFingerprintHeader, so the middleware increments the errors counter (documented as "store errors (500)") and returns 400 "could not read request body" instead of 413. Client-disconnect read errors are also miscounted as store errors.

**Suggested fix:** errors.As(*http.MaxBytesError) in the fingerprint error branch and return 413; don't bump the store-errors counter for client read failures.

#### 115. [MEDIUM] store.Get and store.TryLock errors are never logged despite WithLogger contract
`httpx/middleware/idempotency/idempotency.go:448` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

WithLogger's doc says it "sets the logger for idempotency store errors", but the Get error path (line 448-455) and TryLock error path (line 472-479) return a generic 500 and bump the errors counter without ever calling cfg.logger — the underlying error is discarded entirely. Operators see 500s and errors_total with no way to diagnose the cause; only Unlock/Set failures are logged.

**Suggested fix:** Log Get/TryLock errors via cfg.logger.Error with redact.Error before writing the 500 response.

#### 116. [MEDIUM] Processing lock TTL equals cache TTL (default 24h) — crash locks key out for a day
`httpx/middleware/idempotency/idempotency.go:472` — api-design — conf 0.75 — _reviewer-reported (unverified)_

TryLock is called with cfg.ttl, the response-cache TTL (default 24h). The deferred Unlock only runs on panic; a hard crash (kill -9, OOM, node loss) mid-handler leaves the lock held, so every retry of that key gets 409 "request already in progress" until the full TTL expires. Stripe-style designs use a short processing-lock TTL separate from the response TTL. No option exists to configure a shorter lock TTL.

**Suggested fix:** Add additive WithLockTTL option (short, e.g. 30-60s) passed to TryLock, distinct from the response cache TTL passed to Set.

#### 117. [MEDIUM] Cached headers diverge from headers actually sent to first caller
`httpx/middleware/idempotency/idempotency.go:533` — correctness — conf 0.85 — _reviewer-reported (unverified)_

responseCapture only copies capturedHeaders to the real writer inside WriteHeader (line 789). A handler that sets headers via w.Header() but returns without calling Write/WriteHeader sends an implicit 200 with NO custom headers to the first caller, yet the post-handler snapshot (line 533) caches those headers — replays include headers the original response never had. Headers added after WriteHeader are likewise cached but never sent.

**Suggested fix:** After ServeHTTP, if !rec.wroteHeader copy capturedHeaders to the underlying writer; snapshot headers at WriteHeader time for the cache.

#### 118. [MEDIUM] 5xx responses are cached and replayed for the full TTL with no opt-out
`httpx/middleware/idempotency/idempotency.go:549` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Every handler status, including 500/502/503 from transient backend failures, is stored via store.Set and replayed for up to 24h. A client that retries with the same Idempotency-Key (the documented correct behavior) can never recover from a transient error during the TTL window. There is no option to skip caching selected statuses, and no test covers 5xx replay semantics.

**Suggested fix:** Add additive WithUncachedStatuses/WithCachePredicate option (unlock instead of Set for matching statuses); document the current replay-errors semantics explicitly.

#### 119. [MEDIUM] doc.go claims label `path` but the code emits `route` — dashboard-pinning rationale is stale
`httpx/middleware/metrics/doc.go:3` — docs — conf 0.90 — _reviewer-reported (unverified)_

doc.go says the package emits http_requests_total{method,path,status} and positions redmetrics as the variant that "spells the route label `route`". The code (metrics.go lines 71, 81) has used "route" since wave 85 (commit f787c5b4 renamed path→route). The package's stated reason to exist — preserving v1 label names for pinned dashboards — is contradicted by its own labels, misleading anyone authoring or migrating dashboards.

**Suggested fix:** Rewrite doc.go to state the actual labels {method,route,status} and drop/correct the path-pinning claim.

#### 120. [MEDIUM] KeyedLimiter published to scrape collector before shards initialized — data race
`httpx/middleware/ratelimit/keyed.go:117` — concurrency — conf 0.80 — _reviewer-reported (unverified)_

WithKeyedMetrics (keyed.go:85) calls m.trackKeyedLimiter(rl) during the option loop, publishing rl to keyedActiveKeysCollector. Shard LRUs (keyed.go:117-120) and possibly rl.name are written AFTER publication without holding shard mutexes. A concurrent Prometheus scrape's Collect (metrics.go:147-153) reads s.entries under s.mu, but the constructor's write is unsynchronized — no happens-before. The nil guard in Collect shows awareness but does not fix the race.

**Suggested fix:** Initialize shards (and apply name) before running options in NewKeyedLimiter, or defer trackKeyedLimiter until construction completes.

#### 121. [MEDIUM] KeyedLimiter has the same Stop-before-Start latch / goroutine-leak defect as Limiter
`httpx/middleware/ratelimit/keyed.go:265` — concurrency — conf 0.80 — _reviewer-reported (unverified)_

Identical pattern to ratelimit.go: Stop before Start sets stopped=true; Start (line 221) ignores the stopped flag and launches the cleanup loop; the next Stop short-circuits on `!rl.started || rl.stopped` without cancelling or waiting, leaving the ticker goroutine running until the Start context is cancelled. Contradicts the doc claim that Stop 'cancels the cleanup goroutine ... and waits for it to exit'.

**Suggested fix:** Reject Start when stopped is already set, mirroring lifecycle.FuncComponent.

#### 122. [MEDIUM] Stop-before-Start latches stopped; subsequent Start leaks an uncancellable cleanup goroutine
`httpx/middleware/ratelimit/ratelimit.go:332` — concurrency — conf 0.80 — _reviewer-reported (unverified)_

Stop with started=false sets stopped=true and returns. Start (line 287) checks only rl.started, so a later Start launches the cleanup loop. Any subsequent Stop hits `!rl.started || rl.stopped` (stopped already true) and returns immediately — never cancels or waits. The goroutine runs until the Start ctx ends. Sibling lifecycle.FuncComponent (runtime/lifecycle/component.go:102) explicitly rejects Start after Stop, showing this divergence is unintended.

**Suggested fix:** In Start, return an error when rl.stopped is true, matching FuncComponent's Start-after-Stop rejection.

#### 123. [MEDIUM] Limiter.Stop and KeyedLimiter.Stop are completely untested
`httpx/middleware/ratelimit/ratelimit_test.go:267` — testing — conf 0.90 — _reviewer-reported (unverified)_

No test in the package calls .Stop( (verified by grep across all _test.go). Stop carries non-trivial branches: nil receiver, Stop before Start, idempotent re-Stop, nil cancel/doneCh, nil ctx (blocking wait), and ctx-timeout returning ctx.Err(). The cancel-and-wait contract claimed in the doc comment is never exercised, which is exactly why the Stop/Start latch defect went unnoticed.

**Suggested fix:** Add tests for Stop after Start (waits for goroutine), idempotent Stop, Stop-before-Start, ctx-timeout path, and the Stop-then-Start sequence.

#### 124. [MEDIUM] Flush does not mark wroteHeader — flush-then-panic corrupts an already-started response with the 500 JSON body
`httpx/middleware/recover/recover.go:303` — correctness — conf 0.75 — _reviewer-reported (unverified)_

recordingWriter.Flush delegates without setting wroteHeader, yet net/http's Flush implicitly sends WriteHeader(200) if headers are unsent. A streaming/SSE handler that flushes then panics makes handlePanic (line 193) see wroteHeader=false, so it calls WriteHeader(500) (superfluous, no-op) and appends the INTERNAL JSON body to the live 200 stream, instead of taking the log-only 'response started' path. Same hole via http.ResponseController which reaches the inner writer through Unwrap.

**Suggested fix:** Set wroteHeader=true (statusCode 200) in Flush, and implement FlushError so ResponseController flushes are also recorded.

#### 125. [MEDIUM] WriteHeader latches on 1xx interim responses, swallowing the final status
`httpx/middleware/response_recorder.go:45` — correctness — conf 0.80 — _reviewer-reported (unverified)_

net/http allows WriteHeader(103) (Early Hints) followed by a final WriteHeader. ResponseRecorder sets wroteHeader=true on the first call, so the final WriteHeader(201/204/...) is silently dropped — never delegated to the underlying writer — and the wire status becomes an implicit 200 on first Write. Status() then reports 103. metrics.go:118 and logging.go use rec.Status() for labels/logs, so 1xx-using handlers get wrong wire statuses and wrong metrics.

**Suggested fix:** In WriteHeader, delegate 100–199 codes to the underlying writer and return without setting wroteHeader/statusCode, matching net/http semantics.

#### 126. [MEDIUM] Nonce-store backend failures are unlogged and metric-labeled as bad_signature
`httpx/middleware/signedrequest/metrics.go:117` — error-handling — conf 0.65 — _reviewer-reported (unverified)_

verify() wraps store errors (e.g. Redis outage) and Middleware only does observeVerifyFailure + writeError — the package has no logging, so the wrapped cause is dropped entirely. classifyVerifyFailure's default branch counts these server-side dependency failures as "bad_signature", a client-attributed label. A Redis outage therefore presents as a forged-signature attack spike with 500s and no diagnostic anywhere; ErrBodyTooLarge (413) is similarly counted as "malformed_signature".

**Suggested fix:** Add a store_error (and body_too_large) reason label, and log nonce-store/internal errors server-side at error level.

#### 127. [MEDIUM] timeoutWriter WriteHeader semantics diverge from http.ResponseWriter contract
`httpx/middleware/timeout/writer.go:62` — correctness — conf 0.75 — _reviewer-reported (unverified)_

Stdlib: first WriteHeader wins, Write implies WriteHeader(200), later WriteHeader calls are superfluous no-ops, and 1xx codes are sent immediately without becoming the final status. Here Write never latches a status, so WriteHeader after Write (or a second WriteHeader) silently overwrites tw.code and changes the flushed status; WriteHeader(100/103) is buffered and emitted as the FINAL status, producing a corrupt response. Handlers behave differently inside vs outside this middleware.

**Suggested fix:** Latch the status on first WriteHeader/Write (ignore later calls, mirroring http.TimeoutHandler) and ignore or pass through 1xx informational codes.

#### 128. [MEDIUM] url.scheme attribute is always empty for server-side requests
`httpx/middleware/tracing/tracing.go:45` — correctness — conf 0.80 — _reviewer-reported (unverified)_

semconv.URLScheme(r.URL.Scheme) is recorded on every server span, but for server requests Go leaves URL.Scheme empty (only absolute-form/proxy requests populate it). Every production span carries url.scheme="" — misleading telemetry that violates the semconv requirement that the attribute reflect the actual scheme.

**Suggested fix:** Derive scheme from r.TLS != nil ("https"/"http"), or honor X-Forwarded-Proto behind trusted proxies; omit the attribute when unknown.

#### 129. [MEDIUM] hasResponseOption ignores responseExtraContent: WithResponseContentT-only callers get spurious default 200 response
`httpx/openapigen/handle.go:206` — correctness — conf 0.85 — _reviewer-reported (unverified)_

hasResponseOption checks only probe.responseSchemas and probe.responseDescriptions. A caller supplying only WithResponseContentT/WithResponseContent (e.g. 201 + application/problem+json) supplies a response body schema — the function's documented detection target — yet returns false, so Handle/HandleStatus inject WithResponseType[Resp](200), and the spec documents a phantom 200 response alongside the intended one.

**Suggested fix:** Also check len(probe.responseExtraContent) > 0 in hasResponseOption.

#### 130. [MEDIUM] CursorSigner has no domain separation; signed cursors are replayable across endpoints
`httpx/pagination/cursor.go:234` — security — conf 0.55 — _reviewer-reported (unverified)_

Encode HMACs only the payload. The docs tell operators to share one secret across all replicas, so a cursor legitimately signed for endpoint A (/users) verifies on endpoint B (/orders) using the same signer, feeding a foreign table's PK into B's ListFn. This partially re-opens the forgery/enumeration path the signer exists to close: any ID the attacker can get signed anywhere becomes a valid cursor everywhere the secret is shared.

**Suggested fix:** Add additive scope binding: e.g. CursorListOpts.SignerScope or NewCursorSignerWithContext(label) mixing an endpoint label into the HMAC input.

#### 131. [MEDIUM] Unknown-total 'next' link is NOT conditioned on the page being full, contrary to docs
`httpx/pagination/link_header.go:44` — docs — conf 0.85 — _reviewer-reported (unverified)_

Doc (line 19-20) and inline comment (line 44-45) claim that with negative total, next is emitted "conditioned on the current page being full". The function has no item-count parameter, so the condition is unimplementable: nextPageOffset only checks integer overflow, and next is always emitted (TestWriteLinkHeader_unknownTotalEmitsOnlyNext confirms). Clients relying on the documented heuristic to detect the end of a streaming list will follow next links forever.

**Suggested fix:** Fix the docs to state next is always emitted when total is unknown, or add an additive variant taking the current page's item count.

#### 132. [MEDIUM] ParsePathID accepts non-canonical UUID forms and returns them verbatim
`httpx/request.go:32` — api-design — conf 0.60 — _reviewer-reported (unverified)_

uuid.Parse accepts 'urn:uuid:...' prefixes, braced '{...}', raw 32-hex, and uppercase forms. ParsePathID returns the raw path string, not the canonical form, so many distinct strings validate and address the same logical resource. Downstream string-keyed lookups, caches, audit logs, and TEXT-column DB queries treat 'ABC...' / 'urn:uuid:abc...' / 'abc...' as different keys — enabling cache-key splitting and duplicate-identity bugs despite the function's purpose being ID validation.

**Suggested fix:** Return the canonical form (parsed UUID string) instead of raw, or reject non-canonical inputs; document whichever contract is chosen.

#### 133. [MEDIUM] Default shouldTrip counts caller cancellations as breaker failures
`httpx/resilient.go:158` — security — conf 0.60 — _reviewer-reported (unverified)_

The default predicate returns true for any non-nil error, including context.Canceled propagated from inbound request contexts. cbThreshold defaults to 5, cbReset to 30s: five quickly-cancelled requests (impatient or malicious clients resetting connections) open the breaker and fail all traffic to a healthy downstream with ErrCircuitOpen for 30s. Because of the shouldTrip finding above, this cannot be fixed by callers via WithCBShouldTrip either.

**Suggested fix:** Exclude context.Canceled (caller-driven) from default failure counting, or document the availability risk and make exclusion possible once shouldTrip is fixed.

#### 134. [MEDIUM] req.Body never closed when circuit is open (RoundTripper contract violation)
`httpx/resilient.go:221` — resource-leak — conf 0.75 — _reviewer-reported (unverified)_

When cb.Execute returns ErrCircuitOpen (or half-open ErrTooManyRequests) the wrapped fn never runs, base.RoundTrip is never called, and RoundTrip returns the error without closing req.Body. http.RoundTripper requires implementations to always close the request body, including on errors; http.Client does not close it on transport error. Streaming bodies (io.Pipe, files, on-the-fly encoders) leak or block writers for every request rejected by an open breaker.

**Suggested fix:** If cb.Execute fails without invoking the closure (track via a bool), close req.Body before returning the error.

#### 135. [MEDIUM] circuitBreakerTransport.RoundTrip has zero behavioral test coverage
`httpx/resilient.go:221` — testing — conf 0.80 — _reviewer-reported (unverified)_

No test in the package exercises the transport's runtime behavior: the 5xx→(resp, nil) serverError conversion, breaker-open ErrCircuitOpen mapping, body close on the defensive non-serverError-with-response path, threshold/reset interaction, or the custom shouldTrip path. Existing tests (httpx_test.go, deadline_transport_test.go) only assert constructor panics, transport type assertions, and TLS/redirect config. The most behavior-rich code in the package ships untested.

**Suggested fix:** Add httptest-backed tests: N consecutive 5xx opens breaker, subsequent request gets ErrCircuitOpen, 5xx returns (resp, nil) with readable body.

#### 136. [MEDIUM] RoundTrip zeroes the KeyStore-returned secret; copy-per-call contract is undocumented
`httpx/sign/sign.go:343` — api-design — conf 0.70 — _reviewer-reported (unverified)_

The deferred loop zeroes the slice returned by KeyStore.CurrentKeyID, assuming "per-call copy" semantics that the exported KeyStore interface doc (lines 50-66) never states. A third-party KMS/Vault adapter returning a cached slice has its key silently zeroed after the first request: later requests still pass validateSigningKey (length-only) and get signed with a corrupted key, failing verification server-side. Concurrent RoundTrips sharing one slice also race (concurrent zero vs HMAC read).

**Suggested fix:** Document on KeyStore that CurrentKeyID must return a fresh secret copy per call; optionally have transport copy defensively before zeroing.

#### 137. [MEDIUM] Dots in backslash-delimited segments are not %2E-escaped (inconsistent with detection logic)
`httpx/urlutil/urlutil.go:194` — security — conf 0.60 — _reviewer-reported (unverified)_

containsDecodedPathControl treats '\\' as a segment separator when detecting dot segments (line 178), but isDotSegmentByte only checks '/' neighbours. For part `a\\..\\b` the function detects danger (disables escape preservation) yet emits `a%5C..%5Cb` with raw dots. A downstream normalizer that maps backslash to slash (IIS, some proxies/CDNs) decodes this to `a\\..\\b` -> `a/../b`, a traversal the package's own %2E-escaping design (asvs V5.2.5, TestAppendPaths_doesNotCleanDotSegments) is meant to prevent. Raw-slash equivalents are correctly escaped.

**Suggested fix:** Make isDotSegmentByte treat '\\' as a boundary too (matching containsDecodedPathControl), so backslash-adjacent dots become %2E.

#### 138. [MEDIUM] False claim that signedrequest receivers accept the X-Kit header triplet
`httpx/webhook/webhook.go:21` — docs — conf 0.80 — _reviewer-reported (unverified)_

Comment says "Receivers wired with [httpx/middleware/signedrequest] expect this triplet", and doc.go directs receiving to signedrequest. But signedrequest uses fixed X-Signature-Timestamp/Nonce/Key-Id/X-Signature headers and verifies a canonical string over method/path/nonce/key-id (signedrequest.go:48-51, 395), with no option to rename headers. Webhook sends X-Kit-* with a body-only HMAC; signedrequest.Middleware rejects every such delivery. Users following the docs get a 100% verification failure. Actual counterpart is crypto/signing.Verify.

**Suggested fix:** Fix webhook.go and doc.go to point receivers at crypto/signing.Verify with the X-Kit headers, or actually emit the signedrequest wire format.

#### 139. [MEDIUM] New accepts secrets shorter than 32 bytes; every Send then fails at runtime
`httpx/webhook/webhook.go:99` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

New only checks len(cfg.Secret) > 0, but crypto/signing.SignContext rejects secrets under minSecretLen=32 (signing.go:154, ErrEmptySecret). A Dispatcher built with a 1-31 byte secret passes the documented "validates Config" construction, then every Send returns a permanent sign error at runtime. Inconsistent with sibling constructors (sign.Wrap, NewCursorSigner, NewStaticKeyStore) which all enforce the 32-byte floor at construction.

**Suggested fix:** Reject len(cfg.Secret) < 32 in New with a clear error, matching crypto/signing's floor.

#### 140. [MEDIUM] DeliveryID and URL are not bound into the HMAC, hollowing out the claimed replay protection
`httpx/webhook/webhook.go:163` — security — conf 0.75 — _reviewer-reported (unverified)_

Sign covers only "<timestamp>.<body>"; X-Kit-Delivery-Id is sent unsigned even though Delivery.DeliveryID docs (line 134) and doc.go ("replay-protection nonces") present it as the receiver's nonce. An attacker replaying a captured request can simply mint a fresh delivery ID, bypassing receiver-side dedupe for the full signing.Verify maxAge window (default 5m). Path/method are also unbound (CanonicalContext unused), so a delivery is replayable across endpoints sharing the secret.

**Suggested fix:** Sign DeliveryID (e.g. via SignContext or include ID in signed payload) as an additive v2 header format; correct the replay-protection claims in doc.go meanwhile.

#### 141. [MEDIUM] No exported close-status classifier; normal peer disconnects are logged as handler errors
`httpx/websocket/handler.go:125` — api-design — conf 0.70 — _reviewer-reported (unverified)_

The kit exports StatusCode constants 'so callers can switch on close codes without importing the upstream package', but there is no exported equivalent of coderws.CloseStatus(err) — internally used at conn.go:221. A handler has no kit-native way to distinguish a normal peer close (1000/1001) from real failures, so the natural pattern of returning the read error (used by the package's own tests) makes Handle log WARN 'handler returned error' and attempt a StatusInternalError close on every routine client disconnect.

**Suggested fix:** Add additive func CloseStatus(err error) StatusCode (and/or IsNormalClosure helper); in Handle, skip the warn/InternalError path when the returned error carries a normal/going-away close status.

#### 142. [LOW] Panic message missing httpx/ prefix
`httpx/budget/budget.go:213` — consistency — conf 0.95 — _reviewer-reported (unverified)_

Wrap's nil-option panic reads "budget: Wrap option must not be nil" while every other panic in this package uses the "httpx/budget:" prefix. Operators grepping panics will attribute it to data/budget.

**Suggested fix:** Change to "httpx/budget: Wrap option must not be nil".

#### 143. [LOW] retryAfter from Consume is discarded; callers cannot back off
`httpx/budget/budget.go:236` — api-design — conf 0.70 — _reviewer-reported (unverified)_

The pre-charge Consume returns a retryAfter hint (when the next period starts) but RoundTrip drops it and returns the bare ErrBudgetExceeded sentinel. Callers distinguishing budget rejection from upstream 429s still cannot schedule a retry without separately calling Peek/knowing the window.

**Suggested fix:** Additive: return a typed error wrapping ErrBudgetExceeded carrying RetryAfter (errors.As-accessible), keeping errors.Is compatibility.

#### 144. [LOW] Duplicate/garbage estimate header silently falls back to default
`httpx/budget/budget.go:269` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

estimate() falls back to defaultAmount on duplicate, empty, or unparseable estimate headers with no log, whereas the analogous actual-header path in reconcile() warns operators (L294, L300). A proxy duplicating the estimate header silently mis-accounts every request's pre-charge.

**Suggested fix:** Emit the same logger.Warn on ambiguous/malformed estimate headers as reconcile does for actual headers.

#### 145. [LOW] singletonHeaderValue duplicates headerutil with inverted ok-semantics for missing headers
`httpx/budget/budget.go:336` — consistency — conf 0.70 — _reviewer-reported (unverified)_

budget's private singletonHeaderValue returns ("", false, true) — ok=true — when the header is absent, while the sibling httpx/internal/headerutil.SingletonToken returns ok=false for absent. Two near-identical trust-boundary helpers in the same module with opposite ok meaning for the missing case invite copy-paste bugs; budget also trims whitespace where headerutil rejects it.

**Suggested fix:** Reuse or extend headerutil (e.g. a SingletonValue variant) instead of a divergent private copy; document the missing-vs-invalid contract.

#### 146. [LOW] WriteServiceProblem with nil logger drops 5xx error logs entirely
`httpx/error_handler.go:161` — error-handling — conf 0.65 — _reviewer-reported (unverified)_

logErr returns immediately when logger == nil, and the function never consults the request-scoped logger or slog.Default. WriteServiceError, by contrast, routes through Logger(ctx, logger) and falls back to slog.Default, so the same nil-logger call site logs there but is fully silent here — unavailable/operation-failed errors vanish without trace. Tests (error_handler_test.go:97-113) codify the silent behavior rather than question it.

**Suggested fix:** Mirror WriteServiceError: use Logger(ctx, logger) when r != nil and slog.Default() otherwise, instead of skipping logging.

#### 147. [LOW] WriteServiceProblem omits Retry-After header set by its sibling
`httpx/error_handler.go:190` — consistency — conf 0.70 — _reviewer-reported (unverified)_

WriteServiceError sets the Retry-After header for rate-limit and unavailable errors with RetryAfter > 0; WriteServiceProblem only emits the `retry_after_seconds` JSON extension (problemdetails.Write sets no headers — verified). Standard clients, proxies, and CDNs honor the header, not a body extension, so services migrating from WriteServiceError to problem+json silently lose RFC-compliant backoff signaling.

**Suggested fix:** Set Retry-After in WriteServiceProblem before problemdetails.Write when AsRateLimit/AsUnavailable report RetryAfter > 0, mirroring WriteServiceError.

#### 148. [LOW] HTTPCheck does not validate name/url at construction, unlike kit convention
`httpx/healthhttp/handler.go:116` — consistency — conf 0.60 — _reviewer-reported (unverified)_

Sibling constructors in this unit (authz extractors, budget options) panic on invalid configuration at mount time. HTTPCheck accepts an empty/garbage URL or name silently; a typo'd URL only surfaces as a perpetually-unhealthy dependency at probe time, and an invalid name surfaces later via ValidateChecker only if the caller wires it through Handler.

**Suggested fix:** Validate name (health.ValidateDependencyCheck) and url.Parse at construction; panic on misconfiguration per kit convention.

#### 149. [LOW] Health check drains dependency response body without a size limit
`httpx/healthhttp/handler.go:142` — performance — conf 0.70 — _reviewer-reported (unverified)_

httpCheck does io.Copy(io.Discard, resp.Body) with no byte cap. A misbehaving or malicious dependency can stream data for up to 5 seconds per probe, wasting bandwidth/CPU on every readiness evaluation interval. Time-bounded but not byte-bounded.

**Suggested fix:** Drain via io.CopyN(io.Discard, resp.Body, smallLimit) (e.g. 4KiB) before Close.

#### 150. [LOW] ParseID silently truncates on 32-bit platforms
`httpx/httpx.go:400` — correctness — conf 0.85 — _reviewer-reported (unverified)_

strconv.ParseUint(idStr, 10, 64) accepts values up to 2^64-1, then `uint(id)` truncates to 32 bits where uint is 32-bit (GOARCH=arm, 386). An id like 4294967297 parses successfully and returns (1, true) instead of failing, so a handler can act on the wrong record on 32-bit builds.

**Suggested fix:** Use strconv.ParseUint(idStr, 10, strconv.IntSize) so out-of-range values fail instead of truncating.

#### 151. [LOW] DecodeJSON trailing-data and unknown-field rejection untested
`httpx/httpx.go:446` — testing — conf 0.75 — _reviewer-reported (unverified)_

DecodeJSON's two strictness guarantees — DisallowUnknownFields and the second-decode io.EOF check rejecting bodies like `{"a":1} {"b":2}` (a subtle check the comment says replaced a buggy dec.More() approach) — have no tests in the package (grep for trailing/unknown across *_test.go finds none). Both are security-relevant parser-differential guards and regressions would pass the suite silently.

**Suggested fix:** Add table tests: unknown field → 400, trailing second JSON value → 400, trailing whitespace-only → ok.

#### 152. [LOW] Duplicate, divergent package comments in doc.go and httpxtest.go
`httpx/httpxtest/doc.go:1` — docs — conf 0.80 — _reviewer-reported (unverified)_

Both doc.go and httpxtest.go carry package doc comments for package httpxtest. doc.go's short version (mentioning "authenticated contexts" — a feature not present in the package) conflicts with httpxtest.go's accurate, detailed comment; go/doc renders the doc.go one first (sorted file order), surfacing the stale text.

**Suggested fix:** Delete doc.go (or its comment) and keep the accurate package comment in httpxtest.go only.

#### 153. [LOW] DoRealServerRequest loses ContentLength, silently switching to chunked encoding
`httpx/httpxtest/httpxtest.go:147` — correctness — conf 0.65 — _reviewer-reported (unverified)_

http.NewRequestWithContext only infers ContentLength for *bytes.Buffer/*bytes.Reader/*strings.Reader. Requests built with httptest.NewRequest wrap the body in a NopCloser, so the rebuilt target gets ContentLength 0 with a non-nil Body → sent chunked. Handlers/middleware under test that read Content-Length or reject chunked transfer see different behavior than the original request specified; GetBody is also dropped.

**Suggested fix:** Copy req.ContentLength and req.GetBody onto target, or buffer the body and rebuild with bytes.NewReader.

#### 154. [LOW] CloneTLSConfigWithFloor ignores its label parameter, losing panic diagnosability
`httpx/internal/transportdefaults/transport.go:57` — api-design — conf 0.90 — _reviewer-reported (unverified)_

Every call site threads a caller label ("httpx/budget: Wrap", "healthhttp: HTTPCheck", httpx.go L116/124) but the parameter is discarded (_ string) and both panic messages are static. When the process default transport carries InsecureSkipVerify or a low MaxVersion, the panic cannot tell operators which constructor tripped it.

**Suggested fix:** Interpolate label into both panic messages, or drop the parameter from the internal API.

#### 155. [LOW] Second Stop call returns nil immediately without awaiting worker drain
`httpx/mcp/mcp.go:516` — concurrency — conf 0.75 — _reviewer-reported (unverified)_

If the first Stop(ctx) times out (returns ctx.Err()) while workers are still draining, auditStopped is already true, so any subsequent Stop returns nil instantly without re-waiting on auditWG. A caller retrying Stop with a fresh context gets a false success while audit appends are still in flight, racing process exit.

**Suggested fix:** On the already-stopped path, still wait on auditWG (with ctx) instead of returning nil unconditionally.

#### 156. [LOW] reflect.TypeOf(tok).String() panics when trailing token is JSON null
`httpx/mcp/mcp.go:917` — correctness — conf 0.70 — _reviewer-reported (unverified)_

In the trailing-token rejection branch, dec.Token() returns a nil interface for a JSON null token; reflect.TypeOf(nil) returns nil and .String() panics. Reachability is low because req.Params.Arguments is a json.RawMessage holding exactly one JSON value from the envelope, so trailing tokens cannot normally occur — but the defensive code itself is panic-prone if ever reached (e.g. direct in-process invocation).

**Suggested fix:** Guard: kind := "null"; if tok != nil { kind = reflect.TypeOf(tok).String() }.

#### 157. [LOW] TestServer_UnknownTool_ReturnsErrorResponse can pass without asserting anything
`httpx/mcp/server_test.go:252` — testing — conf 0.80 — _reviewer-reported (unverified)_

The test returns early when the JSON-RPC response carries an error member (`if rpc.Error != nil { return }`), so whichever path the SDK takes for unknown tools, one branch asserts nothing. If the SDK ever started returning a success envelope for unknown tools only in the error-member case, the regression would not be caught.

**Suggested fix:** Assert explicitly: require rpc.Error != nil OR res.IsError, failing if neither holds.

#### 158. [LOW] Async-context test's assertions are conditional and may silently assert nothing
`httpx/mcp/server_test.go:790` — testing — conf 0.75 — _reviewer-reported (unverified)_

TestServer_ActionLog_AsyncMode_PreservesContextValuesAfterCancellation only checks logger.value/ctxErr inside `if logger.value != nil`. If the SDK rejects the cancelled-context request before enqueue (a plausible outcome the comment itself acknowledges), the test exercises and verifies nothing about context propagation, permanently green regardless of the WithoutCancel behavior it exists to pin.

**Suggested fix:** Drive the enqueue deterministically (call the wrapper directly or require logger.value non-nil) so the context-preservation property is actually asserted.

#### 159. [LOW] writeError shape diverges from httpx.WriteError; comment rationale is false
`httpx/middleware/approval/approval.go:448` — consistency — conf 0.90 — _reviewer-reported (unverified)_

writeError emits {"error": msg} while sibling middleware (apikey) uses httpx.WriteError, which emits APIError{error, code}. The justifying comment claims this package "is its own module" and avoids dragging the httpx dep — but the file already imports github.com/bds421/rho-kit/httpx/v2 (line 19) and lives inside the httpx/v2 module. Consumers get inconsistent error envelopes across kit middlewares.

**Suggested fix:** Use httpx.WriteError (already imported) and delete the misleading comment.

#### 160. [LOW] EnsureBodyBuffered leaves stale GetBody and Content-Length header from the cloned request
`httpx/middleware/approval/approval.go:457` — api-design — conf 0.70 — _reviewer-reported (unverified)_

r.Clone copies GetBody and the Header map. The returned request has Body/ContentLength pointing at the stored payload, but GetBody (if set, e.g. requests built via http.NewRequest) still resurrects the ORIGINAL body on retry/redirect, and a stale Content-Length header may disagree with the new payload length when executors replay via an HTTP client.

**Suggested fix:** Set r2.GetBody to return a fresh reader over owned, and delete/overwrite the Content-Length header.

#### 161. [LOW] WithTrustedProxies doc wrong: default and explicit-empty list silently trust loopback proxies
`httpx/middleware/auditlog/auditlog.go:71` — docs — conf 0.80 — _reviewer-reported (unverified)_

Doc claims "Default: no trusted proxies (only the direct r.RemoteAddr peer is recorded)". Actually clientip.ClientIPWithTrustedProxies substitutes defaultTrustedProxyCIDRs (127.0.0.0/8, ::1/128) whenever len(trusted)==0 — including when an operator explicitly passes an empty slice to mean "trust nobody". Loopback peers' X-Real-IP/X-Forwarded-For ARE honored by default, and proxy trust cannot be fully disabled via this option.

**Suggested fix:** Fix the doc; consider distinguishing nil (default loopback) from explicit empty (trust none), or document WithClientIPFunc as the opt-out.

#### 162. [LOW] Synchronous audit emit with 5s timeout runs in every request goroutine
`httpx/middleware/auditlog/auditlog.go:177` — performance — conf 0.60 — _reviewer-reported (unverified)_

writeAuditEntry executes in the deferred block before the handler chain returns: a degraded audit sink stalls each request goroutine up to 5 seconds after the response is written, blocking keep-alive connection reuse and growing goroutines at ~rps×5s with no concurrency cap. Deliberate durability tradeoff, but unbounded under sink failure and undocumented on Middleware.

**Suggested fix:** Document the synchronous-emit latency/goroutine impact; consider an opt-in bounded async emit or shorter default timeout option.

#### 163. [LOW] Callback-panic logging bypasses the WithErrorLogger logger
`httpx/middleware/auditlog/safe.go:60` — consistency — conf 0.70 — _reviewer-reported (unverified)_

logCallbackPanic always writes to slog.Default(), while emit failures honor cfg.errLogger from WithErrorLogger. Operators who route audit-pipeline errors to a dedicated logger (the documented purpose of WithErrorLogger) will not see path-filter/status-filter/extractor panics there, fragmenting the audit-health signal across two sinks.

**Suggested fix:** Thread cfg.errLogger into the safe* wrappers and fall back to slog.Default() only when unset.

#### 164. [LOW] Impersonation guard 'identity' prefix convention (cn:/dns:/uri:) is undocumented
`httpx/middleware/auth/auth.go:104` — api-design — conf 0.75 — _reviewer-reported (unverified)_

WithS2SImpersonationGuard says the callback decides whether `identity` may impersonate `userID`, but the value delivered is prefixed by matchCertIdentity ("cn:backend", "dns:svc-a.internal", "uri:spiffe://..."). A guard comparing against raw service names (identity == "backend") rejects every request — a silent fail-closed outage only discoverable from logs or tests.

**Suggested fix:** Document the prefix scheme on WithS2SImpersonationGuard (and RequireS2SAuthWithIdentity), or expose constants/helpers for parsing it.

#### 165. [LOW] URI SAN matching is case-sensitive while DNS SAN matching is case-insensitive
`httpx/middleware/auth/auth.go:310` — correctness — conf 0.65 — _reviewer-reported (unverified)_

matchCertIdentity lowercases cert DNSNames before lookup, but URI SANs use exact u.String() equality and mtlsidentity.NormalizeSAN preserves URI host case (only DNS-form entries are lowercased). RFC 3986 hosts are case-insensitive, so a cert bearing spiffe://Example.org/svc-a fails against allowlist spiffe://example.org/svc-a. Fail-closed (rejection, not bypass), but inconsistent treatment between the two SAN kinds in the same function.

**Suggested fix:** Lowercase URI scheme+host on both allowlist (NormalizeSAN) and cert side before comparison, keeping path case-sensitive (SPIFFE-compatible).

#### 166. [LOW] TestRequireS2SAuth_ValidMTLS permissions assertion checks the wrong context — vacuous
`httpx/middleware/auth/auth_test.go:708` — testing — conf 0.85 — _reviewer-reported (unverified)_

The test asserts `Permissions(withUserIDForTest(req.Context(), testUUID))` is nil, but req.Context() is the ORIGINAL request context, which never had permissions set regardless of middleware behavior. The intended invariant — that the mTLS S2S branch stamps no permissions onto the context seen by the handler — is not actually verified; the handler-observed context is discarded.

**Suggested fix:** Capture Permissions(r.Context()) inside the handler (like capturedUserID) and assert nil on that.

#### 167. [LOW] API key header value has no length cap, unlike the 8KB bearer-token cap
`httpx/middleware/auth/strategy.go:278` — security — conf 0.70 — _reviewer-reported (unverified)_

parseBearerToken enforces maxBearerTokenLen (8KB), but NewAPIKeyAuthenticator passes any single header value of any length (up to the ~1MB server header limit) straight to VerifyAPIKey. Verifiers typically hash/compare the key, so attacker-sized inputs cost CPU per request; inconsistent hardening versus the sibling bearer path in the same package.

**Suggested fix:** Apply a length cap (e.g., reuse maxBearerTokenLen) before invoking the verifier; reject oversized values as ErrInvalidCredentials.

#### 168. [LOW] 429 rejection omits Cache-Control: no-store and uses different Content-Type than httpx.WriteError
`httpx/middleware/budget/budget.go:204` — consistency — conf 0.75 — _reviewer-reported (unverified)_

writeRejected hand-rolls the response with "application/json; charset=utf-8" and no Cache-Control, while every other error in this middleware goes through httpx.WriteError which sets "application/json" plus "Cache-Control: no-store" (asserted by the package's own assertJSONError). The 429 carries per-key X-Budget-Remaining/Retry-After data, the one response in the package that most wants no-store.

**Suggested fix:** Set Cache-Control: no-store in writeRejected and align the Content-Type with httpx.WriteError.

#### 169. [LOW] Only the first Accept-Encoding header line is parsed
`httpx/middleware/compress/compress.go:162` — correctness — conf 0.80 — _reviewer-reported (unverified)_

selectEncoder receives r.Header.Get("Accept-Encoding"). Accept-Encoding is a list-typed header that may legally be split across multiple field lines; additional lines are ignored, so 'Accept-Encoding: br' + 'Accept-Encoding: gzip' negotiates as br-only and the response goes uncompressed.

**Suggested fix:** Use strings.Join(r.Header.Values("Accept-Encoding"), ",") before parsing.

#### 170. [LOW] Wildcard '*' can select an encoding the client explicitly refused with q=0
`httpx/middleware/compress/compress.go:217` — correctness — conf 0.70 — _reviewer-reported (unverified)_

Entries with q=0 are dropped before matching, so 'gzip;q=0, *' yields prefs=[*] and the wildcard branch returns encoders[0] (gzip) — the coding the client explicitly excluded. RFC 9110 §12.5.3: '*' matches codings not explicitly mentioned.

**Suggested fix:** Track q=0 tokens in a refused set and have the wildcard branch skip encoders whose token was explicitly refused.

#### 171. [LOW] No tests for multi-chunk writes, default-config buffer ceiling, or panic path
`httpx/middleware/compress/compress_test.go:42` — testing — conf 0.85 — _reviewer-reported (unverified)_

Every test writes the body in a single Write call, so the n>len(p) contract violation, the >256KiB-single-write bypass under default config (the ceiling test only uses minSize>maxBuffer), and the panic-converts-to-200 finalize path are all uncovered. An io.Copy-based handler test would have caught two high-severity bugs.

**Suggested fix:** Add tests: handler writing many sub-1KiB chunks via io.Copy; single >MaxBufferSize write with defaults asserting gzip; panicking handler under an outer recover middleware asserting 500.

#### 172. [LOW] Write after Hijack panics with nil dereference instead of returning an error
`httpx/middleware/compress/writer.go:141` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

Hijack sets cw.buf = nil while mode stays modeUndecided. A buggy handler that writes after hijacking hits cw.buf.Len() in Write and panics with a nil-pointer dereference; net/http's own writer returns http.ErrHijacked for this case. Also, Hijack in modeCompressed Closes the gzip writer, flushing a gzip trailer through the response path it is about to abandon.

**Suggested fix:** Have Write/Flush return http.ErrHijacked (or no-op) when hijacked; on Hijack, reset the encoder to io.Discard instead of Close before Release.

#### 173. [LOW] jcors.NewMiddleware error discarded — panic gives no hint which config entry is invalid
`httpx/middleware/cors/cors.go:120` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

On configuration error the code panics with the fixed string 'middleware/cors: New invalid configuration', dropping err entirely. jub0bs/cors errors identify the offending origin pattern/header; origins are not secrets, so operators debugging a startup panic get zero diagnostic information. The test even pins this opaque message via PanicsWithValue.

**Suggested fix:** panic("middleware/cors: " + err.Error()) or fmt.Sprintf including err so the failing pattern is identifiable at startup.

#### 174. [LOW] HTMLAttr comment misstates nonce encoding (claims URL-safe + padding; actual is RawStdEncoding)
`httpx/middleware/cspnonce/cspnonce.go:132` — docs — conf 0.85 — _reviewer-reported (unverified)_

generateNonce uses base64.RawStdEncoding (standard alphabet with '+' and '/', no padding), but HTMLAttr's trust-boundary comment says 'base64 (URL-safe alphabet + = padding)'. The output is still attribute-safe ('+'/'/' need no HTML escaping), but the safety justification is wrong on both alphabet and padding — risky if someone later relies on the documented alphabet (e.g. embedding the nonce in a URL).

**Suggested fix:** Fix the comment to RawStdEncoding, or switch to RawURLEncoding so the documented property holds.

#### 175. [LOW] Auto-added script-src/style-src include 'self', silently widening stricter base policies; *-elem directives bypass the nonce
`httpx/middleware/cspnonce/cspnonce.go:177` — security — conf 0.70 — _reviewer-reported (unverified)_

When the base policy lacks script-src/style-src, injectNonce appends them with 'self' + nonce. For a base of "default-src 'none'" this re-enables same-origin script/style file loads the operator explicitly forbade. Separately, a base policy containing script-src-elem/style-src-elem is not augmented, and those directives take precedence over script-src/style-src for elements — so the injected nonce is ineffective and inline scripts stay blocked.

**Suggested fix:** Inherit the default-src source list (or nonce-only) when creating directives, and also inject the nonce into script-src-elem/style-src-elem when present.

#### 176. [LOW] Unreachable nil-cookie recovery block in double-submit handler
`httpx/middleware/csrf/csrf.go:504` — correctness — conf 0.85 — _reviewer-reported (unverified)_

At line 504, `cookie` can never be nil: every path that leaves it nil (r.Cookie error) regenerates and sets cookieRegenerated, which returns 403 at line 500. The block `if cookie == nil { cookie, err = r.Cookie(...) }` and the subsequent err check at line 507 are dead code that obscures the actual invariant and re-uses err confusingly.

**Suggested fix:** Delete lines 504-510 or replace with a comment stating the invariant that cookie is non-nil and previously validated here.

#### 177. [LOW] Cookie MaxAge hardcoded to 24h regardless of session token TTL
`httpx/middleware/csrf/csrf.go:566` — api-design — conf 0.70 — _reviewer-reported (unverified)_

All four Set-Cookie sites hardcode MaxAge 86400, but session-bound tokens expire after sessionTTL (default 1h, configurable via WithSessionTTL). A browser holds a dead cookie for up to 23h; the first state-changing request after TTL expiry deterministically 403s with 'CSRF cookie was reissued; retry' (lines 556→609), forcing clients to implement retry for routine idle periods. Aligning cookie lifetime with token validity would surface expiry on the preceding safe request instead.

**Suggested fix:** In session-bound mode derive MaxAge from the effective TTL (cfg.sessionTTL or securitycsrf.DefaultTTL) instead of the hardcoded 86400.

#### 178. [LOW] Legacy token verification HMACs unbounded attacker-controlled input, unlike sibling security/csrf
`httpx/middleware/csrf/csrf.go:774` — performance — conf 0.75 — _reviewer-reported (unverified)_

isValidSignedToken has no length cap: cookie.Value (up to ~1MB under default MaxHeaderBytes) is HMAC-SHA256'd once per secret in the ring on every request via isValidSignedTokenAny at line 421 (all methods, including GET), and potentially again at lines 443 and 529. security/csrf added MaxTokenLen=256 explicitly to stop multi-MB hostile inputs; the legacy double-submit path lacks the equivalent guard. A valid token is always exactly 129 chars (64 hex + '.' + 64 hex).

**Suggested fix:** Reject tokens longer than ~256 bytes (or != 129 chars) before computing any HMAC, mirroring securitycsrf.MaxTokenLen.

#### 179. [LOW] WithoutRequiredMethods makes the middleware a total no-op, not 'optional' enforcement
`httpx/middleware/idempotency/idempotency.go:242` — api-design — conf 0.70 — _reviewer-reported (unverified)_

With an empty requiredMethods map, every request takes the early pass-through at line 368 — Idempotency-Key headers that clients DO send are completely ignored (no dedup, no caching). The doc says "every request becomes optional", which suggests opportunistic deduplication when a key is present; actual behavior is full bypass. Same applies to non-required methods carrying a key under default config.

**Suggested fix:** Document that non-required methods bypass dedup entirely, or implement opportunistic dedup when the header is present.

#### 180. [LOW] Metric counters fire for cases their Help text excludes
`httpx/middleware/idempotency/idempotency.go:437` — docs — conf 0.80 — _reviewer-reported (unverified)_

errors_total is described as "store errors (500)" but is incremented for body-read failures answered with 400 (line 437). conflicts_total is described as "concurrent processing (409)" but is also incremented for fingerprint mismatches answered with 422 (lines 457, 482). Dashboards and alerts keyed on these helps will misattribute client errors as backend faults and 422s as contention.

**Suggested fix:** Split or re-document: separate mismatch counter, and don't count client body-read failures as store errors.

#### 181. [LOW] preserveHeaders lookup uses raw map key, not canonical form
`httpx/middleware/idempotency/idempotency.go:535` — correctness — conf 0.80 — _reviewer-reported (unverified)_

The strip filter checks cfg.preserveHeaders[k] with the raw iteration key but canonicalises only for the identityResponseHeaders lookup. A handler that writes an identity header via direct map access (rc.Header()["set-cookie"] = ...) is still stripped even when WithPreserveHeaders("Set-Cookie") was configured, because preserveHeaders keys are canonical.

**Suggested fix:** Use cfg.preserveHeaders[http.CanonicalHeaderKey(k)] for the preserve check.

#### 182. [LOW] 1xx informational WriteHeader is captured as the final cached status
`httpx/middleware/idempotency/idempotency.go:783` — correctness — conf 0.70 — _reviewer-reported (unverified)_

responseCapture.WriteHeader treats the first call as final. A handler emitting w.WriteHeader(103) (Early Hints — valid and repeatable in net/http) locks statusCode=103 and suppresses the later real WriteHeader; the cached response then replays 103 with a body as the final status to subsequent callers, producing a broken response.

**Suggested fix:** Pass through 1xx codes without latching wroteHeader/statusCode, mirroring net/http semantics.

#### 183. [LOW] TestConcurrentRequestsSameKey can flake under CI scheduling delay
`httpx/middleware/idempotency/idempotency_test.go:663` — testing — conf 0.50 — _reviewer-reported (unverified)_

The test assumes both goroutines overlap within the handler's 50ms sleep. If the second goroutine is scheduled after the first request completes (possible on a loaded CI machine), it gets a cache replay (201) instead of 409, failing the "one 201 and one 409" assertion even though handlerCalls==1 passes. No synchronization (e.g. a barrier inside the handler) guarantees overlap.

**Suggested fix:** Gate the handler on a channel/WaitGroup so the second request provably arrives while the first holds the lock.

#### 184. [LOW] WithClientIPResolver(nil) is silently ignored, against kit fail-fast convention
`httpx/middleware/logging/logging.go:41` — consistency — conf 0.70 — _reviewer-reported (unverified)_

Passing nil to WithClientIPResolver silently keeps the default resolver, while the same file panics on a nil LoggerOption and sibling packages (idempotency.WithUserExtractor, WithMetrics, metrics.WithRegisterer) panic on nil arguments. A miswired caller (resolver variable accidentally nil) silently falls back to the loopback-only default and logs proxy IPs instead of client IPs.

**Suggested fix:** Panic on nil resolver to match the kit's fail-fast option convention.

#### 185. [LOW] Hijacked (WebSocket) requests are logged with bogus status=200
`httpx/middleware/logging/logging.go:121` — consistency — conf 0.75 — _reviewer-reported (unverified)_

logAccessRequest never consults wrapped.WasHijacked(), so a hijacked connection (101 written directly to the raw conn) is logged as status=200 with a duration spanning the entire socket lifetime. The sibling metrics middleware explicitly skips hijacked requests for exactly this reason (metrics.go line 110), so the two disagree.

**Suggested fix:** Check wrapped.WasHijacked() and either skip the access log line or log a hijacked=true attr without a synthetic status.

#### 186. [LOW] Doc overpromises automatic 413 — http.MaxBytesReader does not write a status
`httpx/middleware/maxbody/maxbody.go:8` — docs — conf 0.85 — _reviewer-reported (unverified)_

Both maxbody.go ("will receive a 413 ... when the handler attempts to read the body") and doc.go ("exceeding the cap returns HTTP 413 the first time the handler calls Read") claim a 413 response. MaxBytesReader only makes Read fail with *http.MaxBytesError and flags connection-close; the status written depends entirely on the handler. httpx.DecodeJSON maps it to 413, but any handler using io.ReadAll typically returns 400/500.

**Suggested fix:** Reword: "reads beyond the cap fail with *http.MaxBytesError; kit decode helpers translate it to 413".

#### 187. [LOW] Over-limit test asserts nothing about the response or error type
`httpx/middleware/maxbody/maxbody_test.go:32` — testing — conf 0.80 — _reviewer-reported (unverified)_

TestMaxBodySize_OverLimit only checks that io.ReadAll returns some error inside the handler; it never asserts the error is *http.MaxBytesError nor verifies the documented 413 behavior (which would have exposed the doc inaccuracy). The recorder's status is never inspected.

**Suggested fix:** Assert errors.As(*http.MaxBytesError) and document/assert the actual resulting status behavior.

#### 188. [LOW] tryRegister discards the registration error and duplicates promutil.MustRegisterOrGet
`httpx/middleware/metrics/metrics.go:179` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

panic("httpx/metrics: metric registration failed") drops err, so a name/help/label conflict (e.g. coexisting with redmetrics on the default registry, which doc.go explicitly warns about) panics with zero diagnostic detail. The unchecked type assertions on lines 89-91 can also panic with a generic interface-conversion message. promutil.MustRegisterOrGet already exists, preserves the error text, and is type-safe; the idempotency sibling uses it.

**Suggested fix:** Replace tryRegister with promutil.MustRegisterOrGet, as in middleware/idempotency.

#### 189. [LOW] KeyedMiddleware accepts a nil next handler; Middleware panics on it
`httpx/middleware/ratelimit/keyed.go:331` — consistency — conf 0.85 — _reviewer-reported (unverified)_

ratelimit.Middleware validates next != nil at construction (ratelimit.go:392-394) but KeyedMiddleware's inner closure does not, so KeyedMiddleware(rl, fn)(nil) defers the failure to a nil-pointer panic on the first allowed request instead of failing fast at wiring time. tenant.New shares the same gap.

**Suggested fix:** Panic on nil next in KeyedMiddleware (and tenant.New) to match the sibling's fail-fast contract.

#### 190. [LOW] KeyedLimiter expiry/cleanup tests use real 10ms windows instead of the injectable clock
`httpx/middleware/ratelimit/keyed_test.go:51` — testing — conf 0.70 — _reviewer-reported (unverified)_

TestKeyedLimiter_WindowExpiry asserts the second Allow within a real 10ms window is denied; on a loaded CI runner a >10ms preemption between the two calls makes it pass the window and flake. TestKeyedLimiter_Cleanup similarly sleeps real time. WithKeyedClock exists precisely for this and is used by the IP-limiter tests (TestLimiterWindowReset) but not here.

**Suggested fix:** Rewrite both tests with WithKeyedClock and a mutable fake now, matching ratelimit_test.go style.

#### 191. [LOW] keyedActiveKeysCollector retains KeyedLimiter pointers forever with no removal path
`httpx/middleware/ratelimit/metrics.go:175` — resource-leak — conf 0.75 — _reviewer-reported (unverified)_

add() only appends; there is no untrack/remove API and the collector lives as long as the registry. Any short-lived or replaced KeyedLimiter attached via WithKeyedMetrics is pinned (all 16 shard LRUs, up to 160k entries each) and keeps emitting stale active_keys series on every scrape. Unbounded growth if limiters are created dynamically.

**Suggested fix:** Add an untrack method or hold weak references; document that metrics-attached limiters must be process-lifetime singletons.

#### 192. [LOW] WithTrustedProxies with an empty slice silently falls back to default trusted CIDRs
`httpx/middleware/ratelimit/ratelimit.go:87` — api-design — conf 0.70 — _reviewer-reported (unverified)_

WithTrustedProxies([]string{}) yields len(trusted)==0, which triggers clientip.ParseTrustedProxies(nil) — the default loopback trusted set — instead of 'trust no proxies'. Undocumented, and inconsistent with the sibling secheaders.WithTrustedProxiesForProto, which explicitly documents nil/empty as reverting to the strict check. A caller intending to disable XFF trust entirely cannot express it.

**Suggested fix:** Document the empty-slice fallback, and consider a distinct way to express trust-none (additive; do not change defaults).

#### 193. [LOW] cleanup comments claim Keys() runs outside the shard lock; code holds the lock
`httpx/middleware/ratelimit/ratelimit.go:248` — docs — conf 0.90 — _reviewer-reported (unverified)_

Both the cleanup doc comment (lines 234-240: 'Keys() snapshots outside the lock so the O(n) slice allocation ... doesn't block concurrent allow() calls') and the inline comment (line 248) assert lock-free snapshotting, but the code wraps Keys() in s.mu.Lock/Unlock. The advertised non-blocking property is false: allow() is blocked during the O(n) snapshot. keyed.go:302-305 repeats the same wrong claim. The hashicorp v2 Cache is internally synchronized, so dropping the shard lock for Keys() would be safe.

**Suggested fix:** Either drop the shard lock around Keys() (cache is thread-safe) or fix both comments to match reality.

#### 194. [LOW] WithStatusCode is unvalidated; an out-of-range code panics inside the recovery path itself
`httpx/middleware/recover/recover.go:96` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

WithStatusCode(0) or any code outside 1xx-5xx is accepted at construction, but http's WriteHeader panics on invalid codes. That panic fires inside the deferred recover block (handlePanic, line 206) while handling a real handler panic, escaping to net/http — the structured log is emitted but the connection dies with no JSON body. Every other option in this kit fails fast at construction.

**Suggested fix:** Panic in WithStatusCode for codes outside 100-599, consistent with the kit's construction-time validation style.

#### 195. [LOW] Flush does not mark wroteHeader, desyncing recorder from committed response
`httpx/middleware/response_recorder.go:69` — correctness — conf 0.70 — _reviewer-reported (unverified)_

http.Flusher.Flush implicitly commits a 200 header if none was written, but the recorder leaves wroteHeader=false. Panic-recovery consumers (metrics.go:118, logging.go:122, tracing.go:82) then treat the response as unwritten and report 500 while the wire already carried 200; an outer handler writing an error triggers a superfluous-WriteHeader. Affects streaming handlers that Flush before a later panic.

**Suggested fix:** In Flush, set wroteHeader=true (statusCode stays 200) before delegating, mirroring net/http's implicit commit.

#### 196. [LOW] validateHeaderValue ignores its name parameter; panics do not say which header is misconfigured
`httpx/middleware/secheaders/secheaders.go:365` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

The function receives the header name but every panic message is generic ('secheaders: header value is invalid'). With up to seven value-bearing options in one New(...) call, an operator gets no indication which option failed. Including the header name (not the value, which is correctly withheld) is safe and matches the tests' no-value-leak assertions.

**Suggested fix:** Include name in the panic message, e.g. panic("secheaders: " + name + " header value is invalid").

#### 197. [LOW] readSpooledBody preallocates full inMemoryMax capacity for every body
`httpx/middleware/signedrequest/body.go:56` — performance — conf 0.85 — _reviewer-reported (unverified)_

sb.mem is allocated with make([]byte, 0, inMemoryMax) before any bytes are read, so every signed request with a non-empty body allocates the full in-memory cap (64KiB default) even for a 200-byte JSON webhook. Operators who raise WithInMemoryBodyMax to avoid disk spooling (e.g. 10MiB) silently get that allocation per request — recreating the heap-amplification the spool design exists to prevent.

**Suggested fix:** Grow sb.mem on demand (cap appends at inMemoryMax) instead of preallocating the maximum.

#### 198. [LOW] Four files fail gofmt -s -l
`httpx/middleware/signedrequest/body.go:152` — build-ci — conf 1.00 — _reviewer-reported (unverified)_

gofmt -s -l flags httpx/middleware/signedrequest/body.go (spooledReader field misalignment, trailing blank line at EOF), signedrequest/metrics.go (verifyReason const block misaligned), signedrequest/metrics_test.go (trailing blank line), and stack/stack.go (EnableCompress/CompressOptions misaligned). make fmt (gofmt -s -w .) would dirty the tree; current lint/CI evidently does not enforce formatting.

**Suggested fix:** Run make fmt and add a gofmt -s -l cleanliness check to the lint/ci target.

#### 199. [LOW] MemoryNonceStore doc claims "backed by a sync.Map"; it is a plain map + Mutex
`httpx/middleware/signedrequest/noncestore.go:16` — docs — conf 0.95 — _reviewer-reported (unverified)_

The type comment says "in-process [NonceStore] backed by a sync.Map". The implementation is map[string]time.Time guarded by sync.Mutex (different contention and sweep characteristics). Per hostile-review policy this is exactly the kind of concurrency doc claim that must match the code; it misleads anyone evaluating lock behavior of the periodic O(n) sweep described in WithSweepEvery.

**Suggested fix:** Change the comment to "backed by a mutex-guarded map".

#### 200. [LOW] streamBody/readBody are dead production code with false comments
`httpx/middleware/signedrequest/signedrequest.go:508` — consistency — conf 0.85 — _reviewer-reported (unverified)_

Comments claim streamBody is "Used only by the offline Sign helper" and readBody is "Kept for the offline Sign helper", but SignCanonical takes Body []byte and never reads the request body; the only callers of readBody are tests (TestReadBodyErrorsAreStable). Two unexported functions exist solely to be tested, and their comments describe a call relationship that does not exist.

**Suggested fix:** Delete streamBody/readBody and their tests, or correct the comments and move the safeWrap error-stability tests onto readSpooledBody.

#### 201. [LOW] SignRequest.Nonce doc references nonexistent nonceMinLen
`httpx/middleware/signedrequest/signedrequest.go:664` — docs — conf 0.95 — _reviewer-reported (unverified)_

The field doc says "Nonce is the [HeaderNonce] value (>= nonceMinLen base64 chars)" but no nonceMinLen identifier exists anywhere in the repo. The actual contract enforced by validNonce is: <= nonceMaxLen chars and decodes to exactly 16 bytes in one of four standard base64 alphabets. Misleads SignCanonical callers about what nonce values are acceptable.

**Suggested fix:** Reword to the real contract: base64 encoding (Std/RawStd/URL/RawURL) of exactly 16 random bytes.

#### 202. [LOW] Nonce-store failure path (store error -> 500) untested
`httpx/middleware/signedrequest/signedrequest_test.go:1` — testing — conf 0.70 — _reviewer-reported (unverified)_

No test exercises verify() when NonceStore.SeenOrStore returns an error: the fmt.Errorf("signedrequest: nonce store: %w") wrap, writeError's default 500 branch for it, and classifyVerifyFailure routing it to bad_signature are all unverified by tests (writeError's default branch is only reached via this path). A regression that failed open on store errors would not be caught.

**Suggested fix:** Add a failing-store fake asserting 500, no nonce consumed, and the metric label emitted.

#### 203. [LOW] Chain.ThenFunc(nil) bypasses Then's nil-handler panic via typed-nil interface
`httpx/middleware/stack/chain.go:60` — api-design — conf 0.85 — _reviewer-reported (unverified)_

Then panics on nil handler to fail fast at wiring time, but ThenFunc(nil) converts a nil http.HandlerFunc into a non-nil http.Handler interface, so the check passes and the composed handler panics only on the first request (nil function call). This defeats the documented fail-fast guarantee; justinas/alice explicitly special-cases this.

**Suggested fix:** In ThenFunc: if fn == nil { return c.Then(nil) } so the existing panic fires at construction.

#### 204. [LOW] Compress, secheaders-options, frame-option, and recover-metrics stack wiring untested
`httpx/middleware/stack/stack_test.go:1` — testing — conf 0.85 — _reviewer-reported (unverified)_

stack_test.go has zero coverage of WithCompress/EnableCompress (including its documented placement between request logger and Inner), WithSecHeadersOptions (including the FR-018 ordering claim that caller options override the typed FrameOption), WithFrameOption, and WithRecoverMetrics. These are the only stack options whose wiring order carries documented invariants, and none are exercised.

**Suggested fix:** Add wiring tests: compressed response with WithCompress, secheaders option override order, and panic counter increment with WithRecoverMetrics.

#### 205. [LOW] Abandoned handler goroutine keeps using *http.Request after ServeHTTP returns
`httpx/middleware/timeout/timeout.go:145` — concurrency — conf 0.60 — _reviewer-reported (unverified)_

After the 503 is written and ServeHTTP returns (immediately with WithHard, after 100ms by default), the handler goroutine may still read r.Body/headers while net/http tears down the request and reuses the connection — a data race for handlers that don't honor ctx. This mirrors http.TimeoutHandler's known hazard, but WithHard() makes it strictly more likely and the docs don't mention the body-read race.

**Suggested fix:** Document that handlers must not touch r.Body after ctx.Done; consider wrapping r.Body in a guard that fails reads once the timeout fires.

#### 206. [LOW] TestTimeout_PanicAfterReturnIsCaptured asserts nothing about capture
`httpx/middleware/timeout/timeout_test.go:199` — testing — conf 0.85 — _reviewer-reported (unverified)_

The test's name and purpose is verifying late panics are captured/logged via drainLateHandler, but it only checks the 503 and then sleeps 20ms — no logger hook, no assertion that the panic value was logged. The entire drainLateHandler/WithLogger path (including redact.PanicValue formatting) is effectively untested; a regression silently dropping late panics would pass.

**Suggested fix:** Install WithLogger with a slog handler writing to a buffer and assert the "handler panicked after request returned" record appears (with sync, not sleep).

#### 207. [LOW] TestTimeout_WriteAfterTimeout orders goroutines with a 5ms sleep — flaky under load
`httpx/middleware/timeout/timeout_test.go:380` — testing — conf 0.60 — _reviewer-reported (unverified)_

The handler sleeps 5ms after ctx.Done() hoping the middleware's writeTimeout has run first. Under CPU contention the handler's Write can still win the race, get buffered with err == nil, and the assertion `err != http.ErrHandlerTimeout` fails intermittently. Sleep-based cross-goroutine ordering is the classic flake pattern.

**Suggested fix:** Poll w.Write in a loop until it returns non-nil error (bounded by a deadline), or expose a test hook signaled after writeTimeout.

#### 208. [LOW] Stale comment from old 10 MiB buffer cap
`httpx/middleware/timeout/timeout_test.go:402` — docs — conf 0.95 — _reviewer-reported (unverified)_

Comment says "11 MiB — 1 MiB over the cap", but defaultMaxBufferSize was lowered to 1 MiB, so overLimit is 2 MiB total (1 MiB over). The test still passes, but the comment survived the cap change and now misdescribes the scenario reviewers reason about.

**Suggested fix:** Update the comment to "2 MiB — 1 MiB over the 1 MiB cap".

#### 209. [LOW] Unwrap exposes the real ResponseWriter, letting ResponseController.Hijack bypass the timeout/buffering design
`httpx/middleware/timeout/writer.go:133` — api-design — conf 0.60 — _reviewer-reported (unverified)_

Flush is deliberately neutered to preserve buffering, yet Unwrap (pre-timeout) hands http.ResponseController the real writer: rc.Hijack() succeeds and escapes the timeout without the WebSocket opt-in; rc.SetWriteDeadline also reaches the real connection. After hijack, writeTimeout's 503 write to the hijacked conn is dropped with a stdlib log. Inconsistent with the hardened opt-in story in WithWebSocketUpgradeBypass.

**Suggested fix:** Document the escape hatch, or return nil from Unwrap (accepting loss of ResponseController support) for routes without the bypass opt-in.

#### 210. [LOW] Flush warning bypasses the configured logger
`httpx/middleware/timeout/writer.go:153` — consistency — conf 0.85 — _reviewer-reported (unverified)_

The one-shot Flush no-op warning always goes to slog.Warn (slog.Default), while the late-panic path carefully routes through cfg.logger (WithLogger). Services with a structured non-default logger lose or misroute the SSE/streaming misconfiguration signal, and the timeoutWriter has no access to the configured logger at all.

**Suggested fix:** Plumb cfg.logger into timeoutWriter and use it for the Flush warning.

#### 211. [LOW] Mount doc claim is false: panic logs do NOT carry correlation/request IDs
`httpx/openapi/openapi.go:68` — docs — conf 0.80 — _reviewer-reported (unverified)_

Doc says "correlation ID is inside recovery so panic logs carry the correlation ID". Recover is outermost; requestid/correlationid store IDs via r.WithContext on the inner copy, and recover.handlePanic reads contextutil.RequestID(r.Context()) from its own outer request — always empty here. Panic log lines and the 500 JSON body's request_id lack any correlation for routes mounted via openapi.Mount.

**Suggested fix:** Fix the comment (ordering gives the opposite result), or have recover fall back to the X-Request-Id response header already set on the shared ResponseWriter.

#### 212. [LOW] Register does not validate the documented leading-slash path requirement; Handle's 'atomic' claim breaks on mux panic
`httpx/openapigen/spec.go:200` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

Doc says path "must start with /" but only the empty string is rejected; "widgets" registers fine and yields an OAS-invalid path. Via Handle, spec.Register succeeds, then mux.Handle("POST widgets") panics (no slash / pattern conflict / duplicate), leaving the spec with a phantom route despite the "registers ... atomically" docstring — visible to any caller that recovers at boot.

**Suggested fix:** Reject paths not starting with "/" in Register; soften the "atomically" wording or validate the mux pattern before mutating the spec.

#### 213. [LOW] Response examples for a status with no other registration are silently dropped
`httpx/openapigen/spec.go:271` — correctness — conf 0.85 — _reviewer-reported (unverified)_

statusSet is the union of responseSchemas, responseExtraContent, responseHeaders, and responseDescriptions — but not responseExamples. WithResponseExample(404, mt, ex) alone produces no 404 Response node at all, even though the loop below explicitly supports "schema-less example" media-type entries for statuses that made it into the set. Silent data loss inconsistent with the in-code design comment.

**Suggested fix:** Add `for status := range cfg.responseExamples { statusSet[status] = struct{}{} }`.

#### 214. [LOW] Document() returns structures aliasing internal Spec state despite 'caller owns the result' doc
`httpx/openapigen/spec.go:389` — api-design — conf 0.70 — _reviewer-reported (unverified)_

build() deep-copies servers/tags/security/components, but each Operation copy shares op.Tags/op.Parameters backing arrays, Response.Content/Headers maps, requestExamples, Info.Contact/License pointers, and per-op Security inner maps with routeState. A caller mutating the returned Document ("the caller owns the result") corrupts the Spec, surfacing in the next cache rebuild. Same aliasing applies to WithSecurity's shallow clone of caller-supplied inner maps.

**Suggested fix:** Deep-copy operations (tags, parameters, responses, request body, security) in applyOperation/build, or document that the returned Document must be treated as read-only.

#### 215. [LOW] BuildResult serialises nil item slices as "data":null instead of []
`httpx/pagination/cursor.go:142` — api-design — conf 0.80 — _reviewer-reported (unverified)_

CursorResult.Data has no nil-normalisation, so when ListFn returns a nil slice (common for empty DB results), HandleCursorList emits {"data":null,...} whereas an empty non-nil slice emits []. JSON consumers in strictly-typed languages treat null vs [] differently; the response shape silently depends on the callee's slice idiom. Tests only cover the []testItem{} case.

**Suggested fix:** In BuildResult, normalise: if items == nil { items = []T{} } before constructing the result.

#### 216. [LOW] HandleCursorList panics on nil ListFn/IDFn while other misconfig returns 500
`httpx/pagination/cursor_list.go:105` — api-design — conf 0.85 — _reviewer-reported (unverified)_

The handler carefully validates DefaultLimit, MaxLimit and Signer readiness and converts each to a logged 500, but a nil opts.ListFn nil-derefs at line 105 and a nil opts.IDFn panics inside BuildResult whenever hasMore is true. Inconsistent failure mode for the same class of wiring bug; the panic surfaces as a torn connection instead of the package's deliberate logged 500.

**Suggested fix:** Add nil checks for ListFn and IDFn next to the existing config validation, returning the same logged 500.

#### 217. [LOW] ParseOffset accepts unbounded client offsets
`httpx/pagination/offset.go:74` — performance — conf 0.60 — _reviewer-reported (unverified)_

limit is clamped to maxLimit but offset accepts any non-negative int up to MaxInt64. ?offset=9223372036854775807 flows straight into SQL OFFSET, which in Postgres/MySQL scans and discards that many rows — the classic offset-pagination DoS the package's own doc comment warns about for limit. No optional cap parameter exists.

**Suggested fix:** Offer an additive ParseOffsetWithMax (or clamp constant) so callers can bound offset like they bound limit.

#### 218. [LOW] Write doc references a non-existent err parameter; Extensions doc falsely claims Write panics
`httpx/problemdetails/problem.go:80` — docs — conf 0.90 — _reviewer-reported (unverified)_

Write's doc comment (lines 80-83) describes behaviour for "if err is non-nil" but the signature is Write(w, p) with no error parameter — stale text from an earlier design. Separately, the Extensions field doc (line 44) says "Write panics" on reserved-key collision, but MarshalJSON returns an error and Write silently degrades to a generic 500 (writeInternalMarshalError) with no logging, so the developer never learns why.

**Suggested fix:** Rewrite both doc comments to match actual behaviour; consider logging or debug-surfacing the marshal failure reason.

#### 219. [LOW] Write panics mid-response for out-of-range Problem.Status
`httpx/problemdetails/problem.go:97` — correctness — conf 0.75 — _reviewer-reported (unverified)_

Write only normalises Status==0; any other caller-supplied value is passed to w.WriteHeader, and net/http panics for codes <100 or >999. FromError always produces valid codes, but Write is exported for hand-built Problems, and the package otherwise converts every misuse into a safe 500 (e.g. the marshal-error path).

**Suggested fix:** Clamp/replace invalid status codes (<100 or >599) with 500 before WriteHeader.

#### 220. [LOW] FuzzSafeRedirect asserts nothing beyond no-panic
`httpx/redirect_fuzz_test.go:30` — testing — conf 0.70 — _reviewer-reported (unverified)_

The doc comment promises the invariant 'returns a string that is safe (relative to root OR pointing at an allow-listed host) OR returns an error', but the fuzz body is `_, _ = SafeRedirect(in, allowed...)` — it would not catch a regression where SafeRedirect starts accepting scheme-relative or off-allowlist URLs, the exact bug class it claims to guard.

**Suggested fix:** On err == nil, re-parse the result and assert it has no host or its host/port matches the allowlist, and scheme is http(s) or empty.

#### 221. [LOW] Quick-start example ignores New's error return (does not compile)
`httpx/webhook/doc.go:32` — docs — conf 0.90 — _reviewer-reported (unverified)_

The example assigns d := webhook.New(...) but New returns (*Dispatcher, error); copying the snippet fails to compile. The example also omits error handling for the very misconfiguration cases New exists to surface, and passes Secret from an env var where a short value would only fail at first Send (see related New validation finding).

**Suggested fix:** Update the example to d, err := webhook.New(...) with error handling.

#### 222. [LOW] Response body drained without a size cap from untrusted receivers
`httpx/webhook/webhook.go:197` — resource-leak — conf 0.70 — _reviewer-reported (unverified)_

io.Copy(io.Discard, resp.Body) reads the third-party receiver's response to EOF with no byte limit. A hostile or broken endpoint streaming data ties up Send (and bandwidth) until the caller's client timeout — and indefinitely if the supplied http.Client has no Timeout, which New does not require. Kit precedent (security/netutil FR-016) explicitly defends against exactly this.

**Suggested fix:** Drain via io.CopyN / io.LimitReader (e.g. 64KiB) before Close.

#### 223. [LOW] Vacuous assertion in TestSend_HonoursCtxCancel
`httpx/webhook/webhook_test.go:184` — testing — conf 0.95 — _reviewer-reported (unverified)_

require.True(t, errors.Is(err, context.DeadlineExceeded) || err != nil, ...) is always true once require.Error(t, err) has passed on the previous line — the || err != nil arm makes the condition tautological. The test therefore never verifies that cancellation actually produced a ctx-derived error rather than, say, retry-budget exhaustion.

**Suggested fix:** Assert errors.Is(err, context.DeadlineExceeded) directly, or drop the redundant check.

#### 224. [LOW] Every ping failure is recorded as 'timeout' and closed with PolicyViolation
`httpx/websocket/heartbeat.go:62` — correctness — conf 0.60 — _reviewer-reported (unverified)_

Any Ping error that survives the ctx re-check — e.g. peer reset or the connection closed by a racing read-error path before the handler returns — is counted as pingResultTimeout and triggers Close(StatusPolicyViolation, "ping timeout"). The pings_total{result="timeout"} metric and PolicyViolation close label therefore conflate genuine pong-deadline expiry with ordinary connection death, skewing the operator signal the metric exists to provide.

**Suggested fix:** Distinguish errors.Is(err, context.DeadlineExceeded) (true timeout) from other ping errors; record a separate result label such as 'error' and skip the PolicyViolation close for already-dead conns.

#### 225. [LOW] WithPongTimeout without WithPingInterval is silently inert
`httpx/websocket/options.go:175` — api-design — conf 0.75 — _reviewer-reported (unverified)_

WithPongTimeout panics aggressively on non-positive values 'so misconfiguration surfaces at startup', yet setting it without WithPingInterval configures nothing — Handle only spawns the heartbeat when pingInterval > 0, so the pong timeout is silently dropped. This is exactly the silent-misconfiguration class the package's other options panic to prevent.

**Suggested fix:** Panic (or warn) in Handle when pongTimeout is set while pingInterval is zero, mirroring the package's fail-fast convention.

#### 226. [INFO] Estimate header is forwarded to the upstream provider
`httpx/budget/budget.go:244` — security — conf 0.50 — _reviewer-reported (unverified)_

The wrapper reads the caller-set estimate header but does not strip it before base.RoundTrip, so the internal accounting hint (e.g. X-Estimated-Tokens) is transmitted to the third-party provider on every request. Harmless for most providers but leaks internal budgeting signals and may collide with provider-reserved headers.

**Suggested fix:** Strip the configured estimate header from the outbound request after reading it (clone request per RoundTripper contract).

#### 227. [INFO] WriteValidationError never uses its logger parameter
`httpx/error_handler.go:120` — api-design — conf 0.85 — _reviewer-reported (unverified)_

The exported function accepts `logger *slog.Logger` but the body never references it; WriteJSON is called with r=nil so write failures log to slog.Default. Callers (WriteServiceError, ParsePathID with nil) pass loggers expecting them to matter. Removing the parameter is breaking; wiring it up is not.

**Suggested fix:** Pass the logger into the WriteJSON write-failure path (e.g. via a request-aware variant) so the parameter has effect.

#### 228. [INFO] NewServer doc claims unconditional HTTP/2 hardening; it is TLS-gated
`httpx/httpx.go:267` — docs — conf 0.70 — _reviewer-reported (unverified)_

The doc comment states 'The returned server has HTTP/2 hardening installed via http2.ConfigureServer' and doc.go repeats it, but the code only runs ConfigureServer when srv.TLSConfig != nil (line 312). Plaintext servers behind TLS-terminating load balancers using h2c get none of the frame-size/stream pins, contrary to what an operator reading the doc would conclude.

**Suggested fix:** Amend the doc: hardening applies only when WithTLSConfig is set; h2c deployments must pin limits themselves.

#### 229. [INFO] Default ErrorLog pins slog handler captured at construction time
`httpx/httpx.go:291` — correctness — conf 0.55 — _reviewer-reported (unverified)_

NewServer wires ErrorLog to slog.NewLogLogger(slog.Default().Handler(), ...). The handler is captured once; if the application calls slog.SetDefault after constructing the server (a common init ordering), connection-level errors keep flowing to the old/bootstrap handler — potentially the plain-text stderr handler the kit explicitly tries to avoid per its own comment.

**Suggested fix:** Document the ordering requirement, or use a small adapter handler that resolves slog.Default() per record.

#### 230. [INFO] Decode failures are not audited while validation failures are
`httpx/mcp/mcp.go:909` — consistency — conf 0.85 — _reviewer-reported (unverified)_

An argument-decode failure returns "invalid arguments" without calling recordActionLog, whereas validate.Struct failures and destructive-gate refusals each write an Outcome=failure entry. Operators reviewing the audit log see schema-validation probes but not malformed-argument probes against the same tool — an inconsistent forensic picture for unexecuted-call attempts.

**Suggested fix:** Record a failure entry (generic reason "invalid arguments") on the decode-failure path, or document the asymmetry.

#### 231. [INFO] No dummy verification on unknown key id — timing distinguishes id existence
`httpx/middleware/apikey/apikey.go:70` — security — conf 0.60 — _reviewer-reported (unverified)_

When FindByID fails, the middleware skips key.Verify entirely, so unknown-id requests return measurably faster than known-id/bad-secret requests, despite the comment about not leaking which key ids exist. With UUID ids the enumeration value is low, but the doc claim of indistinguishability holds only for the response body, not timing.

**Suggested fix:** On repository miss, run Verify against a fixed dummy key to equalize timing, or soften the doc claim.

#### 232. [INFO] 401 responses omit the WWW-Authenticate challenge required by RFC 7235
`httpx/middleware/auth/auth.go:228` — api-design — conf 0.80 — _reviewer-reported (unverified)_

jwtOnlyHandler, s2sHandler, and Strategy all return 401 via httpx.WriteError without a WWW-Authenticate header. RFC 7235 §4.1 says a server generating 401 MUST send WWW-Authenticate; RFC 6750 defines `Bearer` challenges with error codes. Standard HTTP clients and SDKs key re-auth behavior off this header.

**Suggested fix:** Set WWW-Authenticate: Bearer (optionally with error="invalid_token") before writing 401 in the auth middlewares — additive, no API break.

#### 233. [INFO] Default cookie name forgoes __Host- prefix protection against subdomain planting
`httpx/middleware/csrf/csrf.go:32` — security — conf 0.70 — _reviewer-reported (unverified)_

WithAllowedOrigins' own rationale (lines 211-215) is that a sibling subdomain can Set-Cookie overwrite the CSRF cookie. The default config (Secure=true, Path=/, no Domain) already satisfies the __Host- prefix requirements, which would make browsers reject subdomain-planted cookies outright — a stronger, browser-enforced defense than allowlisting alone. Default name '__csrf' gets no prefix semantics. Renaming the default silently invalidates existing deployed cookies, so it cannot just be flipped.

**Suggested fix:** Add an additive option (e.g., WithHostPrefixedCookie) or document recommending WithCookieName("__Host-csrf") for production; renaming the default is a v3 candidate.

#### 234. [INFO] WithSkipCheck docs omit that WithAllowedOrigins still 403s non-browser API clients
`httpx/middleware/csrf/csrf.go:298` — api-design — conf 0.80 — _reviewer-reported (unverified)_

Origin allowlist intentionally runs before the skip predicate (lines 480-493, tested as the M-9 fix). Consequence: non-browser clients (curl, mobile, server-to-server) send neither Origin nor Referer, so with both options configured, every bearer/API-key request is 403 'untrusted origin' — exactly the traffic WithSkipCheck's 'Common use' paragraph says the option enables. Behavior is documented on WithAllowedOrigins but the conflict is invisible from WithSkipCheck's docs.

**Suggested fix:** Add a sentence to WithSkipCheck (and WithAllowedOrigins) docs: when both are set, header-authenticated clients must still send an allow-listed Origin or Referer.

#### 235. [INFO] canonicalQuery's manual sort is redundant — url.Values.Encode already sorts keys
`httpx/middleware/idempotency/idempotency.go:742` — performance — conf 0.90 — _reviewer-reported (unverified)_

canonicalQuery collects keys, sorts them, and copies into a fresh url.Values before calling Encode — but Encode itself sorts keys deterministically, so the function is equivalent to v.Encode() with two extra allocations per request on the hot path of every required-method request.

**Suggested fix:** Return v.Encode() directly (behavior identical, including value-order preservation within a key).

#### 236. [INFO] Window-boundary semantics differ between Limiter and KeyedLimiter
`httpx/middleware/ratelimit/keyed.go:174` — consistency — conf 0.85 — _reviewer-reported (unverified)_

Limiter resets when elapsed >= window (window is [t, t+w)), KeyedLimiter resets only when now.After(windowEnd) (window is [t, t+w] inclusive). At the exact boundary instant the IP limiter allows a fresh window while the keyed limiter still counts against the old one. Observable with fake clocks; cosmetic with wall clocks, but the two siblings claim matching behavior.

**Suggested fix:** Align keyed reset to !now.Before(entry.windowEnd) or document the inclusive boundary.

#### 237. [INFO] WithTrustedProxies panic deliberately omits the error but could safely include the failing index
`httpx/middleware/ratelimit/ratelimit.go:85` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

The panic discards ParseTrustedProxiesStrict's error entirely; tests confirm withholding the raw value is intentional (secret hygiene). But the operator gets no hint which of N entries is bad. The entry index (not the value) leaks nothing. Same pattern in normalizeLimiterName (metrics.go:197) and clientip.ParseTrustedProxies.

**Suggested fix:** Panic with the offending entry's index, e.g. "invalid trusted proxy at index 3", keeping the value redacted.

#### 238. [INFO] tenant middleware does not guard the user-supplied limiter against panics, unlike KeyedMiddleware's keyFunc guard
`httpx/middleware/ratelimit/tenant/tenant.go:69` — consistency — conf 0.65 — _reviewer-reported (unverified)_

KeyedMiddleware wraps the caller-supplied keyFunc in safeRateLimitKey with recover+503; tenant.New invokes the caller-supplied ratelimit.Limiter.Allow directly, so a panicking limiter implementation propagates and relies entirely on an outer recover middleware being installed. Inconsistent hardening posture for equivalent caller-supplied extension points in the same middleware family.

**Suggested fix:** Wrap lim.Allow in a recover-to-500 helper mirroring safeRateLimitKey, or document the reliance on the recover middleware.

#### 239. [INFO] doc.go understates the default CSP
`httpx/middleware/secheaders/doc.go:11` — docs — conf 0.90 — _reviewer-reported (unverified)_

Package doc lists the default as "Content-Security-Policy: default-src 'none'" but the actual default in New() (secheaders.go:256) is "default-src 'none'; frame-ancestors 'none'". The New() doc comment is correct; doc.go drifted. Minor, but doc.go is the rendered package overview operators read first.

**Suggested fix:** Update doc.go line 11 to include "; frame-ancestors 'none'".

#### 240. [INFO] Nonce keyspace is global across key IDs; doc rationale only covers random collisions
`httpx/middleware/signedrequest/redis/doc.go:35` — security — conf 0.50 — _reviewer-reported (unverified)_

Keys are prefix+nonce with no key-ID scoping, and the doc asserts "collisions across callers are statistically impossible — there is no need to partition by key id". That argument only covers honest random nonces: any holder of a different valid key who learns another caller's nonce pre-delivery (header-logging proxy, shared telemetry) can sign their own request with it and burn it, causing the victim's request to 401 as a replay. Narrow preconditions, but the documented reasoning is incomplete.

**Suggested fix:** Scope stored nonces by key ID (hash(keyID)+":"+nonce — internal, non-breaking) or document the co-tenant nonce-squatting assumption.

#### 241. [INFO] time.Unix overflow misclassifies extreme future timestamps as expired
`httpx/middleware/signedrequest/signedrequest.go:428` — correctness — conf 0.60 — _reviewer-reported (unverified)_

For tsUnix near math.MaxInt64, time.Unix's internal +unixToInternal addition wraps, producing a far-past time, so classifyTimestampSkew returns -1 (expired) for a maximally-future timestamp; metrics count it as "expired" instead of "clock_skew". Rejection still occurs (400) — the existing TestVerify_RejectsExtremeTimestampWithoutOverflow only asserts the status code, so the direction label is wrong but the security outcome is unaffected.

**Suggested fix:** Bound tsUnix to a sane range (e.g. |tsUnix| < 1<<40) before calling time.Unix, or compare raw unix seconds.

#### 242. [INFO] Extractor-panic logging bypasses service logger via slog.Default()
`httpx/middleware/tenant/tenant.go:245` — consistency — conf 0.70 — _reviewer-reported (unverified)_

safeExtractTenant logs panics with slog.Default() and a full debug.Stack(). The package exposes no logger option and ignores the request-scoped logger that the stack middleware installs (httpx.Logger pattern used by sibling packages), so panic diagnostics bypass the service's configured handler/sink and formatting.

**Suggested fix:** Resolve the logger from the request context (httpx.Logger(r.Context(), slog.Default())) or add a WithLogger option.

#### 243. [INFO] Garbled panic message in WriteHeader
`httpx/middleware/timeout/writer.go:69` — docs — conf 0.95 — _reviewer-reported (unverified)_

Panic text reads "middleware/timeout: WriteHeader writer WriteHeader received invalid status code" — duplicated/garbled wording that will end up in production panic logs and crash reports.

**Suggested fix:** Change to "middleware/timeout: WriteHeader received invalid status code".

#### 244. [INFO] RoundTrip mutates the caller's request (Body, ContentLength), bending the RoundTripper contract
`httpx/sign/sign.go:313` — api-design — conf 0.70 — _reviewer-reported (unverified)_

To fix FR-023 the transport replaces req.Body with a buffered reader and overwrites req.ContentLength on the original request (asserted in TestSign_PreservesCallerBodyAfterRoundTrip:444). http.RoundTripper documents that implementations "should not modify the request, except for reading and closing the Body". The mutation is benign and deliberate here, but callers composing this with other contract-relying wrappers (or reusing the request concurrently) may be surprised; the behaviour is only documented in an internal comment.

**Suggested fix:** Document the body-restoration side effect in the Wrap/WrapKeyStore doc comments.


### grpcx — gRPC server, client, interceptors

_16 findings — 2 high, 7 medium, 6 low, 1 info_

#### 245. [HIGH] DeadlineStream docstring falsely claims setup-only bounding; default 30s deadline kills all long-lived streams
`grpcx/client/interceptor/deadline.go:37` — api-design — conf 0.80 — _reviewer-reported (unverified)_

Doc says it "bounds stream setup time" and "Stream RPCs after setup are not deadline-bounded by this interceptor", but boundedCtx applies context.WithTimeout(parent, d) whose ctx governs the ENTIRE stream lifetime in gRPC. With client.NewClient defaults (DeadlineStream(30s) auto-installed) every stream — including bidi/watch — is aborted with DeadlineExceeded after 30s. The inline comment (line 53) even admits the ctx "bounds the whole stream", and the claimed mirror of server-side "bound the setup, not the body" is also false: the server interceptor bounds the whole handler too.

**Suggested fix:** Fix the docstring to state the whole stream is bounded, or branch on desc to skip bounding bidi/long-lived streams; loudly document WithoutDefaultDeadline for streaming.

#### 246. [HIGH] StreamIdleTimeout cannot terminate streams blocked in RecvMsg — cancellation is cooperative-only
`grpcx/interceptor/stream_limits.go:142` — correctness — conf 0.75 — _reviewer-reported (unverified)_

The watchdog cancels a context derived via context.WithCancel(ss.Context()). grpc-go's transport-level RecvMsg blocks on the real stream context, which is the parent; cancelling a child never unblocks it. The canonical handler pattern `for { stream.Recv() }` with a paused client stays blocked forever, so the advertised GAP-03 mitigation (reaping idle streams, bounding goroutines) does not work for the most common handler shape. Only handlers that explicitly select on ss.Context().Done() benefit.

**Suggested fix:** Document the cooperative contract prominently and pair with keepalive/MaxConnectionAge server options; or have the wrapper run Recv via a mechanism that can abort (not possible in-interceptor — document as limitation).

#### 247. [MEDIUM] Correlation/request-ID propagation is a side effect of the logging interceptor; WithoutLogging silently breaks it
`grpcx/client/interceptor/logging.go:43` — api-design — conf 0.75 — _reviewer-reported (unverified)_

injectIDs (copying contextutil correlation/request IDs into outgoing metadata) runs only inside LoggingUnary/LoggingStream. No other interceptor injects them, so client.WithoutLogging() — documented merely as disabling log lines — also silently disables end-to-end ID propagation to the server. Cross-service trace joins break with no indication.

**Suggested fix:** Extract injectIDs into a dedicated always-on propagation interceptor in NewClient; document the coupling on WithoutLogging until then.

#### 248. [MEDIUM] Client-side interceptors largely untested: only DeadlineUnary has direct unit tests
`grpcx/client/interceptor/recovery.go:23` — testing — conf 0.80 — _reviewer-reported (unverified)_

In grpcx/client/interceptor the sole test file is deadline_test.go covering DeadlineUnary. RecoveryUnary/RecoveryStream, LoggingUnary/LoggingStream (including injectIDs metadata propagation), DeadlineStream and its boundedClientStream cancel-on-RecvMsg semantics, and RetryUnary have no direct tests; client_test.go only indirectly exercises retry and metrics. The subtle stream-cancel lifecycle and panic-to-codes.Internal conversion are unverified.

**Suggested fix:** Add unit tests per interceptor, especially boundedClientStream cancel timing, injectIDs end-to-end metadata, and recovery panic conversion.

#### 249. [MEDIUM] Default retry on ResourceExhausted retries permanent failures (oversized payloads, kit PayloadTooLarge)
`grpcx/client/interceptor/retry.go:28` — correctness — conf 0.70 — _reviewer-reported (unverified)_

DefaultRetryableCodes includes codes.ResourceExhausted for rate-limit recovery, but grpc-go also returns ResourceExhausted for messages exceeding MaxRecv/SendMsgSize, and the kit's own server maps apperror.CodePayloadTooLarge and CodeStorageFull to ResourceExhausted (grpcx/apperror_status.go:27,35). A client sending an oversized request will burn the full retry budget (default 3 retries, 1s/2s/4s backoff) on a guaranteed-permanent error.

**Suggested fix:** Drop ResourceExhausted from the default set, or inspect the status message/details to skip msg-size and payload-too-large causes.

#### 250. [MEDIUM] mTLS S2S success path of MTLSAuthUnary/MTLSAuthStream is never tested
`grpcx/interceptor/auth_test.go:747` — testing — conf 0.80 — _reviewer-reported (unverified)_

Every MTLSAuth test asserts a rejection (no cert, CA cert, server-only EKU, bad/duplicate metadata, guard error/panic) or the JWT branch. No test anywhere in the repo (grep confirms no callers outside this directory) drives the full mTLS branch to success: guard approves, handler runs, IsTrustedS2S(ctx)==true, UserID(ctx) set. verifyClientCertGRPC success is unit-tested in isolation, but the authenticateMTLSOrJWT composition — the feature's core contract — is unverified, including the stream variant's contextStream wrapping.

**Suggested fix:** Add unary and stream tests with a matching cert + valid x-user-id asserting handler invocation, IsTrustedS2S true, and UserID equal to the impersonated UUID.

#### 251. [MEDIUM] authtest-gated trusted-S2S bypass tests never run in CI
`grpcx/interceptor/auth_trusted_s2s_test.go:1` — testing — conf 0.85 — _reviewer-reported (unverified)_

The only positive-path tests for the RBAC/scope bypass (RequirePermission*/RequireScope* honoring the trusted-S2S marker) live under the `authtest` build tag. No Makefile target or CI workflow passes -tags authtest (verified: make ci -> test-race -> `go test -race ./...`; repo-wide grep finds no runner). A regression in checkPermission/checkScope's IsTrustedS2S branch — security-critical authorization logic — would ship undetected. Same gap exists for the httpx mirror.

**Suggested fix:** Add a Makefile/CI step running `go test -tags authtest ./...` for grpcx and httpx auth packages, wired into `make ci`.

#### 252. [MEDIUM] close(done) not deferred — handler panic leaves watchdog goroutine and ticker running up to full idle duration
`grpcx/interceptor/stream_limits.go:186` — resource-leak — conf 0.85 — _reviewer-reported (unverified)_

close(done) is a plain statement after handler(srv, wrapped). With RecoveryStream as the recommended outermost interceptor, a handler panic unwinds past close(done); only the deferred cancel() runs, which the watchdog does not observe (it selects only on done and the ticker). The watchdog plus ticker linger until time.Since(lastActive)>=d — minutes for typical configs — then spuriously increments metrics.idleClose. Repeated panics multiply lingering goroutines.

**Suggested fix:** Use `defer close(done)` (idempotent via sync.Once or restructure), and/or add `case <-ctx.Done(): return` to the watchdog select.

#### 253. [MEDIUM] Idle-fire translation overwrites successful completion and business errors with DeadlineExceeded
`grpcx/interceptor/stream_limits.go:192` — correctness — conf 0.80 — _reviewer-reported (unverified)_

If the watchdog cancels (e.g., handler computes >d after the last Recv, ignoring ctx) and the handler then returns nil or a business error, the translation block unconditionally returns codes.DeadlineExceeded because ctx.Err()==Canceled and ss.Context().Err()==nil. A client-streaming RPC whose response was already sent via SendAndClose is reported failed; client retries can duplicate side effects. metrics.idleClose also increments though no stream was actually closed.

**Suggested fix:** Only translate when err is non-nil and errors.Is(err, context.Canceled); propagate handler results otherwise. Increment idleClose only when the translation actually applies.

#### 254. [LOW] Package doc's recommended chain order contradicts NewServer's actual auto-chain
`grpcx/doc.go:14` — docs — conf 0.70 — _reviewer-reported (unverified)_

doc.go recommends recovery → metrics → logging → auth and justifies it with "metrics record every call (including auth failures)", but NewServer builds recovery → logging → metrics → deadline (server.go:270 and implementation). Readers composing custom chains get guidance inconsistent with what the kit itself installs.

**Suggested fix:** Align doc.go with the actual NewServer chain or explain why the manual recommendation differs.

#### 255. [LOW] Check doc contradicts implementation on unrecognized service names
`grpcx/health.go:46` — docs — conf 0.85 — _reviewer-reported (unverified)_

Method comment says "An empty or unrecognized service name checks overall health" but the code returns codes.NotFound for ANY non-empty service (line 49-51), and the very next sentence says named services return NOT_FOUND. The implementation follows the gRPC health spec; the first sentence is wrong and could mislead probe configuration.

**Suggested fix:** Reword: empty service checks overall health; any named service returns NOT_FOUND.

#### 256. [LOW] URI SAN matching is case-sensitive on host while DNS SAN matching is case-insensitive
`grpcx/interceptor/auth.go:699` — correctness — conf 0.70 — _reviewer-reported (unverified)_

Cert DNS names are lowercased before lookup and NormalizeSAN lowercases DNS allowlist entries, but URI SANs are compared via exact u.String() equality; url.Parse lowercases only the scheme, not the host. An allowlist entry like spiffe://Example.org/svc never matches a cert URI with lowercase host (and vice versa), producing a silent fail-closed authorization outage that is hard to diagnose. SPIFFE mandates lowercase trust domains, but mixed-case operator input is accepted without normalization or warning.

**Suggested fix:** Lowercase the URI host in NormalizeSAN and when stringifying cert URIs before comparison, mirroring DNS handling.

#### 257. [LOW] DeadlineStream's derived deadline cannot abort transport-blocked stream handlers; header doc overclaims
`grpcx/interceptor/deadline.go:60` — docs — conf 0.70 — _reviewer-reported (unverified)_

The file header claims the interceptor prevents a crashed/misbehaving client from holding a handler open indefinitely (GAP-03). For streams, context.WithDeadline on a child of ss.Context() expires without affecting the underlying transport; a handler blocked in Recv on the real stream never observes it, same cooperative-only limitation as StreamIdleTimeout. Protection only applies to handlers that propagate/check ss.Context().

**Suggested fix:** Document the cooperative contract in the DeadlineStream doc comment and recommend pairing with grpc keepalive enforcement for non-cooperative cases.

#### 258. [LOW] tryRegister duplicates promutil.MustRegisterOrGet with worse failure diagnostics
`grpcx/interceptor/metrics.go:150` — consistency — conf 0.85 — _reviewer-reported (unverified)_

stream_limits.go in the same package uses promutil.MustRegisterOrGet, while metrics.go keeps a local tryRegister that panics with a constant string, discarding the underlying registration error (name/label conflicts become undiagnosable), and follows with unchecked type assertions (.(*prometheus.CounterVec)) that would panic with a bare interface-conversion message if a differently-typed equivalent collector were returned. promutil's helper reports both the error and the type mismatch.

**Suggested fix:** Replace tryRegister with promutil.MustRegisterOrGet in NewMetrics, matching stream_limits.go.

#### 259. [LOW] Dead test scaffolding: recvBlockCh/recvMsgCount never used; blocked-RecvMsg idle scenario untested
`grpcx/interceptor/stream_limits_test.go:28` — testing — conf 0.85 — _reviewer-reported (unverified)_

fakeServerStream.recvBlockCh is defined with blocking logic in RecvMsg but no test ever sets it; recvMsgCount is never asserted. The missing test is precisely the advertised threat scenario — a handler blocked inside RecvMsg while the client pauses — which would expose that the idle watchdog cannot unblock it (the existing CancelsIdleStream test uses a ctx-aware handler instead).

**Suggested fix:** Add a test with recvBlockCh set and a handler blocked in Recv; assert actual behavior, then either fix the interceptor or document the limitation. Remove scaffolding if unused.

#### 260. [INFO] Retry wraps deadline, so each attempt gets a fresh default 30s budget
`grpcx/client/client.go:224` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Chain is retry -> deadline, so DeadlineUnary re-applies now+30s per attempt. With WithRetry(retry.DefaultPolicy()) and no caller deadline, a call can run ~3x30s plus 1s/2s/4s backoff (~97s total), despite DefaultClientDeadline's doc claiming client and server "agree on the default unary-call timeout". Per-attempt deadlines are defensible, but the overall-budget behavior is undocumented.

**Suggested fix:** Document the per-attempt semantics on WithRetry/DefaultClientDeadline, or add an overall MaxElapsedTime default when retry is enabled.


### data — stores: idempotency, lock, queue, cache, approval, budget, ratelimit, saga

_90 findings — 6 high, 27 medium, 50 low, 7 info_

#### 261. [HIGH] OccurredAt signed at ns precision but persisted at µs — every postgres entry fails verification
`data/actionlog/actionlog.go:579` — correctness — conf 0.85 — _reviewer-reported (unverified)_

canonicalForm signs OccurredAt as RFC3339Nano (full nanoseconds); Append never truncates (line 579 only calls UTC()). The postgres store column is TIMESTAMPTZ and pgx v5.10.0 truncates sub-microsecond nanoseconds on encode (pgtype/timestamptz.go:177,207). On Linux time.Now() has ns resolution, so ~99.9% of entries read back with a different canonical form and Get/List/VerifyChain return ErrSignatureInvalid — the audit log is unreadable through the verified API. Masked by µs-granular macOS clocks; ci.yml never runs the integration tests.

**Suggested fix:** In Append, set e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond) before signing (additive, applies to all stores); add a Linux-realistic integration assertion with a ns-precision WithClock.

#### 262. [HIGH] scanEntry decodes metadata numbers as float64 — int64 > 2^53 breaks signatures permanently
`data/actionlog/postgres/store.go:297` — correctness — conf 0.80 — _reviewer-reported (unverified)_

validMetadata accepts int64/uint64 of any magnitude; the signature is computed over their exact decimal form. scanEntry uses plain json.Unmarshal, so on read every number becomes float64: 1234567890123456789 round-trips as 1234567890123456800, the recomputed canonical form differs, and Get/List/VerifyChain return ErrSignatureInvalid forever for that row. Snowflake IDs and unix-nano timestamps in metadata are common. JSONB also normalizes json.Number forms like "1e2" to "100", same failure. Memory store is unaffected — store behavior diverges.

**Suggested fix:** Decode with json.Decoder.UseNumber() in scanEntry; additionally reject or normalize json.Number scientific notation and integers JSONB/JSON cannot round-trip at validMetadata time.

#### 263. [HIGH] ApplyTo passes DB-sourced spec/name to Scheduler.Add, which panics on invalid input
`data/cron/pgstore/store.go:217` — correctness — conf 0.85 — _reviewer-reported (unverified)_

runtime/cron Scheduler.Add panics when the schedule fails cron.AddFunc parsing or the name fails promutil.ValidateStaticLabelValue (cron.go:154-168). ApplyTo feeds it rec.Spec/rec.Name read straight from Postgres. Store.validate never parses the spec (only non-empty, <=128 chars), and doc.go explicitly tells operators to INSERT rows via raw SQL, bypassing validation entirely. One malformed row crashes the service during startup ApplyTo — a persistent crash loop until the row is fixed.

**Suggested fix:** In ApplyTo, validate spec with the cron parser (and name) before Add, collecting bad records into an error/skip list instead of panicking.

#### 264. [HIGH] Failed Release returns pooled conn with advisory lock still held — permanent lock leak
`data/lock/pgadvisory/pgadvisory.go:173` — resource-leak — conf 0.78 — _reviewer-reported (unverified)_

doRelease defers s.conn.Close(), which returns the session connection to the pool. If pg_advisory_unlock never executes (most trivially: Release called with an already-canceled/deadline-expired ctx — the common `defer lk.Release(ctx)` pattern), the session keeps the advisory lock. Session locks have no TTL, so the key is wedged until the pooled connection happens to die (no ConnMaxLifetime by default). redislock deliberately detaches the release context (context.WithoutCancel); pgadvisory has no such guard and no doc warning.

**Suggested fix:** On unlock failure, force-discard the session via conn.Raw returning driver.ErrBadConn (PG then frees the lock), and/or use a detached timeout context for the unlock query like redislock does.

#### 265. [HIGH] Queue.Close always returns a non-nil error
`data/queue/redisqueue/queue.go:546` — error-handling — conf 0.93 — _reviewer-reported (unverified)_

client and inspector are always built via asynq.NewClientFromRedisClient/NewInspectorFromRedisClient, which set sharedConnection=true. In asynq v0.26.0 both Close() methods unconditionally return "redis connection is shared so the Client can't be closed through asynq" in that mode. So Queue.Close returns an error on every call; it also frees nothing. All repo tests discard Close errors (`_ = q.Close()`), hiding this. Any caller checking Close in shutdown wiring fails every time.

**Suggested fix:** Make Close a documented no-op returning nil (connection is caller-owned), or filter the shared-connection sentinel before propagating.

#### 266. [HIGH] Every handler invocation gets a hidden, unconfigurable 30s deadline
`data/stream/redisstream/consumer.go:686` — correctness — conf 0.78 — _reviewer-reported (unverified)_

In the normal (non-cancelled) path handleMessage wraps the handler context with context.WithTimeout(ctx, handlerShutdownTimeout=30s). The constant is documented only as a shutdown grace period, and no option exists to change it. Any ctx-respecting handler legitimately running >30s (plausible in this kit's agentic/LLM workloads) systematically fails, is retried, and after maxRetries is dead-lettered. Neither Consume's godoc nor doc.go mentions any handler deadline.

**Suggested fix:** Add WithHandlerTimeout option (default 30s) and document the deadline in Consume/doc.go; additive, no breaking change.

#### 267. [MEDIUM] ValidateForCreate allows forged decision/audit metadata on pending requests
`data/approval/approval.go:277` — correctness — conf 0.88 — _reviewer-reported (unverified)_

ValidateForCreate never rejects non-empty DecidedBy, non-zero DecidedAt, or an arbitrary CreatedAt. Both stores persist these on Create: memory stores r as-is; postgres INSERTs decided_by and decided_at directly (store.go:86-88). A direct Store caller can create a StatePending request that already shows a decider and decision timestamp in Get/List output — audit-trail pollution in a package whose whole purpose is a trustworthy approval record. CreatedAt can also be future-dated to manipulate List ordering/pagination.

**Suggested fix:** In ValidateForCreate, reject DecidedBy != "" and !DecidedAt.IsZero(); consider bounding CreatedAt (e.g. not after ExpiresAt, not in future). Additive, no API break.

#### 268. [MEDIUM] TenantStore security-critical mutation paths are untested
`data/approval/tenantstore_test.go:37` — testing — conf 0.85 — _reviewer-reported (unverified)_

Tests cover only Create's TenantID override and Get's cross-tenant ErrNotFound. The IDOR-fix paths this wrapper exists for (FR-054) — Approve/Reject/MarkExecuted returning ErrNotFound for another tenant's request, List forcing TenantID and clearing AllTenants, and the post-write ErrTenantMismatch tripwire — have zero coverage. The invalid-receiver test also omits Reject. A regression in decideTenant's ownership check would ship silently.

**Suggested fix:** Add tests: Approve/Reject/MarkExecuted on tenant-b-request expect ErrNotFound; List with AllTenants=true is rescoped; mutate inner tenant mid-op to hit ErrTenantMismatch; include Reject in receiver test.

#### 269. [MEDIUM] Sweeper can delete a bucket mid-Consume, losing the charge and granting a fresh full cap
`data/budget/memory/memory.go:201` — concurrency — conf 0.72 — _reviewer-reported (unverified)_

sweep() reads bk.periodN and CompareAndDeletes without taking bk.mu. Consume's post-lock map recheck only guards eviction before the lock: sweeper can read stale periodN, then delete after Consume's recheck while Consume rolls the bucket to the current period and increments used. The charge lands on an orphaned bucket; the next Consume creates a fresh bucket with used=0, allowing cap+amount spend in one window. The sweep comment claiming this cannot happen is false. Refund has the same exposure (no recheck loop at all).

**Suggested fix:** In sweep(), lock bk.mu around the periodN check and CompareAndDelete (mirroring tokenbucket's mutex-guarded sweep); add Consume-style live-entry recheck to Refund.

#### 270. [MEDIUM] Close/Wait do not drain singleflight leader compute goroutines abandoned by all waiters
`data/cache/compute.go:338` — concurrency — conf 0.85 — _reviewer-reported (unverified)_

foregroundWg tracks the calling goroutine, not the DoChan-spawned leader closure (x/sync singleflight runs fn in its own goroutine). When the caller exits early via ctx.Done()/bgCtx.Done() (lines 381-396), foregroundWg.Done() runs while executeCompute is still in flight, so Close()/Wait() return before the compute finishes. A ctx-ignoring or slow fn keeps running and calls backend.Set after Close, contradicting the comment 'Each leader goroutine is tracked in foregroundWg so Close can wait for it to drain'.

**Suggested fix:** Add/Done a WaitGroup inside the DoChan closure itself (guarded by bgMu like triggerBackgroundRefresh), or spawn a tracked waiter goroutine on resCh when abandoning.

#### 271. [MEDIUM] WithMaxSize does not clear costFunc, breaking its documented 'Implies WithEntryCost' contract
`data/cache/memory_cache.go:72` — correctness — conf 0.80 — _reviewer-reported (unverified)_

WithEntryCost sets costFunc=nil, but WithMaxSize only sets entryCost=true. NewMemoryCache(WithByteCost(), WithMaxSize(100)) (either order) leaves costFunc=len(value) while maxCost=100, so the cache is bounded at 100 BYTES instead of 100 entries — nearly every Set returns ErrAdmissionRejected. Violates both the 'Implies WithEntryCost' doc on WithMaxSize and the 'last option wins' doc on WithEntryCost.

**Suggested fix:** Set mc.costFunc = nil inside WithMaxSize's closure, mirroring WithEntryCost.

#### 272. [MEDIUM] SetNX existence check misses just-written plain Set values still in ristretto's write buffer
`data/cache/memory_cache.go:461` — correctness — conf 0.65 — _reviewer-reported (unverified)_

ristretto SetWithTTL for a NEW key only enqueues to setBuf (verified v2.4.0: storedItems.Update applies immediately only for updates), so a sequential Set(k) followed by SetNX(k) — without Sync — sees cache.Get(k) miss, returns true, and overwrites the value. Redis SetNX would return false here. nxClaims only covers prior SetNX writes, not plain Set, so the documented in-process test-and-set semantics are violated for the Set→SetNX sequence.

**Suggested fix:** Call mc.cache.Wait() before the existence check at line 461 (already done after the write at 471).

#### 273. [MEDIUM] Close doc falsely claims forgotten Close is 'not a goroutine leak' — ristretto internals leak forever
`data/cache/memory_cache.go:533` — resource-leak — conf 0.85 — _reviewer-reported (unverified)_

The weak-pointer design only rescues the nxClaims sweeper. ristretto v2.4.0 NewCache starts a processItems goroutine and a cleanupTicker (verified in module source; no runtime finalizer exists), and that goroutine strongly references the ristretto cache, so it runs for the process lifetime if Close is never called. The Close comment "'Forgetting Close' is a deterministic-cleanup bug, not a goroutine leak" is wrong and invites real leaks (many tests in this package already skip Close).

**Suggested fix:** Correct the doc, or add a weak-pointer watchdog that calls cache.Close() when the MemoryCache becomes unreachable (e.g. runtime.AddCleanup).

#### 274. [MEDIUM] Capped MGet fails the entire batch on a single per-key STRLEN/GET error (e.g. WRONGTYPE)
`data/cache/rediscache/cache.go:361` — correctness — conf 0.60 — _reviewer-reported (unverified)_

With a cap configured, a WRONGTYPE error from STRLEN (key holding a list/hash) at line 359-361, or a non-Nil GET error at line 388-390, aborts the whole MGet. The uncapped path uses MGET, which returns nil for wrong-typed keys and treats them as misses. This contradicts the function's own comment that 'failing the whole batch on a single poisoned entry would let one hostile co-tenant deny the entire request' — a co-tenant who plants one wrong-typed key DoSes every capped batch containing it.

**Suggested fix:** Treat per-key WRONGTYPE/command errors as misses (skip + miss metric) in the capped path, matching MGET semantics; only fail on pipeline/transport errors.

#### 275. [MEDIUM] DegradedCache silently downgrades SetNX/MGet/MSet to racy per-key fallbacks
`data/cache/rediscache/degraded_cache.go:21` — api-design — conf 0.80 — _reviewer-reported (unverified)_

DegradedCache implements only cache.Cache, not cache.BulkCache, although its primary (*rediscache.Cache) implements all three bulk methods. Callers using the cache.SetNX free function on a DegradedCache hit the documented 'racy Exists+Set' fallback, silently losing cross-process compute-once atomicity; MGet/MSet lose pipelining. tenant.Wrap was explicitly fixed for this exact bug in the v2 audit (tenant_test.go:229-233), but DegradedCache retains it.

**Suggested fix:** Add MGet/MSet/SetNX to DegradedCache delegating to primary when healthy and policy/fallback when degraded (additive; mirrors tenant.scopedBulk).

#### 276. [MEDIUM] Add/Upsert persist cron specs without parsing them
`data/cron/pgstore/store.go:227` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

validate() checks only that Spec is non-empty and <=128 bytes. An unparseable spec (e.g. 'every day') is accepted into the table and only detected later — currently as a panic in ApplyTo via Scheduler.Add. Failure is deferred from write time (where the operator/API caller could be told) to the next restart of every consumer binary.

**Suggested fix:** Parse Spec with robfig/cron's parser in validate() so Add/Upsert reject invalid schedules at write time (additive change).

#### 277. [MEDIUM] ApplyTo has zero test coverage anywhere in the repo
`data/cron/pgstore/store_test.go:110` — testing — conf 0.90 — _reviewer-reported (unverified)_

The comment says SQL-roundtrip tests including ApplyTo 'belong under //go:build integration', and testing/integrationtest/cronpg/doc.go claims 'ApplyTo round-trip' coverage, but the cronpg integration tests contain only Add/Upsert/Enable/Get/Remove tests — no test references ApplyTo (verified via repo-wide grep). The enabled-filtering, unknown-name collection, and nil-arg paths are all untested, and the panic-on-bad-spec behavior went unnoticed.

**Suggested fix:** Add ApplyTo tests (unit with a fake scheduler is feasible, or cronpg integration) covering enabled filtering, unknown names, and invalid specs.

#### 278. [MEDIUM] memoryStoreMaxEntries is not a cap: MemoryStore grows unbounded under live keys and TryLock churn
`data/idempotency/idempotency.go:440` — resource-leak — conf 0.70 — _reviewer-reported (unverified)_

The constant's comment claims it 'caps the in-memory store' and 'prevents unbounded memory growth', but when len(items) >= 10k Set merely runs sweepExpiredLocked(256), which deletes only EXPIRED entries, then inserts anyway — live long-TTL entries grow without bound. Worse, m.locks is never swept by TryLock at all; abandoned locks (handler crash without Unlock) accumulate until a Set-path sweep, whose 256-entry budget is consumed by the items map first (sweep scans items before locks).

**Suggested fix:** Enforce a real cap (reject or evict-oldest) or correct the comment; sweep locks in TryLock and give locks a separate sweep budget.

#### 279. [MEDIUM] Conformance suite does not cover contracts its doc claims (Set TTL<=0, oversized key, Unlock empty key)
`data/idempotency/idempotencytest/conformance.go:58` — testing — conf 0.90 — _reviewer-reported (unverified)_

doc.go claims the suite covers 'TTL <= 0 rejection on both TryLock and Set' and 'Empty / oversized key rejection', but testRejectsInvalidTTL exercises only TryLock, testRejectsEmptyKey omits Unlock, and no test passes an oversized (>MaxKeyLen) key. A third-party backend that creates an instantly-expired row on Set(ttl=0) — the exact divergence class this harness exists to prevent per its own doc — would pass the battery.

**Suggested fix:** Add Set-with-ttl<=0 (after a successful TryLock), oversized-key, and Unlock-empty-key cases; or fix the doc claims.

#### 280. [MEDIUM] context.DeadlineExceeded classified as store-unavailable; context.Canceled passes through raw
`data/idempotency/redisstore/unavailable.go:86` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

isConnectionUnavailable ends with errors.As(err, &netErr); context.DeadlineExceeded implements net.Error (Timeout/Temporary), so a caller's expired request deadline is translated into ErrStoreUnavailable — documented to render as 502 + Retry-After — while context.Canceled propagates untranslated. The two caller-driven cancellation modes get inconsistent classification, and a client-side deadline is misreported as a dependency outage. The same net.Error pattern exists in jwtutil and observability/tracing, so this is systemic.

**Suggested fix:** Exclude context errors before the net.Error check: if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) pass the error through unchanged.

#### 281. [MEDIUM] Conformance suite's concurrency test asserts nothing about mutual exclusion
`data/lock/locktest/conformance.go:175` — testing — conf 0.88 — _reviewer-reported (unverified)_

testConcurrentWinner spawns 16 acquirers and asserts winners >= 1 and <= 16 — both trivially true (each goroutine increments at most once). The doc.go claims the suite covers 'Concurrent Acquire on the same key: exactly one winner', but a Locker that grants the lock to every caller simultaneously passes. The suite's central safety property (no two concurrent holders) is entirely unverified.

**Suggested fix:** Track an in-critical-section counter (atomic add/sub around the hold window) and assert it never exceeds 1; fail on observed concurrent holders.

#### 282. [MEDIUM] Acquire error path can leak a granted advisory lock into the pool
`data/lock/pgadvisory/pgadvisory.go:114` — resource-leak — conf 0.60 — _reviewer-reported (unverified)_

In doAcquire, if pg_try_advisory_lock succeeds server-side but the Scan fails client-side (ctx canceled mid-flight, response lost), the code does conn.Close(), returning the connection to the pool with the advisory lock held by that session. Depending on the driver (lib/pq keeps the conn alive after a successful cancel request), the lock leaks indefinitely and all future Acquires on that key fail as contended.

**Suggested fix:** On query error in doAcquire, discard the underlying connection (conn.Raw + driver.ErrBadConn) instead of returning it to the pool.

#### 283. [MEDIUM] WithRetry off-by-one: maxAttempts is total tries, not retry count
`data/lock/redislock/lock.go:46` — docs — conf 0.80 — _reviewer-reported (unverified)_

WithRetry docs say Acquire 'will retry at the given interval for up to maxAttempts times', implying maxAttempts+1 total attempts. tryCount maps maxAttempts directly to redsync's tries, where tries is the TOTAL attempt count (tryCount's own comment: '1 means no retry'). So WithRetry(d, 1) performs zero retries and WithRetry(d, N) performs N-1, one fewer attempt than documented. Same mismatch is mirrored in redlock (redlock.go:247).

**Suggested fix:** Either map tries = maxAttempts + 1, or fix the doc on both WithRetry functions to say maxAttempts is the total attempt count.

#### 284. [MEDIUM] WithMaxWait timeout during a final-try Redis command surfaces as backend error, not contention
`data/lock/redislock/lock.go:203` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

WithMaxWait documents that an internal timeout yields (nil, false, nil). Verified against redsync v4.16.0: lockContext returns the raw node error on the last try (`if i == tries-1 && err != nil return err`), wrapped as a RedisError multierror containing context.DeadlineExceeded. doAcquire's outer ctx.Err() is nil and isContentionError doesn't match RedisError, so the call returns a wrapped backend error. With the default single-try config, every maxWait expiry mid-command violates the documented contract. Same code in redlock.Acquire.

**Suggested fix:** After LockContext fails, check lockCtx.Err() != nil while ctx.Err() == nil (or errors.Is context.DeadlineExceeded) and return (nil, false, nil) for internal maxWait expiry.

#### 285. [MEDIUM] redlock double-Release returns nil, violating the kit's lock.Lock contract
`data/lock/redislock/redlock/redlock.go:221` — consistency — conf 0.90 — _reviewer-reported (unverified)_

handle.Release returns nil when l.released is already true. The kit's own conformance suite (locktest testDoubleReleaseLost) requires lock.ErrLockLost on a second Release, redislock and pgadvisory both return ErrLockLost, and locktest's package doc claims redlock behaves 'identically'. Callers swapping backends via the lock.Locker interface get divergent behavior; redlock is never wired into locktest.Run so the divergence is invisible. Also, a second Release returns nil even if the first attempt failed with a backend error.

**Suggested fix:** Return lock.ErrLockLost on already-released handles to match redislock/pgadvisory, update TestQuorumLocker_Release_IsIdempotent, and run redlock through locktest.Run.

#### 286. [MEDIUM] WithInvisibilityTimeout does not control re-enqueue of crashed workers
`data/queue/redisqueue/queue.go:441` — api-design — conf 0.85 — _reviewer-reported (unverified)_

Option maps to asynq.Timeout (queue.go:670), which is a handler-context deadline for a LIVE worker; exceeding it fails the attempt and consumes a retry. Crash recovery in asynq is lease/heartbeat based (recoverer.go, fixed ~30s lease) and is unaffected by this knob. Also, default invisibilityTO=0 means asynq applies its 30-MINUTE defaultTimeout (client.go:310), not the documented "asynq's upstream default (30s)". Small values kill healthy long handlers; large values do not delay crash recovery.

**Suggested fix:** Rename/redocument as handler timeout; fix the "default 30s" claim (actual: 30m handler deadline, ~30s lease recovery).

#### 287. [MEDIUM] FR-059 idempotency claim only holds while the task still exists
`data/queue/redisqueue/queue.go:567` — docs — conf 0.70 — _reviewer-reported (unverified)_

Enqueue doc says a second Enqueue with the same (queue,id) returns ErrTaskIDConflict "by default". asynq TaskID uniqueness lasts only while the task exists; with the default retentionTTL=0 a completed task is deleted immediately, so a producer retry after consumer success enqueues and executes a duplicate. The dedupe window silently depends on WithRetention, which the doc does not state.

**Suggested fix:** Document that idempotency lapses once the task completes unless WithRetention is set; consider a non-zero default retention.

#### 288. [MEDIUM] Dedupe is by full args JSON, not by Message.ID as documented
`data/queue/riverqueue/riverqueue.go:113` — correctness — conf 0.80 — _reviewer-reported (unverified)_

Enqueue doc claims "a second Enqueue with the same ID is a no-op" (FR-059). UniqueOpts{ByArgs:true, ByQueue:true} with no `river:"unique"` tags on envelopeArgs makes River hash the entire args (id+type+payload). Same ID with a different payload inserts and executes twice, defeating the idempotency-token semantics and diverging from the redisqueue sibling, which keys strictly on TaskID.

**Suggested fix:** Tag the ID field `river:"unique"` (river v0.39 supports field-scoped ByArgs) or correct the documentation.

#### 289. [MEDIUM] Advertised transactional enqueue (pgx.Tx) is not exposed by the adapter
`data/queue/riverqueue/riverqueue.go:118` — api-design — conf 0.85 — _reviewer-reported (unverified)_

Package doc (lines 9-12) sells "Atomic enqueue + business write: the Publish call accepts a pgx.Tx, so the job appears iff the transaction commits". Publisher.Enqueue only calls client.Insert (non-tx); no EnqueueTx/InsertTx path exists. Callers cannot even bypass via client.InsertTx because envelopeArgs is unexported, so no kit envelope can be inserted transactionally at all — the headline reason to pick this backend is unreachable.

**Suggested fix:** Add additive EnqueueTx(ctx, tx pgx.Tx, queue string, msg kitqueue.Message) error; meanwhile fix the package doc.

#### 290. [MEDIUM] Lua number-to-string conversion loses TAT precision (~100µs grid)
`data/ratelimit/redis/redis.go:68` — correctness — conf 0.70 — _reviewer-reported (unverified)_

The script does redis.call("SET", KEYS[1], newTat, ...) with newTat as a Lua number. Redis converts number args with %.14g; current Unix-microsecond timestamps have 16 significant digits, so the stored TAT is rounded to roughly a 100µs grid (±50µs per write). For per-key rates near or below ~1ms per event (1k+/sec) the enforced rate drifts materially; at rateUS=100 the error equals the rate. miniredis-based tests cannot catch this.

**Suggested fix:** Store via string.format("%.0f", newTat) (or tostring of an integer) instead of the raw number.

#### 291. [MEDIUM] Sweeper never evicts buckets when capacity is fractional > 1
`data/ratelimit/tokenbucket/tokenbucket.go:217` — resource-leak — conf 0.85 — _reviewer-reported (unverified)_

newBucket truncates capacity to int for rate.NewLimiter burst (e.g. 10.5 → 10), but sweep() compares b.lim.TokensAt(now) >= l.capacity using the un-truncated float. x/time/rate caps tokens at float64(burst), so TokensAt can never reach 10.5; no bucket is ever deleted. With New(10.5, ...) the sweeper runs forever doing nothing and the per-key map grows unboundedly under attacker-controlled key cardinality — exactly what the sweeper exists to prevent. Same applies to capacity in (0,1), where Allow also errors but the bucket is still inserted first.

**Suggested fix:** Compare against float64(int(l.capacity)) (the real burst), or reject non-integer capacity in New.

#### 292. [MEDIUM] Shutdown grace period for in-flight handlers is not actually implemented
`data/stream/redisstream/consumer.go:682` — correctness — conf 0.85 — _reviewer-reported (unverified)_

Comment at lines 647-650 claims handlerShutdownTimeout 'prevents message loss by allowing in-flight handlers to complete their work'. But the detached-context branch only triggers when ctx.Err() != nil BEFORE dispatch; a handler already running when the parent context is cancelled gets handlerCtx (derived via WithTimeout) cancelled immediately, aborting mid-work. The message stays pending so it is redelivered, but the documented grace-period behavior does not exist, causing avoidable duplicate processing on every shutdown.

**Suggested fix:** Either derive handlerCtx from context.WithoutCancel(ctx) with the 30s timeout in all cases, or fix the comments to describe actual immediate-cancel behavior.

#### 293. [MEDIUM] StartConsumers under-validates stream names, converting config errors into runtime panic+shutdown
`data/stream/redisstream/start.go:52` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

StartConsumers pre-validates bindings only for empty Stream and nil Handler, but Consume panics via redis.ValidateName for names with whitespace/control chars or >maxNameLen. Such a name passes StartConsumers' checks, then panics inside the spawned goroutine, where it is recovered, logged as 'stream consumer panicked', and triggers shutdownFn — the service starts and then gracefully shuts itself down instead of failing startup with the BindingError the API promises for malformed bindings.

**Suggested fix:** Call redis.ValidateName(b.Stream, "stream") in the binding validation loop and return a BindingError, matching Consume's stricter check.

#### 294. [LOW] List/VerifyChain call SecretSource.Resolve once per entry — N+1 for KMS-backed sources
`data/actionlog/actionlog.go:668` — performance — conf 0.80 — _reviewer-reported (unverified)_

VerifyEntry resolves the secret per entry. A List page of up to MaxPageLimit (10,000) entries or a VerifyChain over a long tenant chain issues one Resolve per row, almost always for the same keyID. The SecretSource docs explicitly target KMS/Vault/Secrets Manager adapters, so this is a remote call per row — latency and rate-limit pressure — unless every adapter implements its own cache.

**Suggested fix:** Memoize resolved secrets by keyID for the duration of a single List/VerifyChain call (small map in the loop); semantics unchanged for static sources.

#### 295. [LOW] SignEntry doc claims it mutates e.SignatureKeyID — it cannot (pass-by-value)
`data/actionlog/actionlog.go:717` — docs — conf 0.80 — _reviewer-reported (unverified)_

The doc says "Mutates e.SignatureKeyID to the resolved key id", but e is an Entry value; line 740 mutates only the local copy. The returned signature was computed with SignatureKeyID set to the resolved keyID, so a caller who trusts the doc and sets only e.Signature (as the doc instructs) produces an entry whose VerifyEntry either rejects (empty key id) or recomputes a different canonical context, failing verification.

**Suggested fix:** Reword: callers must set both e.Signature = sig and e.SignatureKeyID = keyID from the return values; or return the populated Entry.

#### 296. [LOW] Entry.ID skips UTF-8/control-character validation all other fields get
`data/actionlog/actionlog.go:826` — consistency — conf 0.75 — _reviewer-reported (unverified)_

validate() checks ID only for emptiness and length ≤ 36. Actor/Action/Resource/SignatureKeyID are checked for valid UTF-8, control chars, and whitespace; tenant IDs via coretenant.ValidateID. A caller-supplied replay ID containing invalid UTF-8 or control bytes passes validation, gets signed, then fails at Postgres INSERT (invalid byte sequence) — the exact late-failure class the FR-051 comment says this code was changed to prevent. The ID also flows into signed cursor payloads.

**Suggested fix:** Apply validTextField-style checks (valid UTF-8, no control/space runes) to caller-supplied IDs in validate().

#### 297. [LOW] List deep-clones every matching entry before cursor/limit pruning
`data/actionlog/memory/memory.go:215` — performance — conf 0.80 — _reviewer-reported (unverified)_

List clones (reflection-based metadata deep copy) every entry matching the filters while holding mu.RLock, then sorts, then discards everything outside the cursor window and limit. Each page pays O(all matches) clone cost and lock hold time, not O(limit). latestForTenantLocked similarly scans the whole entries slice per Append, making N appends O(N²). Documented as test-scale, but PruneTenants and ctxCheckBatch suggest long-running multi-million-entry usage is anticipated.

**Suggested fix:** Apply the cursor predicate during the scan and clone only after truncating to limit+1; maintain a per-tenant latest-entry map to make Append O(1).

#### 298. [LOW] Unique-violation 'typed error' promise unfulfilled — no exported sentinel
`data/actionlog/postgres/store.go:345` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

The uniqueViolation comment (line 22) says collisions are translated "back into a typed error the caller can retry", but insertEntry only wraps the pgconn error with a redact message string. There is no exported sentinel, so callers cannot errors.Is-detect a concurrent seq collision without importing pgconn and matching SQLSTATE themselves — contrary to the package's sentinel-driven error conventions (cf. memory.ErrDuplicateID).

**Suggested fix:** Export ErrSeqCollision (or reuse a shared sentinel) and wrap via redact.WrapSentinel so errors.Is works; fix the comment otherwise.

#### 299. [LOW] No boundary length validation, and Revoke with zero time diverges from MemoryRepository
`data/apikey/postgres/store.go:89` — consistency — conf 0.70 — _reviewer-reported (unverified)_

Insert performs no length validation (id ≤ 36, prefix ≤ 64, owner ≤ 255), so oversized values fail late with raw VARCHAR errors — the failure class actionlog explicitly validates against at the package boundary. Separately, Revoke writes at.UTC() directly: a zero `at` persists 0001-01-01 (key reads back revoked-forever), while apikey.MemoryRepository leaves RevokedAt zero (key stays active) — divergent Repository semantics for the same degenerate input.

**Suggested fix:** Validate field lengths before INSERT mirroring actionlog's caps; in Revoke reject or nullTime() a zero `at` to match the memory implementation.

#### 300. [LOW] Encode after Close emits an empty-key-HMAC cursor, contradicting the Close contract
`data/approval/cursor.go:85` — docs — conf 0.90 — _reviewer-reported (unverified)_

Close() doc states subsequent Encode returns "" because "the empty key short-circuits". It does not: secret.String.Use after Zero calls the closure with a nil slice, so Encode computes HMAC-SHA256 with an empty key and returns a non-empty cursor that Decode then rejects. A List racing shutdown returns a poisoned next-cursor instead of "" (clients get 400 instead of clean end-of-pages). Same for a zero-value CursorSigner.

**Suggested fix:** In Encode, return "" when s.key == nil || s.key.IsEmpty(), matching the documented contract.

#### 301. [LOW] List deep-clones every matching request before pagination trims to one page
`data/approval/memory/memory.go:163` — performance — conf 0.85 — _reviewer-reported (unverified)_

cloneRequest (payload copy, up to 64KiB each) runs for all matches during the scan, then sorting and cursor slicing discard everything outside the page. A tenant with 10k pending requests pays ~all-payload copies per page request, under the store mutex. Bounded for the dev-store use case but easily avoidable.

**Suggested fix:** Collect shallow values (or pointers) during the scan, and cloneRequest only the final page slice after cursor/limit trimming.

#### 302. [LOW] MarkExecuted ignores ExpiresAt — approved requests are executable forever
`data/approval/memory/memory.go:271` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Both memory and postgres MarkExecuted check only State==Approved; ExpiresAt gates only the pending->decision transition. An approved-but-unexecuted request can be executed arbitrarily long after both approval and expiry, despite the package selling a "bounded-decision-window invariant". For a gate around destructive operations, a months-old approval silently remains a live execution token.

**Suggested fix:** Document this explicitly, or add an additive option/check rejecting MarkExecuted when now >= ExpiresAt.

#### 303. [LOW] TestNew_PanicsOnNilOption never exercises the nil-option branch
`data/approval/memory/memory_test.go:410` — testing — conf 0.95 — _reviewer-reported (unverified)_

The test calls New(nil), which panics on the nil *CursorSigner check — identical to TestNew_PanicsOnNilCursorSigner below it. The nil-Option panic branch in New (memory.go:67) is never executed, so the test name asserts coverage it does not provide.

**Suggested fix:** Change to assert.Panics(func() { New(testCursorSigner(t), nil) }).

#### 304. [LOW] Create returns nanosecond CreatedAt while Postgres stores microseconds
`data/approval/postgres/store.go:77` — correctness — conf 0.70 — _reviewer-reported (unverified)_

Create echoes r.CreatedAt (time.Now nanoseconds) but timestamptz truncates to microseconds, so the Request returned by Create is not equal to the one a subsequent Get/List returns, and differs from the memory backend (full nanoseconds). Round-trip equality checks and any caller-side keyset comparison built from the Create return value can mismatch.

**Suggested fix:** Truncate CreatedAt/ExpiresAt to time.Microsecond before insert and in the returned Request.

#### 305. [LOW] Duplicate-ID error is a sentinel in memory store but an opaque redacted error in postgres
`data/approval/postgres/store.go:90` — consistency — conf 0.85 — _reviewer-reported (unverified)_

memory.Store.Create returns ErrDuplicateID; postgres.Store.Create surfaces the unique-violation as a generic redact.WrapError with no sentinel, so callers cannot portably detect an ID collision (errors.Is fails) when swapping the documented-interchangeable backends. The shared Store interface defines no duplicate contract at all.

**Suggested fix:** Add an approval.ErrDuplicateID sentinel; map pgcode 23505 in postgres Create and alias the memory sentinel to it (additive).

#### 306. [LOW] Script reply type-assertion failures are silently coerced to zero values
`data/budget/redis/redis.go:367` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

Consume uses `allowed, _ := pair[0].(int64)` and `remaining, _ := pair[1].(int64)`; Peek (line 458) and Refund (line 414) use `rem, _ := res.(int64)`. If go-redis ever returns a different type (proxy, RESP oddity), Consume reports a spurious denial with remaining=0 and nil error, and Peek/Refund report 0 remaining with nil error — indistinguishable from legitimate exhaustion, with no diagnostic.

**Suggested fix:** Check the type assertions and return the existing "unexpected script result shape" error on mismatch.

#### 307. [LOW] ValidateKeyPrefix allocates an ad-hoc error instead of a sentinel, unlike every sibling check
`data/cache/cache.go:91` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

Prefix-too-long returns errors.New("cache prefix exceeds maximum length") per call, so callers cannot errors.Is it, while the analogous key check uses the ErrKeyTooLong sentinel (and NewComputeCache/NewTypedCache propagate this error to users). Inconsistent with the package's otherwise sentinel-based error contract.

**Suggested fix:** Add var ErrPrefixTooLong (or wrap ErrKeyTooLong with %w) and return it.

#### 308. [LOW] Conformance suite compares miss errors with != instead of errors.Is
`data/cache/cachetest/conformance.go:193` — testing — conf 0.85 — _reviewer-reported (unverified)_

testConcurrentReadWrite counts err != cache.ErrCacheMiss as a backend error. A third-party backend that wraps the sentinel (fmt.Errorf("...: %w", ErrCacheMiss)) passes testGetMissing (which uses ErrorIs) yet fails the concurrency test — exactly the external implementations this suite exists to certify. Also, a legitimate ErrAdmissionRejected from a small bounded backend would fail the Set branch.

**Suggested fix:** Use errors.Is(err, cache.ErrCacheMiss) and tolerate ErrAdmissionRejected on the Set side.

#### 309. [LOW] Test factories never Close MemoryCache — ristretto goroutines leak across the test binary
`data/cache/cachetest/conformance_test.go:18` — testing — conf 0.85 — _reviewer-reported (unverified)_

The dogfood factory omits t.Cleanup(mc.Close) despite the Factory doc saying cleanup should be registered there, and ~15 tests in memory_cache_test.go / typed_cache_test.go construct MustNewMemoryCache() without Close. Each instance leaks a ristretto processItems goroutine plus cleanup ticker for the binary's lifetime, contradicting the package's own lifecycle guidance and adding noise to any future goroutine-leak detection.

**Suggested fix:** Register t.Cleanup(func(){ _ = mc.Close() }) in the conformance factory and a shared test helper.

#### 310. [LOW] Prefix concatenation without separator allows cross-instance key collisions
`data/cache/compute.go:221` — api-design — conf 0.70 — _reviewer-reported (unverified)_

fullKey does cc.prefix + key (same in typed_cache.go:46). Distinct (prefix,key) pairs collide: prefix "user"+key "s1" equals prefix "users"+key "1". The constructor docs claim the prefix exists 'to avoid collisions' but nothing enforces a trailing delimiter, so two caches with related prefixes on a shared backend can silently read each other's envelopes (decode failure → spurious recompute, or worse with same T).

**Suggested fix:** Document that prefixes should end in a delimiter, or reject prefixes not ending in ':' / insert one.

#### 311. [LOW] inflight map entry leaks when a background refresh is the singleflight leader
`data/cache/compute.go:350` — concurrency — conf 0.75 — _reviewer-reported (unverified)_

computeAndStore stores `full` in cc.inflight, but the delete lives in the foreground DoChan closure (line 364), which never runs if a triggerBackgroundRefresh closure already holds singleflight leadership for the same key. The stale entry misclassifies all subsequent callers as followers (followers counter and wait histogram skew) until a future foreground leader's defer cleans it; for keys never recomputed in the foreground, the entry persists indefinitely.

**Suggested fix:** Delete the inflight entry from the caller (defer after LoadOrStore when !isFollower) instead of inside the closure.

#### 312. [LOW] ComputeFunc runs on a context stripped of all caller values; undocumented
`data/cache/compute.go:636` — api-design — conf 0.80 — _reviewer-reported (unverified)_

computeContext derives from bgCtx (context.Background), so fn never sees the caller's context values — trace spans, auth tokens, tenant IDs, loggers. Detachment is deliberate for shared computes, but neither GetOrCompute's nor ComputeFunc's doc mentions it; fn doing authenticated or traced backend calls silently loses propagation, which is hard to debug.

**Suggested fix:** Document the value detachment on ComputeFunc/GetOrCompute, or use context.WithoutCancel-style value preservation from the leader's ctx.

#### 313. [LOW] Short-deadline leader aborts shared compute and propagates its deadline error to all followers
`data/cache/compute.go:637` — api-design — conf 0.60 — _reviewer-reported (unverified)_

computeContext binds the shared compute to the first caller's deadline. On a hot key, one request with a tight budget becomes leader, the compute is killed at its deadline, and every concurrent follower with a larger budget receives context.DeadlineExceeded; errors are not cached so the herd then retries. Error amplification footgun inherent to sharing the leader's deadline.

**Suggested fix:** Cap with max(leader deadline, computeTimeout) for the shared compute, or document the amplification; followers already bail via their own ctx.

#### 314. [LOW] MustNewMemoryCache swallows the underlying error; constructor docs reference a nonexistent Open* rename
`data/cache/memory_cache.go:330` — docs — conf 0.90 — _reviewer-reported (unverified)_

MustNewMemoryCache panics with a fixed string, discarding err entirely — debugging a misconfiguration loses the cause (ristretto's init error). Additionally, both NewMemoryCache and MustNewMemoryCache docs claim 'The Open* prefix marks this as a side-effecting constructor' / 'Replaces the v1 NewMemoryCache spelling', but the functions are still named New*/MustNew* — doc rot from an abandoned rename.

**Suggested fix:** panic(fmt.Sprintf("cache: invalid config: %v", redact-safe err)) and delete the Open*-prefix paragraphs.

#### 315. [LOW] require.NoError / testing.T used from non-test goroutines
`data/cache/memory_cache_test.go:669` — testing — conf 0.80 — _reviewer-reported (unverified)_

TestMemoryCache_SetNX_ConcurrentAtomicity calls require.NoError inside spawned goroutines (FailNow from a non-test goroutine only exits that goroutine, leaving the test to hang or pass spuriously), and compute_test.go:829 invokes gatherGaugeValue(t, ...) — which calls require — from inside the singleflight compute goroutine. Both violate the testing.T goroutine contract flagged by go vet's testinggoroutine analyzer pattern.

**Suggested fix:** Collect errors into channels/atomics and assert on the test goroutine after wg.Wait().

#### 316. [LOW] NewCache always registers default metrics on prometheus.DefaultRegisterer, even with a custom registerer
`data/cache/rediscache/cache.go:163` — api-design — conf 0.80 — _reviewer-reported (unverified)_

The struct literal evaluates defaultMetrics() unconditionally before options run. The first NewCache call therefore registers redis_cache_hits_total/misses_total on the global DefaultRegisterer via sync.OnceValue even when every caller passes WithMetricsRegisterer(customReg), polluting the default registry and risking collisions in hosts that manage their own registries.

**Suggested fix:** Leave rc.metrics nil in the literal and call defaultMetrics() after options only if still nil.

#### 317. [LOW] Hit/miss metric accounting inconsistent across oversize and non-string paths
`data/cache/rediscache/cache.go:228` — consistency — conf 0.75 — _reviewer-reported (unverified)_

Get's pre-GET oversize path (line 201) increments the miss counter, but the post-GET TOCTOU oversize path (line 218-228) increments neither hits nor misses. In the uncapped MGet, a non-string MGET reply is skipped (line 331-333) without counting a miss. Hit-ratio dashboards undercount events on these paths.

**Suggested fix:** Increment misses in the TOCTOU branch and the non-string MGet skip for consistent accounting.

#### 318. [LOW] Quick-start example does not compile: wrong ApplyTo signature and nonexistent cron.JobFunc
`data/cron/pgstore/doc.go:36` — docs — conf 0.85 — _reviewer-reported (unverified)_

The example calls store.ApplyTo(scheduler, jobs) with no ctx and a single error return, but the real signature is ApplyTo(ctx, scheduler, jobs) ([]string, error). It also declares jobs as map[string]cron.JobFunc — runtime/cron exports no JobFunc type (grep confirms); the alias lives in pgstore itself, which the doc's own rationale at line 183-185 of store.go says exists precisely to avoid this.

**Suggested fix:** Fix the example to use pgstore.JobFunc and the real ApplyTo signature including the unknown-names return.

#### 319. [LOW] cloneBytes turns a non-nil empty fingerprint into nil, diverging from pgstore semantics
`data/idempotency/idempotency.go:647` — consistency — conf 0.70 — _reviewer-reported (unverified)_

cloneBytes([]byte{}) returns append([]byte(nil)) == nil, so MemoryStore stores a nil fingerprint for a caller-supplied empty (non-nil) fingerprint, permanently disabling mismatch detection for that key (entry.fingerprint != nil guard at line 370). pgstore stores empty bytea (non-NULL) and keeps mismatch detection active. A direct Store caller using an empty fingerprint gets silently different 422 behavior per backend; the conformance suite never tests this.

**Suggested fix:** Preserve emptiness: if b != nil return non-nil copy (make([]byte, len(b)) + copy); add a conformance case.

#### 320. [LOW] Get size-gates response_body via octet_length but scans headers column unbounded
`data/idempotency/pgstore/store.go:141` — security — conf 0.65 — _reviewer-reported (unverified)_

The Wave-66 fix probes COALESCE(octet_length(response_body),0) before the full SELECT to avoid materialising hostile multi-MB bodies, but the same SELECT pulls the headers JSON column with no size gate. A hostile/legacy row with multi-MB headers JSON is fully allocated and json.Unmarshal'd before ValidateCachedResponse rejects it — the exact allocation-before-cap pattern the body gate closed.

**Suggested fix:** Include octet_length(headers) in the size probe and reject above MaxCachedHeaders*MaxCachedHeaderValueBytes-derived bound.

#### 321. [LOW] TryLock/Get wrappers misname doTryLock/doGet results (ok vs mismatch transposed)
`data/idempotency/pgstore/store.go:255` — consistency — conf 0.90 — _reviewer-reported (unverified)_

doTryLock returns (token, fingerprintMismatch, ok, err) per the Store contract, but the wrapper binds them as 'token, ok, fingerprintMatch, err' (line 255) — both names wrong and transposed. Get's wrapper similarly binds the mismatch flag as 'ok' (line 103). Positional returns keep runtime behavior correct today, but the misleading names invite a real transposition during future edits.

**Suggested fix:** Rename to (token, mismatch, ok, err) and (cached, mismatch, err) in the wrappers.

#### 322. [LOW] maxStoredEntryBytes doc references non-existent Store.PutResponse
`data/idempotency/redisstore/store.go:167` — docs — conf 0.90 — _reviewer-reported (unverified)_

The comment says 'Set-side bounds via [Store.PutResponse] reject oversized writes', but no PutResponse method exists anywhere in the repo (grep confirms only this reference). The actual write-side bound is idempotency.ValidateCachedResponse called in doSet. Broken doc link will also fail godoc reference resolution.

**Suggested fix:** Change the reference to [Store.Set] / idempotency.ValidateCachedResponse.

#### 323. [LOW] TryLock wrapper variable names swapped vs interface contract
`data/idempotency/redisstore/store.go:286` — correctness — conf 0.95 — _reviewer-reported (unverified)_

The interface contract is (token, fingerprintMismatch, ok, err), and doTryLock returns values in that order. The wrapper binds them as `token, ok, fingerprintMatch, err` — the variable named `ok` actually holds the mismatch flag and `fingerprintMatch` holds the acquired flag. Behavior is correct because positions are preserved, but any future edit keyed off these names will invert the semantics.

**Suggested fix:** Rename to `token, fingerprintMismatch, ok, err` to match the idempotency.Store.TryLock signature.

#### 324. [LOW] Stale FNV-1a comment and two assertion-free tests in keymap_test
`data/lock/pgadvisory/keymap_test.go:18` — testing — conf 0.90 — _reviewer-reported (unverified)_

Line 18's assertion message says '(FNV-1a)' but keyToInt64 uses SHA-256 (the FNV vulnerability was specifically fixed per the code comment). TestKeyToInt64_HandlesEmptyString and TestKeyToInt64_ResultIsInt64 contain no assertions at all (`_ = id`). Beyond keymap, the package has zero unit tests for Acquire/Release/Extend logic in this directory.

**Suggested fix:** Fix the message, replace the two no-op tests with golden-value assertions (cross-process determinism), and add driver-mock tests for Release/Extend error paths.

#### 325. [LOW] pgadvisory accepts empty/control-byte keys that redislock and redlock reject
`data/lock/pgadvisory/pgadvisory.go:96` — consistency — conf 0.85 — _reviewer-reported (unverified)_

Acquire performs no key validation — empty strings, NULs, and newlines are hashed and accepted — while redislock/redlock reject the same inputs via validateLockKey. Code swapping backends through the lock.Locker interface changes validation behavior silently; the locktest suite never exercises invalid keys so the divergence is untested.

**Suggested fix:** Add the same non-empty/no-control-bytes/max-length key validation as redislock, and add an invalid-key case to locktest.

#### 326. [LOW] Package doc claims Redlock quorum is 'intentionally not adopted' while the redlock subpackage ships it
`data/lock/redislock/doc.go:24` — docs — conf 0.90 — _reviewer-reported (unverified)_

redislock/doc.go states 'Redlock quorum is intentionally not adopted: the consensus argument is contested...' but data/lock/redislock/redlock exports QuorumLocker implementing exactly that, and locktest/lock docs reference it as a supported adapter. The stale rationale will steer users away from a shipped, supported option.

**Suggested fix:** Rewrite the limitation bullet to point readers to the redlock subpackage for quorum mode.

#### 327. [LOW] TestLocker_ReleaseOnlyByOwner asserts nothing about owner-only release
`data/lock/redislock/lock_test.go:130` — testing — conf 0.95 — _reviewer-reported (unverified)_

The test acquires and releases a lock, then constructs `l2 := struct{ lock.Lock }{}` and discards it — no assertion exercises the owner-only release property promised by the test name. The body is vestigial and gives false coverage confidence for a security-relevant invariant (token-fenced release).

**Suggested fix:** Acquire with one handle, attempt Release via a second locker's handle on the same key, and assert ErrLockLost / no-op; or delete the test.

#### 328. [LOW] TestSentinels asserts errors against themselves — trivially true
`data/queue/queue_test.go:13` — testing — conf 0.95 — _reviewer-reported (unverified)_

assert.ErrorIs(queue.ErrInvalidQueue, queue.ErrInvalidQueue) and the ErrBatchTooLarge twin can never fail regardless of how the sentinels are defined. The test provides zero protection; ErrInvalidName, ErrInvalidMessage and ErrMessageTooLarge sentinels get real coverage elsewhere in the file, but these two are only 'covered' by this no-op.

**Suggested fix:** Assert real properties (e.g., MessageTooLargeError unwraps to ErrMessageTooLarge — already covered — and ErrBatchTooLarge wiring from a batch validator) or delete the test.

#### 329. [LOW] Doc references nonexistent WithHealthCheckInterval option
`data/queue/redisqueue/doc.go:24` — docs — conf 0.95 — _reviewer-reported (unverified)_

Package doc says the depth poller runs on "the `WithHealthCheckInterval` cadence; default 30s", but no such option exists anywhere in the repo — healthCheckPeriod is a hard-coded constant. Operators reading the doc will look for an option that cannot be set.

**Suggested fix:** Either add the option or change the doc to say the 30s cadence is fixed.

#### 330. [LOW] Process swallows srv.Start failure; StartProcessors shutdownFn never fires
`data/queue/redisqueue/queue.go:729` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

On Start error Process logs and returns nil-like (no panic, no error). Via StartProcessors the goroutine exits silently and shutdownFn (wired only to panics) is not invoked, so the app keeps running with no consumer. Start failures are rare in asynq (mostly server-state errors), hence low severity, but the failure mode is fully silent apart from one log line.

**Suggested fix:** Invoke a failure callback or panic on Start error so StartProcessors' shutdown path engages.

#### 331. [LOW] messages_retried_total over-counts on the final (archived) attempt
`data/queue/redisqueue/queue.go:820` — correctness — conf 0.80 — _reviewer-reported (unverified)_

For a non-permanent handler error the wrapper always increments messagesRetried, even when retryCount already equals maxRetries and asynq will archive instead of retry (onTaskError then also increments messagesDeadLettered). Each dead-lettered task therefore inflates the retried counter by one, skewing retry/DLQ ratio dashboards.

**Suggested fix:** Only increment messagesRetried when retryCount < q.maxRetries (asynq.GetMaxRetry is available in ctx).

#### 332. [LOW] Duplicate queue names in bindings bypass startup validation and trigger runtime shutdown
`data/queue/redisqueue/start.go:46` — api-design — conf 0.85 — _reviewer-reported (unverified)_

StartProcessors validates empty names and nil handlers "at startup rather than at runtime", but two bindings with the same queue name pass validation; the second Process call then panics (active-queue guard), which the recover turns into a full shutdownFn — a config error becomes a runtime whole-app teardown instead of an immediate error return.

**Suggested fix:** Reject duplicate Binding.Queue values in the validation loop with a BindingError.

#### 333. [LOW] TestEnvelopeWorker_DispatchesToHandler asserts nothing
`data/queue/riverqueue/riverqueue_test.go:144` — testing — conf 0.90 — _reviewer-reported (unverified)_

The test constructs a worker and a shadow args struct, then ends with `_ = w; _ = called` and a comment explaining it cannot build a river.Job — yet the internal test file does exactly that (riverqueue_internal_test.go:26) and covers dispatch. This test contributes zero assertions and false coverage signal.

**Suggested fix:** Delete it or convert it to call Work via the internal-package pattern already used.

#### 334. [LOW] Ignored type assertions can silently deny with nil error
`data/ratelimit/redis/redis.go:253` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

If the script result is a 2-element slice with non-int64 members, `allowed, _ := pair[0].(int64)` yields 0 and retryUS 0, so Allow returns (false, 1ns, nil) — a silent deny indistinguishable from a legitimate rate-limit rejection, instead of the explicit shape error returned for wrong-length results.

**Suggested fix:** Use checked assertions and return the "unexpected script result shape" error when they fail.

#### 335. [LOW] Nil/zero Store panics instead of returning a sentinel like sibling packages
`data/saga/pgstore/store.go:147` — consistency — conf 0.80 — _reviewer-reported (unverified)_

redisqueue, riverqueue, and all ratelimit limiters guard nil/zero receivers with ready() and return ErrInvalidQueue/ErrInvalidLimiter; pgstore's Get/Put/Delete/ListResumable dereference s.db directly, so a zero &Store{} (or nil receiver) panics with a nil-pointer deref. Inconsistent with the kit-wide invalid-receiver convention exercised by every sibling's tests.

**Suggested fix:** Add a ready() check returning a pgstore sentinel (or saga error) on nil receiver/db.

#### 336. [LOW] No guard that claimMinIdle exceeds the handler/ack execution window
`data/stream/redisstream/consumer.go:245` — api-design — conf 0.70 — _reviewer-reported (unverified)_

WithClaimMinIdle accepts any positive duration. A value below the 30s handler timeout + 10s ack window (the kit's own integration test uses 500ms) lets XAUTOCLAIM transfer a message whose handler is still running, producing concurrent duplicate processing and competing ACK/dead-letter writes. At-least-once duplication is inherent, but the footgun threshold is silent.

**Suggested fix:** Document the minimum-safe relationship (claimMinIdle > handler timeout + ackTimeout) on WithClaimMinIdle, or log a startup warning when violated.

#### 337. [LOW] NewConsumer/NewProducer eagerly register metrics on the global default registry even when overridden
`data/stream/redisstream/consumer.go:364` — api-design — conf 0.65 — _reviewer-reported (unverified)_

NewConsumer sets metrics: defaultConsumerMetrics() before options run, so constructing a consumer with WithConsumerRegisterer(customReg) still registers all five redis_stream_* collectors on prometheus.DefaultRegisterer (once per process). MustRegisterOrGet panics on an incompatible same-name collector, so a caller who registered their own redis_stream_* metric on the default registry gets a construction panic despite explicitly opting out of it. Same pattern in NewProducer.

**Suggested fix:** Defer default-metrics materialization until after options: if c.metrics == nil after the option loop, assign defaultConsumerMetrics().

#### 338. [LOW] Idempotency and concurrency contracts referenced but never documented
`data/stream/redisstream/consumer.go:704` — docs — conf 0.85 — _reviewer-reported (unverified)_

handleMessage's comment says 'handlers MUST be idempotent (see Consume godoc and doc.go)', but Consume's godoc and the 6-line doc.go say nothing about idempotency. Also undocumented: the claim loop runs in a separate goroutine from readNew/processPending, so a single Consumer invokes the handler from two goroutines concurrently — Handler's doc makes no thread-safety statement. Both are load-bearing contracts for correct use.

**Suggested fix:** Document at Handler/Consume/doc.go: handlers must be idempotent and safe for concurrent invocation; fix the dangling reference.

#### 339. [LOW] XAUTOCLAIM pagination stops on empty page even with non-terminal cursor
`data/stream/redisstream/helpers.go:147` — correctness — conf 0.55 — _reviewer-reported (unverified)_

claimStaleMessages returns when len(msgs)==0 even if newStart != "0-0". XAUTOCLAIM can scan a window containing zero claimable entries while more PEL remains beyond the cursor; the loop then abandons the scan and the next tick restarts from "0-0", repeatedly scanning only the head window. If the PEL head holds entries younger than claimMinIdle (e.g. recently re-claimed), idle entries deeper in the PEL can be starved of recovery for many intervals.

**Suggested fix:** Continue the loop with startID = newStart when newStart != "0-0", regardless of len(msgs).

#### 340. [LOW] PublishBatch metric undercounts on partial pipeline failure
`data/stream/redisstream/producer.go:334` — correctness — conf 0.70 — _reviewer-reported (unverified)_

When cmd i fails, only the `succeeded` commands before index i are added to messagesProduced, but commands after i may have succeeded on the server (pipelines are not atomic, as the godoc itself notes). The produced counter therefore undercounts in exactly the partial-failure scenario it exists to observe.

**Suggested fix:** Iterate all cmds, count every cmd.Err()==nil success for the metric, then return the first error.

#### 341. [LOW] Auto-generated message ID is unobservable by the publisher
`data/stream/redisstream/producer.go:351` — api-design — conf 0.70 — _reviewer-reported (unverified)_

buildXAddArgs fills msg.ID = id.New() on a by-value copy when empty; Publish returns only the Redis stream ID and its debug log records the caller's original (empty) msg_id. A caller relying on the documented 'id → UUID v7' default can never learn the idempotency ID that was persisted, defeating dedup on the producer side unless they pre-build via NewMessage.

**Suggested fix:** Document that callers needing the message ID must use NewMessage, or additively return/log the generated ID.

#### 342. [LOW] TestSentinels is tautological
`data/stream/stream_test.go:12` — testing — conf 0.90 — _reviewer-reported (unverified)_

assert.ErrorIs(t, stream.ErrInvalidStream, stream.ErrInvalidStream) asserts an error matches itself, which can never fail and tests nothing. Sibling package data/ratelimit shows the intended pattern: assert distinct sentinels are NOT errors.Is-equal to each other.

**Suggested fix:** Delete the test or assert something falsifiable (e.g., message content or distinctness from other sentinels).

#### 343. [LOW] WhereClause silently emits invalid placeholders for negative arg counts
`data/tenant/scope.go:87` — api-design — conf 0.65 — _reviewer-reported (unverified)_

WhereClause(currentArgCount) performs no range validation; a negative input yields "tenant_id = $0" or "$-1", which fails only later at query time with an obscure pgx error. For a helper whose stated purpose is to be 'a one-line guard against off-by-one mistakes', silently producing an out-of-range placeholder undercuts the contract.

**Suggested fix:** Panic or clamp/document for currentArgCount < 0 (programmer error), consistent with the package's fail-loud style.

#### 344. [INFO] budgetScript docs describe arguments and algorithm that do not exist
`data/budget/redis/redis.go:54` — docs — conf 0.95 — _reviewer-reported (unverified)_

The script header documents ARGV[4] = retry-after milliseconds, but the script never reads ARGV[4] and Consume passes only three ARGVs. The package doc (line 6) also describes "optimistically INCRBY, on overflow DECRBY and reject", while the implementation checks headroom before INCRBY and never DECRBYs. Misleading for maintainers editing the Lua.

**Suggested fix:** Delete the ARGV[4] line and update the package doc to describe the pre-check-then-INCRBY algorithm.

#### 345. [INFO] Operations on a closed MemoryCache silently masquerade as misses/admission rejections
`data/cache/memory_cache.go:346` — consistency — conf 0.80 — _reviewer-reported (unverified)_

After Close, ristretto returns zero values: Get yields ErrCacheMiss, Exists false, Set ErrAdmissionRejected (tests even rely on the latter to drive the rejection branch). ComputeCache surfaces ErrCacheClosed, but the base Cache layer has no closed signal, so use-after-close looks like an empty cache — a silent failure mode in shutdown races.

**Suggested fix:** Track closed state in MemoryCache and return a dedicated error (additive: new sentinel, no API break).

#### 346. [INFO] Capped Get doubles Redis round-trips on the hottest path
`data/cache/rediscache/cache.go:196` — performance — conf 0.70 — _reviewer-reported (unverified)_

With the default 10 MiB cap enabled, every single Get issues STRLEN then GET sequentially — 2 RTTs per read, doubling latency for all cache hits and misses fleet-wide. Pipelining doesn't help because the point is to avoid transferring oversize bytes.

**Suggested fix:** Consider a cached EVAL/Lua script (server-side length check returning value-or-toolarge) to restore 1 RTT while keeping the cap.

#### 347. [INFO] WithLock/LockerWithValue contention error has no sentinel for errors.Is
`data/lock/redislock/lock.go:225` — api-design — conf 0.80 — _reviewer-reported (unverified)_

On contention, WithLock and LockerWithValue (and redlock.WithLock) return errors.New("lock: could not acquire lock") — an unexported ad-hoc error. Callers cannot programmatically distinguish 'lock busy, retry later' from backend failures without string matching, unlike Acquire which returns (nil, false, nil).

**Suggested fix:** Additive: export var ErrNotAcquired in data/lock and return it (wrapped) from all WithLock helpers across redislock and redlock.

#### 348. [INFO] redlock lacks tracing spans and WithLogger, unlike sibling redislock
`data/lock/redislock/redlock/redlock.go:202` — consistency — conf 0.85 — _reviewer-reported (unverified)_

redislock instruments Acquire/Release/Extend/WithLock with OTel spans and offers WithLogger for the post-WithLock release-failure log; redlock has neither — releaseAndJoin logs via slog.Default() unconditionally and no spans are emitted. Operators swapping single-instance for quorum locking lose observability silently.

**Suggested fix:** Port the tracing.go helper (kit.lock.backend="redlock") and the WithLogger option from redislock.

#### 349. [INFO] putUpdateOptimistic conflates row-not-found with concurrent update
`data/saga/pgstore/store.go:141` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

rows==0 from the CAS UPDATE is reported as ErrConcurrentUpdate even when the row no longer exists (e.g., instance Deleted by another replica after completion). The executor's re-read will then hit ErrInstanceNotFound, so the loop converges, but the first error is misleading for callers logging conflicts.

**Suggested fix:** Optionally distinguish via a follow-up existence check or document the conflation.

#### 350. [INFO] messagesDeadLettered increments even when the pipelined XACK fails
`data/stream/redisstream/helpers.go:82` — correctness — conf 0.70 — _reviewer-reported (unverified)_

In deadLetter, if XADD succeeds but XACK fails, the function logs and falls through to increment messagesDeadLettered. The message remains in the source PEL and will be dead-lettered again later, producing a duplicate DLQ entry counted twice. The crash-window duplicate is documented, but the ACK-failure path double-count makes the DLQ counter diverge from unique dead-lettered messages.

**Suggested fix:** Skip the metric increment (or use a separate dlq_ack_failed counter) when the XACK leg of the pipeline fails.


### infra — messaging, storage, secrets, redis, sqldb, leaderelection, outbox

_164 findings — 20 high, 57 medium, 72 low, 15 info_

#### 351. [HIGH] Drain watchdog starts at term start, not at leadership end
`infra/leaderelection/etcd/election.go:353` — correctness — conf 0.85 — _reviewer-reported (unverified)_

runOnce spawns OnAcquired then immediately calls awaitCallbackDrain — there is no hold phase selecting on leaderCtx.Done()/cbDone first, unlike pgadvisory.holdLeadership, redislock, and k8slease (drain in OnStoppedLeading). Result: every healthy term >30s emits 'OnAcquired callback still draining' warns plus drainStatePending observations, callback_drain_seconds records full term length as 'drained', and with WithCallbackDrainTimeout(d) any healthy term longer than d is forcibly resigned and Run returns ErrCallbackDrainTimeout (documented as restart-the-process fatal). TestRun_DrainTimeout_ReturnsSentinel cannot distinguish the two semantics.

**Suggested fix:** First wait on cbDone OR leaderCtx.Done() with no watchdog; start the warn ticker and drain timer only after leaderCtx.Done(), matching the pgadvisory/k8slease shape.

#### 352. [HIGH] Run is one-shot, returns nil on leadership loss, and the started guard makes the documented retry loop impossible
`infra/leaderelection/k8slease/lease.go:405` — api-design — conf 0.80 — _reviewer-reported (unverified)_

le.Run returns after a single leadership loss (renew failure) without ctx cancellation; the kit then returns nil (lostErrSlot empty, ctx.Err() nil), which callers read as clean shutdown — contradicting the Elector interface ('returns when ctx cancels or unrecoverable backend error') and the looping etcd/pgadvisory siblings. Worse, the comment at line 402-404 tells callers to 'wrap Run in their own retry loop', but started (line 292) is never reset after a successful Run, so the second invocation permanently fails with 'Run already invoked'. A transient API-server blip silently removes the replica from the election.

**Suggested fix:** Either loop re-acquire internally like the etcd adapter, or reset started on return and return a distinct sentinel (e.g. ErrLeadershipLost) instead of nil when leadership ends without ctx cancellation.

#### 353. [HIGH] Drain timeout: Run retries acquire instead of returning, enabling in-process double leader
`infra/leaderelection/pgadvisory/pgadvisory.go:267` — correctness — conf 0.92 — _reviewer-reported (unverified)_

When Extend fails (ctx alive) and the callback drain times out, holdErr contains ErrCallbackDrainTimeout, but Run has no guard: it falls through to "leadership lost; retrying", re-acquires, and spawns a second OnAcquired while the orphan still runs. This contradicts the ErrCallbackDrainTimeout doc (line 34-40: "returned by Run") and the no-overlap invariant. The sibling redislock fixed exactly this (L-141 guard, redislock.go:272-278); pgadvisory never got the fix.

**Suggested fix:** Port the redislock L-141 guard: before retrying, if errors.Is(holdErr, ErrCallbackDrainTimeout), log and return holdErr.

#### 354. [HIGH] Health-check Extend has no deadline; hung probe defeats lost-leader detection
`infra/leaderelection/pgadvisory/pgadvisory.go:390` — concurrency — conf 0.75 — _reviewer-reported (unverified)_

handle.Extend(ctx) runs with a cancel-only ctx (no deadline). The pg probe is ExecContext("SELECT 1") on the pinned session conn; on a silent network drop (the main failure mode this health check exists for) it blocks until TCP retransmit timeout (minutes). Meanwhile Postgres kills the session, releasing the advisory lock, another replica becomes leader, and this replica's OnAcquired ctx is never cancelled — unbounded overlap in the backend marketed for work that must NEVER overlap.

**Suggested fix:** Wrap each probe: pctx, c := context.WithTimeout(ctx, e.healthCheck); handle.Extend(pctx); treat deadline expiry as loss.

#### 355. [HIGH] Retry redelivery via original exchange duplicates messages into sibling queues
`infra/messaging/amqpbackend/topology.go:167` — correctness — conf 0.80 — _reviewer-reported (unverified)_

declareRetryTopology sets the retry queue's x-dead-letter-exchange to b.Exchange. When the TTL expires, RabbitMQ republishes through the original exchange, so EVERY queue bound with a matching key receives a fresh copy — for fanout (allowed by ValidateBindingSpecs, routing key ignored) all sibling consumer groups reprocess the message on every retry bounce of one group. DefaultRetryPolicy is auto-applied, so any multi-subscriber exchange gets duplicates whenever one subscriber's handler fails transiently.

**Suggested fix:** Dead-letter the retry queue to the default exchange ("") with x-dead-letter-routing-key set to b.ConsumerGroup so the message returns only to the originating queue.

#### 356. [HIGH] State-file persistence silently drops Message.Headers on restart replay
`infra/messaging/buffered_publisher.go:30` — correctness — conf 0.85 — _reviewer-reported (unverified)_

pendingMessage embeds Message, persisted via atomicfile.Save (plain json.Marshal). Message.Headers is tagged json:"-" (message.go:64) and no custom MarshalJSON exists, so headers are never written to the state file. After crash+restart, load() restores messages with nil Headers and drain republishes them. All backends (amqpbackend/publisher.go:145, natsbackend:703, redisbackend/convert.go:18) propagate Headers as transport metadata, so correlation IDs, request IDs, and tenant headers silently vanish on the documented crash-recovery path. No test covers header survival across save/load.

**Suggested fix:** Persist headers in pendingMessage (e.g. add a Headers field or wrap Message with an explicit headers key) and add a save/load round-trip test asserting header survival.

#### 357. [HIGH] Drain throughput hard-capped at ~20 msg/s; buffer can never recover under moderate load
`infra/messaging/buffered_publisher.go:658` — performance — conf 0.80 — _reviewer-reported (unverified)_

drain() publishes at most one batch of bufferedDrainBatchLimit=100 per invocation and is only invoked by Run's 5s ticker (plus startup). While pending>0, every Publish takes the buffer path (direct publish requires len(pending)==0). So after any broker blip, sustained inflow above ~20 msg/s means pending never reaches zero, direct mode never resumes, the buffer grows to maxSize (10k) and Publish starts returning buffer-full drops indefinitely — a permanent death spiral despite a healthy broker. The batch limit's comment shows it exists to bound lock hold, not throughput.

**Suggested fix:** Loop batches inside drain() while healthyFn() && pending>0 && ctx alive; keep per-batch lock bounding. Optionally trigger drain immediately when buffering while healthy.

#### 358. [HIGH] Multi-topic Subscriber delivers every topic's messages to a single binding's handler
`infra/messaging/kafkabackend/subscriber.go:371` — correctness — conf 0.88 — _reviewer-reported (unverified)_

newReader sets GroupTopics to ALL constructor topics regardless of the Consume binding's Exchange (the topic param is only used when len(topics)==1), and dispatch() never checks km.Topic against the binding. With topics=["a","b"], Consume(Binding{Exchange:"a"}, handlerA) processes topic-b records too; concurrent Consume calls for different bindings in one group get arbitrary partition assignment, so deliveries cross handlers. Contradicts the struct doc and NewSubscriber doc ('let Consume dispatch by Binding.Exchange'). Integration tests only use single-topic subscribers.

**Suggested fix:** In dispatch (or before it), skip/route records where km.Topic != b.Exchange, or build the Reader with only the binding's topic.

#### 359. [HIGH] Non-permanent handler errors lose the message once any later record on the partition commits
`infra/messaging/kafkabackend/subscriber.go:440` — correctness — conf 0.82 — _reviewer-reported (unverified)_

On a transient handler error the offset is left uncommitted, but the Consume loop immediately fetches subsequent records; when a later record on the same partition succeeds, CommitMessages advances the committed offset past the failed record (Kafka commits are watermarks, not per-message). The failed message is then never redelivered — even after rebalance/restart — contradicting the documented at-least-once shape. Effectively at-most-once for handler errors in steady state.

**Suggested fix:** On transient error, re-fetch the same record (close/recreate reader at committed offset) or back off without advancing; alternatively document at-most-once honestly.

#### 360. [HIGH] Consumer merges kit-internal X-* headers into user headers; can exceed MaxMessageHeaders and drop valid messages
`infra/messaging/natsbackend/natsbackend.go:921` — correctness — conf 0.82 — _verified_

Publish writes user headers PLUS X-Message-Id, X-Message-Type, X-Exchange, X-Routing-Key into NATS headers (L703-709). On consume, deliveryHeaderMaps copies ALL NATS headers (incl. those 4) into msg.Headers (L920-922), then ValidateMessage runs (L932). MaxMessageHeaders=64, so a message published with 61-64 user headers (allowed at publish) yields 65-68 on consume and is Term-discarded (dropped permanently). Internal X-* metadata also leaks to every handler.

**Suggested fix:** Strip headerExchange/headerRoutingKey/X-Message-Id/X-Message-Type from the materialised user-header map (additive fix) so they don't leak or count toward MaxMessageHeaders.

#### 361. [HIGH] Heartbeat covers only the in-flight entry; queued claimed batch entries can be stale-reset mid-batch, causing duplicate publishes
`infra/outbox/relay.go:409` — concurrency — conf 0.75 — _reviewer-reported (unverified)_

FetchPending (relay.go:351) claims up to batchSize=100 rows to 'processing' at T0, but startHeartbeat runs per-entry only while that entry is being published. Entries awaiting their turn keep updated_at=T0. With defaults (publishTimeout 2min, staleDuration 5min), three slow publishes ahead in the batch push queued entries past the stale window, so another replica's ResetStaleProcessing reclaims them — both relays then publish the same entry. The heartbeat mechanism (added per relay_test.go:679 to prevent exactly this) does not protect queued claims.

**Suggested fix:** Heartbeat all claimed-but-unfinished ids per batch (Claimer.Heartbeat already accepts []string), instead of one goroutine per in-flight entry.

#### 362. [HIGH] Doc-mandated caller Zero() corrupts the shared cache entry
`infra/secrets/cache.go:148` — correctness — conf 0.90 — _reviewer-reported (unverified)_

Get hit path returns entry.value, whose Value is the cached *secret.String pointer. doc.go:51 instructs `defer s.Value.Zero()` and loader.go says 'Callers MUST Zero() when done'. A caller following the docs wipes the cache's shared buffer; every subsequent hit until TTL expiry silently returns an empty secret with nil error (entry.expiresAt is still in the future). Confirmed secret.String.Zero zeroes the shared inner buffer.

**Suggested fix:** Return a copied secret.String from Get (Value: secret.New(entry.value.Value.Reveal())), or remove the caller-Zero contract for cached Gets.

#### 363. [HIGH] fetchAndStore/Invalidate zero secrets still held by concurrent Get callers
`infra/secrets/cache.go:199` — concurrency — conf 0.85 — _reviewer-reported (unverified)_

fetchAndStore zeroes the prior entry's secret.String when storing a refreshed value, and Invalidate (line 184) does the same. Concurrent Gets in the refresh-due window return that exact pointer; the stale-while-revalidate refresh then completes (~ms later) and wipes the buffer before callers RevealString it, yielding "" mid-use. On the documented hot path (every DB connect fetches the password) this use-after-zero recurs every refreshAfter cycle, causing sporadic empty-credential auth failures.

**Suggested fix:** Stop zeroing displaced values (let GC handle), or hand out per-caller copies so the cache owns the only zeroizable buffer.

#### 364. [HIGH] Real *secretmanager.Client does not satisfy gcpsm.API — package unusable with real client
`infra/secrets/gcpsm/gcpsm.go:20` — api-design — conf 0.95 — _reviewer-reported (unverified)_

API declares AccessSecretVersion(..., opts ...option.ClientOption) but the real cloud.google.com/go/secretmanager/apiv1 Client method is (..., opts ...gax.CallOption) — verified via go doc against the pinned v1.20.0. Variadic types are part of the signature, so gcpsm.New(realClient) fails to compile. Doc comment 'The real [*secretmanager.Client] satisfies it' is false; only the test stub compiles. No adapter exists anywhere in the repo.

**Suggested fix:** Change variadic to ...gax.CallOption (BREAKING for stub implementors — but no real client can satisfy today), or add an additive adapter constructor wrapping *secretmanager.Client.

#### 365. [HIGH] Exists fails for existing zero-length blobs (416 InvalidRange)
`infra/storage/azurebackend/azure.go:386` — correctness — conf 0.70 — _reviewer-reported (unverified)_

Exists issues DownloadStream with Range bytes=0-0. Azure returns HTTP 416 / error code InvalidRange for any range request against a 0-byte blob. That error is not BlobNotFound, so Exists returns (false, error) for a blob that exists. Zero-byte objects (markers, .keep, placeholders) are common; siblings use HEAD-equivalents (s3backend HeadObject, gcsbackend Attrs). Also inflates the exists error metric.

**Suggested fix:** Use blob GetProperties (add to BlobClient interface) instead of ranged DownloadStream; or treat bloberror InvalidRange as exists=true.

#### 366. [HIGH] listImpl records a phantom breaker success on dispatch; stream failures never count
`infra/storage/circuitbreaker/breaker.go:281` — correctness — conf 0.80 — _reviewer-reported (unverified)_

cb.cb.Execute wraps only `seq = lister.List(ctx,...)`, which is lazy and always returns nil. gobreaker (MaxRequests=1, ConsecutiveFailures threshold) therefore records a success per List call: (a) in half-open, a single List dispatch closes the circuit without touching the backend; (b) in closed state, interleaved List calls reset ConsecutiveFailures so a dead backend may never trip; (c) errors yielded during iteration bypass breaker accounting entirely.

**Suggested fix:** Pull the first iteration step inside Execute, or feed iteration errors back into the breaker; never record bare dispatch as success. Internal-only fix, no API break.

#### 367. [HIGH] List treats prefix as a directory path, not a string prefix
`infra/storage/localbackend/list.go:43` — correctness — conf 0.85 — _reviewer-reported (unverified)_

When prefix is non-empty, List walks keyPath(prefix) as a directory. A non-directory-aligned prefix like "logs/2026-06-" resolves to a nonexistent path, WalkDir fails with d==nil, and the iterator returns zero results even though keys like "logs/2026-06-01/a.log" match. Lister contract says "objects whose keys start with prefix"; membackend and S3 use string-prefix matching. Also "foo" prefix misses sibling "foobar.txt".

**Suggested fix:** Walk the deepest existing directory component of the prefix (or the root) and rely solely on the existing strings.HasPrefix key filter.

#### 368. [HIGH] SFTP List yields unsorted keys, breaking StartAfter/ListPage pagination
`infra/storage/sftpbackend/list.go:148` — correctness — conf 0.82 — _verified_

walkDir yields objects in client.ReadDir() order, which pkg/sftp v1.13.10 does NOT sort (verified: no sort. calls in client.go); cross-directory recursion further scrambles order. storage.ListOptions.StartAfter is documented as a lexicographic cursor and storage.ListPage derives NextStartAfter from the last yielded key. With unordered output, paging via NextStartAfter skips or duplicates objects, causing silent data loss across pages. Tests pass only because the mock sorts ReadDir results.

**Suggested fix:** Collect entries per directory and sort by key before yielding, or sort each ReadDir result lexicographically, so StartAfter/ListPage produce complete, non-duplicating pages.

#### 369. [HIGH] JPEG polyglot bypass: trailing-bytes check only inspects last two bytes
`infra/storage/storagehttp/uploadsec/uploadsec.go:721` — security — conf 0.83 — _verified_

validateJPEGEnd only requires body[-2:]==0xFFD9 and returns at the first SOS without scanning the entropy stream for a second EOI. Go's jpeg.Decode stops at the first EOI and ignores trailing bytes (confirmed in stdlib reader.go). A polyglot of form <valid JPEG ...FFD9><PHP/JS payload>FFD9 decodes cleanly AND ends in FFD9, so it is accepted — defeating the package's stated 'any trailing data is rejected' guarantee. Untested.

**Suggested fix:** Parse JPEG segments fully and assert the body ends exactly at the first EOI (offset after FFD9 == len(body)), rejecting any bytes between the first EOI and end.

#### 370. [HIGH] GIF polyglot bypass: trailing-bytes check only inspects last byte
`infra/storage/storagehttp/uploadsec/uploadsec.go:779` — security — conf 0.82 — _verified_

validateGIFEnd only checks body[-1]==0x3B. gif.Decode returns at the first frame/trailer and ignores trailing data (stdlib reader.go returns nil on single-frame or sTrailer). A polyglot <valid GIF ...0x3B><payload>0x3B decodes cleanly and ends in 0x3B, so it is accepted — the appended payload survives. Contradicts the doc claim 'GIF with trailer 0x3B ... any trailing data is rejected'. Untested for the payload+terminator variant.

**Suggested fix:** Walk GIF blocks structurally and assert the trailer 0x3B is immediately followed by end-of-body, or reject any data after the first trailer.

#### 371. [MEDIUM] On drain timeout, OnLost runs while OnAcquired is still running
`infra/leaderelection/etcd/election.go:379` — concurrency — conf 0.70 — _reviewer-reported (unverified)_

When awaitCallbackDrain returns timedOut=true, runOnce proceeds to runOnLost(cb) even though the orphan OnAcquired goroutine is still executing. The interface contract (leaderelection.Callbacks) states OnLost runs 'after OnAcquired returns'; user cleanup code in OnLost can therefore race in-flight leader work over shared state, a data-race surface the kit's own contract promises away. Neither WithCallbackDrainTimeout's docs nor ErrCallbackDrainTimeout's docs mention that OnLost is invoked concurrently with the stalled callback.

**Suggested fix:** Skip OnLost on the timedOut path (process is declared unrecoverable anyway), or explicitly document that OnLost may run concurrently with the orphaned OnAcquired.

#### 372. [MEDIUM] Race with client-go's go OnStartedLeading can leave IsLeader() stuck true after Run returns
`infra/leaderelection/k8slease/lease.go:365` — concurrency — conf 0.60 — _reviewer-reported (unverified)_

client-go v0.36.1 Run does 'go OnStartedLeading(ctx); le.renew(ctx)' with deferred OnStoppedLeading. If renew returns quickly (ctx cancelled right after acquire, or immediate renew failure) before the goroutine is scheduled, OnStoppedLeading observes onAcquiredStarted==false, skips drain and OnLost, stores leader=false, and Run returns. The OnStartedLeading goroutine then runs afterwards: e.leader.Store(true) lands after the Store(false) and is never reset, cb.OnAcquired executes after Run returned, and OnLost is never invoked for that term. IsLeader() reports true forever.

**Suggested fix:** In OnStartedLeading check leaderCtx.Err() before Store(true)/invoking the callback, and have Run drain cbDone (or recheck onAcquiredStarted) after le.Run returns before exiting.

#### 373. [MEDIUM] Drain-timeout path invokes OnLost concurrently with the orphaned OnAcquired
`infra/leaderelection/k8slease/lease.go:372` — concurrency — conf 0.70 — _reviewer-reported (unverified)_

In OnStoppedLeading, when awaitCallbackDrain returns timedOut=true, runOnLost(cb) executes immediately while the OnAcquired goroutine is still running. This violates the kit contract that OnLost is invoked 'after OnAcquired returns' (leaderelection.Callbacks docs) and exposes user callback state to concurrent access the contract explicitly rules out. Same defect shape as the etcd adapter; the timeout path is the only place the ordering guarantee is silently broken.

**Suggested fix:** On timedOut, skip OnLost or document the concurrent invocation; mirror whatever resolution is chosen for the etcd adapter to keep cross-adapter behaviour identical.

#### 374. [MEDIUM] IsLeader stays true for entire callback drain after leadership is factually lost
`infra/leaderelection/pgadvisory/pgadvisory.go:241` — correctness — conf 0.78 — _reviewer-reported (unverified)_

leader.Store(false) runs only after holdLeadership returns, and holdLeadership blocks in awaitCallbackDrain until the callback exits (default: forever). So after Extend reports loss, IsLeader() keeps returning true for the whole drain while another replica is leader. IsLeader docs say it "may briefly return true"; the interface explicitly recommends IsLeader to gate per-tick work inside a long-running callback — exactly the consumer that keeps doing leader work during drain. Same defect in redislock.go:246.

**Suggested fix:** Store leader=false immediately when loss is detected (alongside cancel() in the loss branches), before awaiting drain. Apply to both packages.

#### 375. [MEDIUM] No Run-level drain-timeout test; H-008 test comment claims Run behavior it never verifies
`infra/leaderelection/pgadvisory/pgadvisory_test.go:183` — testing — conf 0.85 — _reviewer-reported (unverified)_

The comment on TestHoldLeadership_DrainTimeoutAbandonsStalledCallback asserts "Run returns ErrCallbackDrainTimeout joined with the underlying loss reason so the orchestrator can log + restart", but the test only drives holdLeadership. redislock has run_drain_timeout_test.go pinning L-141 at the Run boundary; pgadvisory has no equivalent, which is exactly why the Run retry-after-timeout bug (pgadvisory.go:267) survived. Integration tests don't cover it either.

**Suggested fix:** Add a pgadvisory analogue of TestRun_DrainTimeoutReturnsImmediatelyWithoutReacquire asserting Run returns ErrCallbackDrainTimeout and OnAcquired fires exactly once.

#### 376. [MEDIUM] Renew Extend unbounded; hung Redis call extends overlap window past documented one renewal interval
`infra/leaderelection/redislock/redislock.go:403` — concurrency — conf 0.65 — _reviewer-reported (unverified)_

handle.Extend(ctx) in the renew loop has no per-call deadline (unlike Release, which the comment at line 248 explicitly bounds to 5s for a "hung Redis"). If the client is configured without read timeouts, a hung Extend blocks the loop: the lock TTLs out, another replica becomes leader, and OnAcquired's ctx stays un-cancelled — overlap is unbounded, contradicting the package doc's "one renewal interval" claim (line 20-22). go-redis defaults (3s read timeout) mitigate but are not guaranteed.

**Suggested fix:** Bound each renew call with context.WithTimeout(ctx, e.renewInterval) and treat expiry as renewal failure.

#### 377. [MEDIUM] TestNewWithLocker_PanicsOnEmptyKey/NilOption pass vacuously — NewLocker(nil) panics first
`infra/leaderelection/redislock/redislock_test.go:264` — testing — conf 0.90 — _reviewer-reported (unverified)_

Both tests construct the locker argument via rlock.NewLocker(nil), which unconditionally panics ("redislock: NewLocker requires a non-nil Redis client", data/lock/redislock/lock.go:138) before NewWithLocker is ever invoked. The deferred recover catches that unrelated panic, so the empty-key guard (redislock.go:179) and nil-option guard (redislock.go:191) are never actually exercised. Neither test asserts the panic message, so the wrong-panic pass is invisible.

**Suggested fix:** Build a valid locker (miniredis client or non-nil stub client) and assert the recovered panic message mentions "key must not be empty" / "option must not be nil".

#### 378. [MEDIUM] Stop racing an in-flight reconnect dial leaves a live, unclosed zombie connection
`infra/messaging/amqpbackend/connection.go:614` — concurrency — conf 0.80 — _reviewer-reported (unverified)_

reconnect() never re-checks c.closed after a successful dial. If Stop() runs (closes c.closed, closes c.conn) while dial() is in flight and the dial then succeeds, the loop installs the new live connection into c.conn; the watcher exits immediately via c.closed, closeOnce is spent, so nothing ever closes it. After Stop returns: TCP connection leaks, Healthy() returns true, connection_up gauge reads 1, and onReconnect runs post-Stop. Plausible during graceful shutdown while the broker flaps.

**Suggested fix:** After dial success, under c.mu check c.closed (select/default); if closed, close the new conn and return instead of installing it.

#### 379. [MEDIUM] Lost reconnect signal between final drain and reconnecting.Store(false)
`infra/messaging/amqpbackend/connection.go:674` — concurrency — conf 0.65 — _reviewer-reported (unverified)_

The R7-46 fix is incomplete: the loop drains reconnectSignal, returns, and only then the deferred reconnecting.Store(false) runs. A watcher whose connection drops in that window calls startReconnect, fails the CAS (flag still true), queues a signal, and exits. The signal sits in the buffer with no loop running and no other trigger — the connection stays down forever with Healthy()=false, Dead() never closed, no recovery path.

**Suggested fix:** In the goroutine, after reconnecting.Store(false), non-blockingly re-check reconnectSignal and re-run startReconnect if a signal is pending.

#### 380. [MEDIUM] DLQ consecutive-failure force-discard branch is completely untested
`infra/messaging/amqpbackend/consumer.go:518` — testing — conf 0.85 — _reviewer-reported (unverified)_

routeToDeadExchange's capped path (fails > maxDLQConsecutiveFail → ack-and-force-discard, i.e. deliberate message shedding) has zero test coverage — grep shows only the option's panic-on-nonpositive test. newTestConsumer constructs Consumer directly with maxDLQConsecutiveFail=0 (uncapped), so neither the counter reaching the cap, the reset-on-success at line 542, nor the dlq_publish_failed→force_discarded transition is ever exercised. A regression in this loss-inducing path would ship silently.

**Suggested fix:** Add tests: N consecutive PublishRaw failures crossing the cap must ack+OnDiscard+force_discarded outcome; a success in between must reset the counter.

#### 381. [MEDIUM] Retried deliveries arrive with RoutingKey rewritten to the binding key/pattern
`infra/messaging/amqpbackend/topology.go:168` — correctness — conf 0.70 — _reviewer-reported (unverified)_

x-dead-letter-routing-key on the retry queue is b.RoutingKey — the BINDING key. For topic exchanges this is a pattern (e.g. "orders.*"), for fanout it may be empty. After one retry bounce, the consumer's messaging.Delivery.RoutingKey is the literal pattern/empty string instead of the original publish key, so handlers that branch on RoutingKey behave differently on retried deliveries than on first delivery.

**Suggested fix:** Route retries back via the default exchange to the queue (fixes the duplication finding too); preserve original routing key from x-death routing-keys if needed.

#### 382. [MEDIUM] Every buffered Publish rewrites the entire pending list to disk with fsync while holding mu
`infra/messaging/buffered_publisher.go:531` — performance — conf 0.75 — _reviewer-reported (unverified)_

saveLocked serializes the full o.pending slice (atomicfile.Save: marshal + temp write + Sync + rename + dir Sync) under o.mu on every single buffered Publish. During a broker outage burst — exactly when buffering happens — persisting n messages costs O(n^2) total bytes written, and all concurrent publishers serialize behind one fsync. With the 10k default cap and KB-size payloads this is tens of MB written per message near capacity, inflating Publish latency and disk wear precisely under stress.

**Suggested fix:** Append-only journal (write the new entry, compact on drain) or persist outside the mutex with a dirty-flag/coalescing writer.

#### 383. [MEDIUM] finalDrain drains at most 100 messages regardless of finalDrainTimeout
`infra/messaging/buffered_publisher.go:622` — correctness — conf 0.85 — _reviewer-reported (unverified)_

finalDrain calls drain(ctx) exactly once, and drain processes at most one 100-message batch. With >100 pending at shutdown and a healthy broker, up to pending-100 messages are left unsent even when the 15s WithFinalDrainTimeout budget is barely used. With WithEphemeralBuffer this is silent message loss on graceful shutdown; with a state file they are stranded until the next restart, contradicting the 'final best-effort drain so in-flight messages are not lost' doc.

**Suggested fix:** Loop drain() in finalDrain until pending==0, no progress, or the detached context expires.

#### 384. [MEDIUM] FR-073 gate accepts SASL/PLAIN over plaintext TCP without AllowInsecure
`infra/messaging/kafkabackend/client.go:118` — security — conf 0.50 — _reviewer-reported (unverified)_

ValidateConfig treats any SASLMechanism as satisfying the safety contract even with TLS nil, so SASL/PLAIN sends the password (and all traffic) in cleartext, and SCRAM is MITM-exposed without channel security — no AllowInsecure opt-in required. This mirrors natsbackend's 'auth satisfies FR-073' rule, so it appears to be a kit-wide design decision, but PLAIN-without-TLS is materially weaker than the other accepted combinations.

**Suggested fix:** Require TLS (or AllowInsecure) when SASLMechanism is PLAIN; at minimum log a startup warning for SASL-without-TLS.

#### 385. [MEDIUM] Consume/dispatch logic entirely untested at unit level; ErrRetryUnsupported path untested anywhere
`infra/messaging/kafkabackend/subscriber_test.go:13` — testing — conf 0.70 — _reviewer-reported (unverified)_

Unit tests cover only constructor validation and binding pre-checks. No test exercises dispatch(): decode-error commit, validation-failure commit, handler-panic commit, permanent-vs-transient outcomes, commitWithOutcome failure, or the Binding.Retry != nil → messaging.ErrRetryUnsupported rejection (grep finds no test in the repo, including testing/integrationtest/kafkabackend). Integration tests cover only happy path and poison-pill.

**Suggested fix:** Add unit tests for Consume's Retry rejection and dispatch outcomes using a local kafka.Reader test double or refactor dispatch to accept a commit func.

#### 386. [MEDIUM] Concurrent Drain/PublishAndDrain can dispatch the same message multiple times
`infra/messaging/membroker/membroker.go:207` — concurrency — conf 0.75 — _reviewer-reported (unverified)_

Drain reads pm := b.published[0] under the mutex, releases it to run handlers, then removes the entry afterwards. Two goroutines calling Drain (e.g. concurrent PublishAndDrain in tests) can both read the same head entry before either removes it, so subscribers receive the message twice. The second remover silently no-ops via removePublishedLocked. For a deterministic test broker this produces flaky duplicate-delivery assertions.

**Suggested fix:** Pop the head entry under the lock before dispatch (re-append on handler error), or serialize Drain with a dedicated drain mutex.

#### 387. [MEDIUM] Wrapper Consume/ConsumeOnce panic from deep inside redisstream on second invocation
`infra/messaging/redisbackend/consumer.go:68` — error-handling — conf 0.70 — _verified_

redisbackend.Consumer.Consume calls stream.Consumer.Consume, which does consumed.CompareAndSwap(false,true) and panics 'called for a second stream' on any second call. Since ConsumeOnce just delegates to Consume, calling Consume twice, ConsumeOnce twice, or one then the other on a single wrapper panics across package boundaries rather than returning an error. The wrapper presents a re-callable interface but the underlying object is single-shot.

**Suggested fix:** Guard with an atomic 'consumed' flag in the wrapper returning a clear error (or messaging.ErrInvalidConsumer) on re-invocation instead of letting redisstream panic.

#### 388. [MEDIUM] consumer_test.go struct literal not gofmt-aligned (CI lint failure)
`infra/messaging/redisbackend/consumer_test.go:54` — build-ci — conf 0.95 — _verified_

gofmt -l flags consumer_test.go: the BindingSpec literal field ConsumerGroup (L54) is over-indented relative to Exchange. CI gofmt/lint gate would fail on this test file.

**Suggested fix:** Run gofmt -w on consumer_test.go.

#### 389. [MEDIUM] Stop racing Start can miss cancellation: cancel stored after started CAS
`infra/messaging/subscription.go:160` — concurrency — conf 0.60 — _reviewer-reported (unverified)_

Start sets started via CompareAndSwap (line 116) and only later stores the cancel func (line 122). A concurrent Stop that observes started==true in that window swaps a nil cancel pointer (line 160), cancels nothing, then blocks on s.done until its own ctx deadline while Consume keeps running on the un-cancelled runCtx. The consumer is then only stoppable via the parent context; the doc claims every method is safe for concurrent use.

**Suggested fix:** Store the cancel pointer before (or atomically with) the started CAS, or have Stop spin/wait for cancel to be published once started is true.

#### 390. [MEDIUM] Stop before/concurrent with Start permanently consumes stopOnce; group becomes uncancellable via Stop
`infra/messaging/subscription_group.go:146` — concurrency — conf 0.60 — _reviewer-reported (unverified)_

Stop's stopOnce.Do runs unconditionally — called before Start has stored cancel (line 105), it swaps nil and burns the once. Every later Stop is then a no-op on cancellation and merely waits on wg until its ctx deadline; the group can only be stopped via the parent context. Additionally Stop's goroutine calls g.wg.Wait() which can start while the counter is 0 concurrently with Start's wg.Add(len(subs)) (line 109), violating WaitGroup reuse rules. Subscription.Stop guards with a started check; the group does not.

**Suggested fix:** Mirror Subscription: check started before consuming stopOnce, or replace stopOnce with an idempotent cancel-pointer swap that Start publishes before launching goroutines.

#### 391. [MEDIUM] ValidateBindingSpecs only checks ConsumerGroup for emptiness — no charset/length validation
`infra/messaging/topology.go:146` — consistency — conf 0.70 — _reviewer-reported (unverified)_

Exchange and RoutingKey get full portable validation (length<=255, UTF-8, no control/whitespace) but ConsumerGroup is only checked non-empty. amqpbackend/topology.go uses ConsumerGroup verbatim as queue names, x-dead-letter-routing-key, and derives '.retry'/'.dead' names; AMQP shortstr caps names at 255 bytes and reserves the 'amq.' prefix. A ConsumerGroup with control chars, whitespace, or >255 bytes passes shared validation and fails (or misbehaves) only at broker-declaration time, inconsistent with the package's fail-fast posture.

**Suggested fix:** Validate ConsumerGroup with the same token rules as exchange names (length, UTF-8, no control/whitespace), accounting for the '.retry'/'.dead' suffix budget.

#### 392. [MEDIUM] Schema validation silently bypassed for any unregistered version supplied via transport header
`infra/messaging/validating_handler.go:37` — security — conf 0.60 — _reviewer-reported (unverified)_

NewValidatingHandler looks up by Delivery.SchemaVersion, which is populated from a transport header the producer (or any peer that can publish) controls. InMemorySchemaRegistry.ValidateMessage returns nil whenever no schema matches (schema.go:141-143) — including unknown nonzero versions of a type that HAS registered schemas. Sending X-Schema-Version: 999 therefore skips validation entirely; the handler then processes the unvalidated payload unless callers also compose NewVersionedHandler. Behavior matches the doc for version 0, but pass-through for arbitrary unknown versions is an unsafe default.

**Suggested fix:** Additive: reject deliveries whose type has registered schemas but whose version is unregistered (or add a strict-mode option); keep version-0 pass-through for legacy.

#### 393. [MEDIUM] fakeStore.FetchPending ignores NextRetryAt, violating the documented Store contract and masking backoff behavior in all relay tests
`infra/outbox/outbox_test.go:68` — testing — conf 0.80 — _reviewer-reported (unverified)_

Store.FetchPending's contract (store.go:24, Entry doc outbox.go:64-69) requires skipping entries whose NextRetryAt is in the future. The fake only checks Status==pending, so unit tests never exercise backoff gating. TestRelay_RecoverAfterPublisherError (relay_test.go:759) passes only because of this divergence: a contract-faithful fake would defer the retry by retryBackoff(1)=2s, exactly the Eventually timeout, making the test fail/flake. Backoff-skip behavior is effectively untested outside integration.

**Suggested fix:** Make fakeStore honor NextRetryAt and add a unit test asserting entries with future NextRetryAt are not fetched.

#### 394. [MEDIUM] Outcome updates guard only on status='processing', not claim ownership — a stale-reset-and-reclaimed row can be terminated by the wrong relay
`infra/outbox/postgres/store.go:215` — concurrency — conf 0.70 — _reviewer-reported (unverified)_

MarkPublished/MarkFailed/IncrementAttempts match WHERE id AND status='processing' with no fencing token. If relay A's claim is stale-reset and relay B reclaims the row, A's late MarkFailed/IncrementAttempts succeeds against B's claim: a row B just published can end up 'failed' (operator retry → duplicate) or reset to 'pending' (third publish). The ErrStaleState design only detects the race when the row already left 'processing', not when it re-entered it under another owner.

**Suggested fix:** Additive: add a claim_token/claimed_by column set in FetchPending and required in outcome UPDATEs; new optional Store capability, default unchanged.

#### 395. [MEDIUM] Shutdown abandons claimed-but-unpublished entries in 'processing' for the full staleDuration, delaying delivery ~5 minutes on every restart
`infra/outbox/relay.go:365` — correctness — conf 0.70 — _reviewer-reported (unverified)_

When Stop cancels the run context mid-batch, poll() returns immediately, leaving already-claimed entries in 'processing'. No reset-on-shutdown exists; recovery waits for a stale sweep (updated_at older than staleDuration, default 5min). On a busy service, every deploy/rolling restart strands part of a batch (up to batchSize=100 messages) with a multi-minute delivery delay, despite the rows being known-unpublished at exit.

**Suggested fix:** On shutdown, reset claimed-but-unpublished entry ids back to 'pending' with a short-deadline context before Start returns.

#### 396. [MEDIUM] Publish failures carry zero diagnostic signal: constant "publish failed" in both last_error and logs, real error never logged even redacted
`infra/outbox/relay.go:547` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

safePublishError returns the constant "publish failed" for every error; handlePublishError logs only that constant ("error", errMsg) and stores it as last_error. Unlike every other error path in this file (which uses redact.Error(err) to at least surface the concrete type), the actual publishErr is discarded entirely. doc.go:36 claims failed entries are retained "for manual inspection", but last_error is always the same string — operators cannot distinguish timeout vs auth vs routing failures from DB or logs.

**Suggested fix:** Log redact.Error(publishErr)/redact.ErrorChain(publishErr) in handlePublishError; store redact.ErrorValue(publishErr) (safe typed form) as last_error.

#### 397. [MEDIUM] FR-077 anonymous-Redis guard counts Config.Password that Options() never uses
`infra/redis/config.go:109` — security — conf 0.80 — _reviewer-reported (unverified)_

checkFR077 passes when c.Password != "" even though, when URL is set, Options() builds opts solely via goredis.ParseURL(c.URL) and RedisURL() returns URL as-is — the Password field is silently ignored. Config{URL:"rediss://host:6380/0", Password:"x"} passes both checkFR077 and ValidateRedis yet connects with no credential, defeating the FR-077 'no anonymous Redis' guarantee the check claims to enforce.

**Suggested fix:** When c.URL != "", consider only URL userinfo in checkFR077; or inject c.Password into the parsed Options.

#### 398. [MEDIUM] Close racing an in-flight successful ping leaves connection permanently Healthy()
`infra/redis/connection.go:463` — concurrency — conf 0.70 — _reviewer-reported (unverified)_

checkHealth never consults c.closed. If Close() runs while a ping is in flight and succeeds, checkHealth may acquire c.mu after Close set healthy=false and set healthy=true plus connection_healthy=1; healthLoop then exits via the closed channel and nothing ever corrects the state. Result: a closed Connection reports Healthy()==true to health.DependencyCheck and the gauge reads 1 indefinitely. The Connect WARNING comment acknowledges only the harmless spurious-log variant, not this sticky-healthy outcome.

**Suggested fix:** In checkHealth, re-check c.closed under c.mu before setting healthy=true (or have Close set a guarded closed flag).

#### 399. [MEDIUM] Backends violate Loader contract: shape errors wrap neither sentinel
`infra/secrets/awssm/awssm.go:79` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

loader.go:44 states any non-NotFound error MUST wrap ErrLoaderUnavailable. Violations: awssm 'no SecretString or SecretBinary' (line 79), gcpsm 'empty payload' (gcpsm.go:83), vaultkv missing-field and wrong-type errors (vaultkv.go:79,83). These return bare fmt.Errorf errors, so CachedLoader skips stale fallback: a bad rotation that writes a malformed secret causes an immediate hard failure instead of serving the stale value within MaxStale, and callers switching on the two documented sentinels get an unclassified error.

**Suggested fix:** Wrap these in ErrLoaderUnavailable (or document a third error class in the Loader contract).

#### 400. [MEDIUM] gcpsm panics at Get time instead of returning an error
`infra/secrets/gcpsm/gcpsm.go:100` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

resolveName panics on a bare key when WithProject was omitted. This is a runtime request-path panic, not a construction-time check: it fires on first Get, and when triggered inside CachedLoader.spawnRefresh's background goroutine (which has no recover, unlike redis fireOnReconnect) it crashes the whole process. The Loader contract communicates failures via returned errors.

**Suggested fix:** Return an error from Get for bare keys without project; keep panic only in New/options.

#### 401. [MEDIUM] singleflight not panic-safe: panicking fn permanently deadlocks the key
`infra/secrets/singleflight.go:37` — concurrency — conf 0.85 — _reviewer-reported (unverified)_

If fn() panics, call.wg.Done() and the map delete never run (no defer). The poisoned flightCall stays in sf.m; every future do() for that key blocks forever in call.wg.Wait(). gcpsm.resolveName panics at Get time (bare key without WithProject), so a recovered panic (e.g. HTTP handler recovery middleware) leaves all subsequent CachedLoader.Get calls for that secret hung indefinitely. x/sync/singleflight explicitly handles this case; this reimplementation does not.

**Suggested fix:** Wrap fn execution: defer wg.Done() and defer map cleanup; record/propagate panics to waiters like x/sync/singleflight.

#### 402. [MEDIUM] HealthCheck discards the framework's cancellation context
`infra/sqldb/health.go:28` — resource-leak — conf 0.78 — _verified_

The DependencyCheck.Check closure receives ctx (which the health framework cancels on its cooperative Timeout) but calls pinger.Ping() with no context. The Pinger interface has no ctx parameter, so a hung ping keeps running and holds a DB connection after kubelet has given up — exactly the anti-pattern observability/health/health.go:201-210 warns against.

**Suggested fix:** Add a ctx-aware Pinger variant (e.g. PingContext(ctx) error) and thread the supplied ctx; keep the legacy Pinger as an additive overload. BREAKING if Pinger signature changes — v3 candidate.

#### 403. [MEDIUM] Shared healthyReplicas/replicaCount gauges corrupt across RoutingPool instances
`infra/sqldb/readreplica/metrics.go:73` — concurrency — conf 0.68 — _verified_

On AlreadyRegisteredError the gauges are adopted from the registry, then New() does healthyReplicas.Set / replicaCount.Set (readreplica.go:176-177) and later Inc/Dec per replica. Two RoutingPools sharing a registerer (DefaultRegisterer is the default) clobber each other's gauge: Set overwrites, and concurrent Inc/Dec produce a meaningless aggregate. Counters are additively safe; gauges are not.

**Suggested fix:** Add a per-pool instance label to the gauges (like pgx PoolStatsCollector), or fail when a gauge with different ownership is already registered.

#### 404. [MEDIUM] InsufficientAccountPermissions misclassified as storage-capacity error
`infra/storage/azurebackend/azure.go:285` — correctness — conf 0.60 — _reviewer-reported (unverified)_

translateAzureCapacity maps the Azure error code 'InsufficientAccountPermissions' (an authorization / account-disabled condition, typically HTTP 403) to storage.ErrInsufficientCapacity ('account out of capacity'). A misconfigured SAS/RBAC Put would satisfy apperror.IsStorageFull and trigger capacity handling/alerts instead of surfacing an auth failure, materially misleading operators.

**Suggested fix:** Drop InsufficientAccountPermissions from the capacity switch; let it fall through to the generic WrapSafe mapping.

#### 405. [MEDIUM] Default ShouldTrip counts client cancellations (context.Canceled) as breaker failures
`infra/storage/circuitbreaker/breaker.go:132` — correctness — conf 0.65 — _reviewer-reported (unverified)_

The package's default ShouldTrip trips on any error except ErrObjectNotFound/ErrValidation. This overrides kitcb's deliberate default (defaultIsSuccessful) that treats context.Canceled as success so a flood of caller cancellations cannot trip the circuit. Through this wrapper, N consecutive client-cancelled Puts/Gets (slow clients navigating away) open the circuit and block all storage traffic for ResetTimeout despite a healthy backend. WrapSafe preserves errors.Is so the exclusion is implementable.

**Suggested fix:** Add !errors.Is(err, context.Canceled) to the default predicate (additive behavior fix), matching resilience/circuitbreaker's documented default.

#### 406. [MEDIUM] Move with srcKey == dstKey permanently deletes the object
`infra/storage/copy.go:51` — correctness — conf 0.80 — _reviewer-reported (unverified)_

Move performs Copy then Delete(srcKey) with no guard for identical keys. Copy onto the same key succeeds (e.g. localbackend uses temp+rename, verified in localbackend/copy.go), then Delete removes the only copy — a no-op request destroys data. Computed src/dst keys that occasionally coincide will hit this in production.

**Suggested fix:** Add `if srcKey == dstKey { return nil }` (or a validation error) before copying. Additive, no API break.

#### 407. [MEDIUM] Get buffers up to ~512MiB per call with no concurrency bound, unlike Put
`infra/storage/encryption/encryption.go:316` — performance — conf 0.75 — _reviewer-reported (unverified)_

Put is gated by putSem precisely because each in-flight op can hold plaintext+ciphertext (~2×256MiB) and 'unbounded fan-out can exhaust memory'. Get has the identical profile — io.ReadAll of up to MaxEncryptableSize+28 ciphertext plus a full plaintext buffer held until cleaningReader.Close — but no semaphore. Download endpoints typically see higher fan-out than uploads, so the documented OOM vector remains fully open via Get (and Copy, which holds both ops' buffers).

**Suggested fix:** Apply the same (or a separate) semaphore to Get, with ctx-aware acquire; additive option mirroring WithMaxConcurrentEncryptions.

#### 408. [MEDIUM] All gcsbackend CRUD logic is untested — no client seam exists
`infra/storage/gcsbackend/gcs.go:136` — testing — conf 0.85 — _reviewer-reported (unverified)_

Backend stores the concrete *gcsstorage.Client/BucketHandle (unlike azurebackend's BlobClient interface), so Put/Get/Delete/Exists are untestable without a fake server, and indeed have zero coverage: no test exercises the resumable-upload abort path, generation pinning, in-path capacity translation, not-found sentinel mapping, or get-metric contract. storagetest's conformance suite covers only local/s3/sftp. Only constructor panics, config, and pure helpers are tested.

**Suggested fix:** Introduce an interface seam like azurebackend's BlobClient, or wire fake-gcs-server into an integration suite; extend storagetest to gcs.

#### 409. [MEDIUM] Get: NewReader error not mapped to ErrObjectNotFound nor routed through gcsMetricErr
`infra/storage/gcsbackend/gcs.go:247` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

Get pins attrs.Generation specifically to handle the Attrs→NewReader race, but if the object is deleted in that window NewReader returns ErrObjectNotExist, which this path wraps as a generic 'gcsbackend: get failed' error (callers checking errors.Is(err, storage.ErrObjectNotFound) misclassify) and passes raw to observeOp, inflating operation_errors_total — violating the not-found metric contract documented 13 lines above.

**Suggested fix:** Check errors.Is(err, gcsstorage.ErrObjectNotExist) on the NewReader error: return storage.ErrObjectNotFound and observe via gcsMetricErr(err).

#### 410. [MEDIUM] TestNewWithClient_PanicsOnEmptyBucket passes for the wrong reason
`infra/storage/gcsbackend/gcs_test.go:16` — testing — conf 0.90 — _reviewer-reported (unverified)_

The test calls NewWithClient(nil, Config{Bucket: ""}). NewWithClient checks client==nil first, so the asserted panic is the nil-client panic; the empty-bucket panic path is never exercised, despite the test name. The azure sibling test correctly uses a stub client with an empty container. There is also a stray outer `defer func(){ _ = recover() }()` that is dead code.

**Suggested fix:** Pass a non-nil client (e.g. &gcsstorage.Client{}) with Bucket: ""; delete the stray outer recover.

#### 411. [MEDIUM] BatchDeleter discovery bypasses Before/AfterDelete hooks through the hooks wrapper
`infra/storage/hooks.go:107` — api-design — conf 0.70 — _reviewer-reported (unverified)_

hookedStorage exposes Unwrap() and is not an OpaqueDecorator, and WithHooks only forwards Lister/Copier/PresignedStore/PublicURLer. AsBatchDeleter (used by storage.DeleteMany) walks past the wrapper to a consumer backend's native DeleteMany, so BeforeDelete (documented as an abort hook) and AfterDelete silently never fire for batch deletes — while the sequential fallback path does fire them. Same bypass applies to Tagger/Versioner/Multipart, though those have no hooks.

**Suggested fix:** Forward BatchDeleter on the hooks wrapper (invoking BeforeDelete/AfterDelete per key), or document that batch deletes bypass hooks.

#### 412. [MEDIUM] ListPage probe of MaxKeys+1 always fails at MaxKeys == MaxListPageSize
`infra/storage/list.go:113` — correctness — conf 0.85 — _reviewer-reported (unverified)_

ListPage validates opts (MaxKeys <= MaxListPageSize passes), then builds probe.MaxKeys = MaxKeys+1 and passes it to the backend. Every in-tree Lister (membackend, localbackend, s3backend, sftpbackend, retry, circuitbreaker, encryption, hooks wrapper) calls ValidateListOptions on the incoming options, which rejects MaxListPageSize+1. So ListPage with the documented maximum page size always returns a validation error instead of a page.

**Suggested fix:** Cap the probe at MaxListPageSize (treat a full max-size page as Truncated via a second peek), or reject MaxKeys == MaxListPageSize in ListPage explicitly.

#### 413. [MEDIUM] Copy does not map ENOSPC to storage.ErrInsufficientCapacity, unlike Put
`infra/storage/localbackend/copy.go:79` — error-handling — conf 0.90 — _reviewer-reported (unverified)_

Put maps syscall.ENOSPC from io.Copy and Sync to wrapInsufficientCapacity (507 sentinel, retryable). Copy's identical write/sync sequence returns localFileError, whose default branch produces "copy write failed" with no sentinel and no error chain. A disk-full Copy is therefore indistinguishable from a permanent failure; apperror.IsStorageFull returns false.

**Suggested fix:** Apply the same errors.Is(err, syscall.ENOSPC) → wrapInsufficientCapacity mapping in Copy's write/sync (and Put/Copy rename) paths.

#### 414. [MEDIUM] List yields keys in WalkDir order, not lexicographic key order, breaking StartAfter pagination
`infra/storage/localbackend/list.go:59` — correctness — conf 0.80 — _reviewer-reported (unverified)_

WalkDir sorts per-directory entry names, so with keys "foo.txt" and "foo/bar" it yields "foo/bar" before "foo.txt" ('.' < '/'). storage.ListPage sets NextStartAfter to the last yielded key; the next page's filter `key <= StartAfter` then permanently skips "foo.txt". membackend sorts keys; localbackend silently loses objects under keyset pagination.

**Suggested fix:** Collect matching keys, sort lexicographically, then yield; or document and fix ListPage interaction.

#### 415. [MEDIUM] Mid-walk context cancellation silently truncates List results without an error
`infra/storage/localbackend/list.go:60` — correctness — conf 0.90 — _reviewer-reported (unverified)_

Inside the walk callback, ctx.Err() != nil returns fs.SkipAll, which WalkDir treats as success; the final `if err != nil && !stopped` never fires, so the iterator ends cleanly. A caller whose ctx is cancelled mid-listing sees a complete-looking but truncated result. membackend yields ctx.Err() on every iteration, so siblings disagree. The existing cancellation test only covers a pre-cancelled ctx caught at entry.

**Suggested fix:** Yield ctx.Err() (when !stopped) after returning fs.SkipAll due to cancellation, matching membackend.

#### 416. [MEDIUM] Temp files created inside object namespace leak into List and survive crashes
`infra/storage/localbackend/local.go:122` — consistency — conf 0.80 — _reviewer-reported (unverified)_

Put and Copy create ".tmp-*" files in the destination directory. A concurrent List walk yields these as objects (keys like "a/.tmp-123456" pass no filter), and a crash between CreateTemp and Rename leaves orphaned temp files permanently visible to List/Get/Exists with no cleanup path. ValidateKey accepts ".tmp-..." segments, so callers cannot distinguish them from real objects.

**Suggested fix:** Skip basenames matching ".tmp-*" during List, or write temps in a reserved subdirectory excluded from key mapping; consider startup/opportunistic orphan cleanup.

#### 417. [MEDIUM] ObjectMeta is silently dropped: ContentType/Custom never persisted, Get returns Size only
`infra/storage/localbackend/local.go:206` — api-design — conf 0.80 — _reviewer-reported (unverified)_

Put validates meta then discards it; nothing is stored. Get returns ObjectMeta{Size} (and silently ignores Stat errors), List never sets ContentType. membackend round-trips ContentType and Custom, and the ObjectMeta doc says backends "should attempt detection or default to application/octet-stream". Code tested against membackend behaves differently on localbackend with no documentation of the divergence.

**Suggested fix:** Persist meta in a sidecar (e.g. xattr or .meta file) or document the limitation prominently in the package doc.

#### 418. [MEDIUM] existingRegularPath accepts directories: Get/Exists succeed on implicit directory keys
`infra/storage/localbackend/local.go:290` — correctness — conf 0.80 — _reviewer-reported (unverified)_

existingRegularPath only rejects symlinks, not directories. After Put("a/b"), Exists(ctx, "a") returns true and Get(ctx, "a") returns a ReadCloser over a directory handle with no error; the first Read fails with a raw *os.PathError whose message contains the absolute root path — bypassing the package's path-redaction policy. membackend and S3 return false/ErrObjectNotFound for such keys.

**Suggested fix:** In existingRegularPath, return ErrObjectNotFound (or a redacted error) when info.Mode().IsRegular() is false.

#### 419. [MEDIUM] Symlink defenses are Lstat-then-act and racy (TOCTOU)
`infra/storage/localbackend/local.go:296` — security — conf 0.70 — _reviewer-reported (unverified)_

rejectSymlinkPath/existingRegularPath check components with Lstat, then separately MkdirAll/Open/Rename/Remove. Between the final check and the syscall, a component can be swapped for a symlink, letting Put/Copy write or Get read outside the root. The package invests heavily in symlink rejection (tests pin it), but the guarantee does not hold under concurrent modification of the root tree.

**Suggested fix:** Use os.Root (Go 1.24+) for traversal-safe Open/Create/Mkdir/Rename, eliminating the race instead of pre-checking.

#### 420. [MEDIUM] DryRun cannot report what would happen, contradicting MigrateOptions doc
`infra/storage/migrate.go:143` — api-design — conf 0.65 — _reviewer-reported (unverified)_

MigrateOptions.DryRun doc says "OnProgress will still be called with what would happen", but the DryRun branch reports copied=false and increments Skipped for objects that WOULD be copied — indistinguishable from genuinely skipped (already-existing) objects in both the callback and MigrateResult counters. A dry run therefore cannot preview the copy set, which is its main purpose.

**Suggested fix:** Report would-copy objects distinctly (e.g. copied=true in DryRun, or add a WouldCopy counter) or fix the doc to state everything is reported as skipped.

#### 421. [MEDIUM] New doc claims List is retried, but listImpl performs no retries
`infra/storage/retry/retry.go:104` — docs — conf 0.95 — _reviewer-reported (unverified)_

New's doc comment states "list calls are retried under the same policy as the core methods", and the RetryStorage doc says optional capabilities are "forwarded through the retry policy". listImpl returns lister.List directly with zero retry logic; combinators.go even documents that mid-iteration retries are deliberately not attempted. Callers relying on the documented retry semantics for transient list failures get none.

**Suggested fix:** Correct the New/RetryStorage doc comments to state List is forwarded without retry, and why.

#### 422. [MEDIUM] Put retry rewinds seekable reader to offset 0, not its initial position
`infra/storage/retry/retry.go:181` — correctness — conf 0.80 — _reviewer-reported (unverified)_

On retry, the reader is rewound with Seek(0, io.SeekStart). If the caller passed a seekable reader positioned mid-stream (e.g. an *os.File after a header was consumed), the first attempt uploads from the current offset while retries upload from byte 0 — the retried object silently contains different, larger content than the intended payload.

**Suggested fix:** Capture start := Seek(0, io.SeekCurrent) before the first attempt and rewind to that offset on retries.

#### 423. [MEDIUM] Put retry behavior is entirely untested (rewind and non-seekable bypass)
`infra/storage/retry/retry_test.go:334` — testing — conf 0.90 — _reviewer-reported (unverified)_

failingBackend has no putFn hook and no test injects a Put failure. The package's most subtle documented behaviors — rewinding a seekable reader between attempts, returning the first error immediately for non-seekable readers, and per-attempt meta cloning — have zero coverage. A regression (e.g. retrying a consumed reader and uploading empty content) would pass the suite. Copy retry forwarding is also untested.

**Suggested fix:** Add tests: transient Put failure with bytes.Reader asserting full content after retry, and non-seekable reader asserting exactly one attempt.

#### 424. [MEDIUM] CopySource leaves '+' unescaped; S3 decodes it as space
`infra/storage/s3backend/copy.go:37` — correctness — conf 0.60 — _reviewer-reported (unverified)_

Copy builds CopySource with url.PathEscape, which leaves '+' literal (allowed in path segments). S3 URL-decodes x-amz-copy-source treating '+' as a space (documented AWS gotcha; SDKs encode it as %2B). storage.ValidateKey permits '+', so copying any key containing '+' fails with NoSuchKey — or silently copies the wrong object if a space-variant key exists.

**Suggested fix:** After PathEscape, also replace "+" with "%2B" in the encoded source key (per AWS CopyObject URL-encoding guidance).

#### 425. [MEDIUM] Copy of missing source not mapped to storage.ErrObjectNotFound
`infra/storage/s3backend/copy.go:53` — consistency — conf 0.75 — _reviewer-reported (unverified)_

membackend and localbackend Copy return storage.ErrObjectNotFound for a missing source; s3backend returns only a generic WrapSafe error. SDK's CopyObject deserializer (verified in module cache) models only ObjectNotInActiveTierError — NoSuchKey arrives as *smithy.GenericAPIError, so even isS3NotFound would miss it. Portable callers using errors.Is(err, storage.ErrObjectNotFound) (e.g. around storage.Move) behave differently between test backends and S3 production.

**Suggested fix:** errors.As to smithy.APIError and map codes NoSuchKey/NotFound (404) to fmt.Errorf("s3backend: copy: %w", storage.ErrObjectNotFound).

#### 426. [MEDIUM] PresignPutURL silently ignores meta.Size — unbounded presigned uploads
`infra/storage/s3backend/presign.go:69` — security — conf 0.60 — _reviewer-reported (unverified)_

PresignPutURL accepts storage.ObjectMeta but never uses meta.Size: ContentLength is not set on the PutObjectInput, so Content-Length is not among the signed headers. A URL holder can upload up to 5 GiB regardless of the size the caller authorized, and WithValidators size limits (enforced only in Put) are fully bypassed on the presigned path. Callers passing Size reasonably assume it is enforced.

**Suggested fix:** Set input.ContentLength = aws.Int64(meta.Size) when meta.Size > 0 so SigV4 signs Content-Length; document that Size==0 leaves uploads unbounded.

#### 427. [MEDIUM] Any InvalidRequest with declared size misclassified as ErrInsufficientCapacity
`infra/storage/s3backend/s3.go:332` — error-handling — conf 0.65 — _reviewer-reported (unverified)_

translateS3Capacity maps every smithy error with code "InvalidRequest" to storage.ErrInsufficientCapacity whenever meta.Size > 0. S3 returns InvalidRequest (HTTP 400) for many non-capacity conditions: SSE-C over HTTP, missing/invalid headers, checksum/Object-Lock issues, accelerate-endpoint misuse. Callers commonly declare Size, so unrelated 400s become CodeStorageFull, surfacing HTTP 507 and routing operators to a capacity runbook for config bugs.

**Suggested fix:** Match the error message (e.g. contains "exceed"/"size") in addition to the code, or drop the InvalidRequest mapping and keep only EntityTooLarge.

#### 428. [LOW] Clean shutdown during Campaign logs WARN 'term ended with error: context canceled'
`infra/leaderelection/etcd/election.go:262` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

When the caller ctx cancels while Campaign blocks (the steady state of every standby replica), runOnce returns ctx.Err(); Run's loop then logs a warning 'term ended with error' carrying context.Canceled before breaking. The same happens when session creation fails due to the cancelled ctx. Every orderly shutdown of a non-leader replica produces a spurious warn-level log suggesting a fault.

**Suggested fix:** Skip the warn when errors.Is(termErr, context.Canceled/DeadlineExceeded) and ctx.Err() != nil; log at Debug instead.

#### 429. [LOW] TestRun_OnAcquiredPanic_IsCaptured asserts nothing about the panic capture
`infra/leaderelection/etcd/election_test.go:340` — testing — conf 0.80 — _reviewer-reported (unverified)_

The test's doc comment says 'the panic value is folded into the joined term error', but the test discards Run's return (_ = e.Run(ctx, cb)) and makes no assertion at all — it only proves the process did not crash. In fact, because etcd's Run loops and only returns ctx.Err() after cancellation, the OnAcquired panic error is logged and never surfaced from Run, so the documented claim is untestable as written (and differs from k8slease, which does surface it).

**Suggested fix:** Assert on observable evidence (captured log output or an injected metrics fake), and correct the comment — or surface per-term panic errors from Run as k8slease does.

#### 430. [LOW] Comment claims Close 'releases the lease without revoking it' — upstream Close revokes
`infra/leaderelection/etcd/session.go:57` — docs — conf 0.85 — _reviewer-reported (unverified)_

concurrency.Session.Close (etcd client v3.6.12) calls Orphan() then client.Revoke(ctx, s.id) — it does revoke the lease. Additionally the revoke context is derived from the session's opts.ctx, which is the elector's Run ctx (passed via concurrency.WithContext), so on shutdown the revoke parent is already cancelled and Close always fails (Debug-logged), falling back to TTL expiry. The comment inverts the actual behaviour, which matters when reasoning about how fast standby campaign keys disappear.

**Suggested fix:** Correct the comment: Close revokes the lease, and the revoke is best-effort once the Run ctx is cancelled (TTL is the fallback).

#### 431. [LOW] TestRun_DoesNotCallOnLostWithoutAcquired tests an inlined copy of the predicate, not production code
`infra/leaderelection/k8slease/lease_test.go:338` — testing — conf 0.85 — _reviewer-reported (unverified)_

The test declares its own local stopFn that re-implements the onAcquiredStarted gate and asserts on that copy; it never touches Elector.Run or the real OnStoppedLeading closure. It would keep passing if the production gate were deleted. The comment acknowledges this, deferring to integration tests, but as written the unit test pins nothing.

**Suggested fix:** Drive the real OnStoppedLeading path (extract the closure into a testable method, or use the fake clientset with a pre-cancelled ctx) instead of asserting on a test-local reimplementation.

#### 432. [LOW] Package doc's implementation list omits the etcd subpackage
`infra/leaderelection/leaderelection.go:5` — docs — conf 0.90 — _reviewer-reported (unverified)_

The leaderelection package doc enumerates implementations 'pgadvisory, redislock, k8slease' but infra/leaderelection/etcd exists and its doc.go cross-references the siblings. Consumers scanning the interface package's directory of backends will not discover the etcd adapter.

**Suggested fix:** Add infra/leaderelection/etcd to the bullet list with its one-line recommendation.

#### 433. [LOW] Drain timeout breaks documented OnLost-after-OnAcquired ordering, undocumented
`infra/leaderelection/pgadvisory/pgadvisory.go:242` — docs — conf 0.85 — _reviewer-reported (unverified)_

leaderelection.Callbacks documents OnLost runs "after OnAcquired returns". With WithCallbackDrainTimeout, runOnLost executes while the orphan OnAcquired goroutine is still running (holdLeadership returned timedOut). A consumer whose OnLost tears down resources the orphan still uses can race or panic. Neither WithCallbackDrainTimeout's doc nor the interface doc mentions this ordering exception. Same in redislock.go:247.

**Suggested fix:** Document in WithCallbackDrainTimeout (both packages) that OnLost may run concurrently with the orphaned OnAcquired after a drain timeout.

#### 434. [LOW] Stale 'round-3 removed the hard drain timeout' comment contradicts implemented drainTimeout
`infra/leaderelection/pgadvisory/pgadvisory.go:327` — docs — conf 0.90 — _reviewer-reported (unverified)_

holdLeadership's doc comment says "round-3 removed the hard drain timeout, so this is the only operator-visible signal that a buggy callback is pinning the elector" — but WithCallbackDrainTimeout reinstated a hard timeout and awaitCallbackDrain implements the deadline branch. Identical stale text in redislock.go:343-345. Misleads maintainers auditing the drain policy.

**Suggested fix:** Update both comments to describe the optional WithCallbackDrainTimeout deadline alongside the warn ticker.

#### 435. [LOW] Single transient Extend error abandons leadership despite 25s of remaining TTL budget
`infra/leaderelection/redislock/redislock.go:403` — api-design — conf 0.70 — _reviewer-reported (unverified)_

One transient renew error (a 3s Redis read timeout blip) immediately cancels OnAcquired and releases the lock, even though with default TTL 30s / renew 5s there are ~5 more renew opportunities before the lock actually expires. This causes avoidable leadership churn and failovers under brief network jitter. pgadvisory's single-strike is justified (session death = lock gone); redislock's is not.

**Suggested fix:** Retry failed renewals on the next tick while elapsed time since last successful Extend stays under a safety fraction of the TTL.

#### 436. [LOW] runWithStub helper misleading: claims to stub the locker but only calls holdLeadership with hidden 200ms deadline
`infra/leaderelection/redislock/redislock_test.go:58` — testing — conf 0.75 — _reviewer-reported (unverified)_

The comment says it replaces "the locker call by overriding the elector's Run via a test-only path"; it actually never touches the locker — stubAcquirer is a dead wrapper around the handle — and it silently imposes a 200ms parent deadline. On a heavily loaded CI runner the deadline can fire before the renew tick, changing which error path tests like TestHoldLeadership_RenewalFailureExits exercise (require.Error still passes, masking the wrong path).

**Suggested fix:** Drop stubAcquirer, call holdLeadership directly with an explicit generous deadline, and assert the specific loss error, not just any error.

#### 437. [LOW] Reconnect loop sleeps full base backoff (~3s) before the FIRST dial attempt
`infra/messaging/amqpbackend/connection.go:590` — performance — conf 0.85 — _reviewer-reported (unverified)_

reconnect() calls bo.Next() at the top of the loop before attempt 1, so with retry.WorkerPolicy (BaseDelay 3s) the first dial happens ~3s after the trigger. For WithLazyConnect this delays the initial connection ~3s even when the broker is up and reachable, and every drop incurs an extra 3s of downtime before the first reconnect try. Connection tests pay this too (15s timeouts).

**Suggested fix:** Attempt the first dial immediately and apply bo.Next() only between failed attempts.

#### 438. [LOW] Self-inflicted reconnect signal after onReconnect failure causes one redundant connection cycle
`infra/messaging/amqpbackend/connection.go:649` — correctness — conf 0.65 — _reviewer-reported (unverified)_

When onReconnect fails, the loop intentionally closes the just-dialed connection; the generation-current watcher sees the close and queues a reconnectSignal (CAS fails since the loop is running). After the loop later succeeds, the finalization drain consumes that stale signal, logs "reconnect signal received", resets backoff, dials again and closes the perfectly healthy connection it just established — doubling connection churn and consumer restarts after every onReconnect failure recovery.

**Suggested fix:** Drain reconnectSignal immediately after the intentional close in the onReconnect-failure branch, before continuing the loop.

#### 439. [LOW] sanitizeURL is production dead code
`infra/messaging/amqpbackend/connection.go:716` — consistency — conf 0.90 — _reviewer-reported (unverified)_

sanitizeURL is defined in connection.go but its only caller in the entire repo is TestSanitizeURL_DropsCredentialsQueryAndFragment. Production logging deliberately uses url_configured booleans instead. Keeping an unused credential-sanitizer invites future callers to assume it is wired into log paths when it is not.

**Suggested fix:** Delete sanitizeURL and its test, or actually use it where the dial URL would aid log diagnostics.

#### 440. [LOW] handlerTimeout hard-coded at 30s with no consumer option
`infra/messaging/amqpbackend/consumer.go:35` — api-design — conf 0.75 — _reviewer-reported (unverified)_

Every handler invocation is capped at the unexported 30s handlerTimeout (and DLE publishes at 10s) with no ConsumerOption to override. Handlers with legitimately long work (large batch, slow downstream) get ctx-cancelled at 30s and the message enters the retry/DLQ cycle; there is no way to tune this without forking, unlike prefetch which is configurable.

**Suggested fix:** Add additive WithHandlerTimeout(d time.Duration) ConsumerOption (panic on d<=0), defaulting to 30s.

#### 441. [LOW] dlqConsecutiveFail counter is shared across all bindings of one Consumer
`infra/messaging/amqpbackend/consumer.go:110` — api-design — conf 0.70 — _reviewer-reported (unverified)_

A single Consumer is typically used for multiple ConsumeOnce/Consume bindings, but dlqConsecutiveFail is one atomic per Consumer. Ten DLE failures on queue A's broken dead exchange push the counter past the cap, so queue B's very first (possibly transient) DLE publish failure is force-discarded — message loss — instead of nacked back into B's retry cycle. Conversely a success on B resets A's streak.

**Suggested fix:** Track consecutive DLE failures per dead-exchange (or per binding), e.g. a sync.Map keyed by DeadExchange.

#### 442. [LOW] AllowFromHeader leaks expected-token length via ConstantTimeCompare early exit
`infra/messaging/amqpbackend/debughttp/guard.go:125` — security — conf 0.70 — _reviewer-reported (unverified)_

subtle.ConstantTimeCompare returns immediately when lengths differ, so an attacker can probe the expected header value's length via timing. BasicAuth in the same file deliberately hashes both sides to fixed-size digests before comparing; AllowFromHeader does not, an inconsistency within one file. Impact is bounded because Guard requires environment==development, but the helper is positioned as a mesh-identity check.

**Suggested fix:** Hash both got and want with sha256 before ConstantTimeCompare, mirroring BasicAuth.

#### 443. [LOW] extractStringHeaders break on over-budget header drops remaining headers nondeterministically
`infra/messaging/amqpbackend/delivery.go:84` — correctness — conf 0.85 — _reviewer-reported (unverified)_

When one header's key+value exceeds the remaining byte budget the loop `break`s instead of skipping that header. Combined with random Go map iteration order, a single large header (e.g. a fat baggage value) causes an unpredictable subset of small legitimate headers (trace/correlation IDs) to be dropped — different subset per delivery. Intermittent tracing loss that is very hard to diagnose.

**Suggested fix:** Use continue (skip the oversized header) instead of break, or iterate keys in sorted order for determinism.

#### 444. [LOW] Shared mutable package-level budget0 makes deepCopy tests order-dependent and -count flaky
`infra/messaging/amqpbackend/delivery_test.go:94` — testing — conf 0.80 — _reviewer-reported (unverified)_

budget0 is a package-level var passed as *int to deepCopyValue, which decrements it. The comment claims it is a "fresh per-test node budget" but it is shared and never reset; each full run consumes ~5 units of the 256 budget, so `go test -count=60` (or future tests adding consumption) exhausts it and TestDeepCopyValue_Table starts returning the truncation sentinel and failing.

**Suggested fix:** Allocate a fresh local budget inside each test (b := maxHeaderNodes; deepCopyValue(v, 0, &b)).

#### 445. [LOW] Buffer-full back-pressure error has no sentinel; callers must string-match
`infra/messaging/buffered_publisher.go:468` — api-design — conf 0.80 — _reviewer-reported (unverified)_

Both drop paths return ad-hoc fmt.Errorf("buffered publisher: buffer full, message dropped") (lines 468 and 522). The package defines errors.Is-able sentinels for every other rejection (ErrMessageTooLarge, ErrInvalidRoute, ErrInvalidMessage, ErrInvalidPublisher), so the one error callers most need to react to programmatically (shed load / retry later) is the only one without a sentinel.

**Suggested fix:** Add var ErrBufferFull (e.g. apperror.NewUnavailable) and wrap it in both drop paths — additive change.

#### 446. [LOW] Stale doc comments: load() describes pre-wave-66 lossy behavior; constructor comment claims an Open* name
`infra/messaging/buffered_publisher.go:913` — docs — conf 0.90 — _reviewer-reported (unverified)_

load()'s comment says invalid entries 'are skipped and logged rather than rejecting the entire file', but the default is strict-fatal — skipping happens only with WithLossyStateValidation (lines 930-944). Separately, NewBufferedPublisher's comment (line 333) says 'The name uses Open* (not New*) because the constructor performs file I/O' while the function is named NewBufferedPublisher. Both mislead maintainers about actual behavior/naming.

**Suggested fix:** Rewrite load()'s comment to state strict-by-default, and fix or delete the Open*/New* sentence.

#### 447. [LOW] chmod-based failure-injection tests fail when run as root
`infra/messaging/buffered_publisher_test.go:1448` — testing — conf 0.65 — _reviewer-reported (unverified)_

TestBufferedPublisherDrain_SaveErrorFiresHookAndLastSaveError and TestPrometheusBufferedPublisherMetrics_StateWriteError (buffered_publisher_metrics_test.go:164) rely on os.Chmod(dir, 0o500) making the directory unwritable. Root (common in CI containers) bypasses permission checks, so atomicfile.Save succeeds, OnSaveError never fires, and both tests fail spuriously.

**Suggested fix:** Skip when os.Geteuid()==0, or inject the save failure via a fake/unwritable mount instead of chmod.

#### 448. [LOW] doc.go contradicts itself on Binding.Retry handling
`infra/messaging/kafkabackend/doc.go:86` — docs — conf 0.90 — _reviewer-reported (unverified)_

Lines 30-39 correctly state Binding.Retry is REJECTED with messaging.ErrRetryUnsupported at Consume entry (matching subscriber.go:303-309), but lines 83-89 of the same file claim 'The backend logs a warning when a Binding declares non-nil Retry' — stale pre-wave-141 text describing behavior that no longer exists.

**Suggested fix:** Update the 'Interface concessions' paragraph to say Retry is rejected with ErrRetryUnsupported, not warned about.

#### 449. [LOW] kafkatracing API diverges from amqptracing despite 'mirrors the amqptracing shape' claim
`infra/messaging/kafkabackend/kafkatracing/tracing.go:114` — consistency — conf 0.75 — _reviewer-reported (unverified)_

amqptracing.StartPublisherSpan(ctx, operation, exchange, routingKey) takes no headers and never injects; kafkatracing.StartPublisherSpan(ctx, headers, operation, topic, key) injects into headers (silently skipping when headers is nil — trace context dropped without error). Span names also differ: amqp uses 'operation publish'/'operation process', kafka uses 'operation topic'. Cross-backend callers cannot swap helpers mechanically.

**Suggested fix:** Document the nil-headers silent skip and the intentional signature divergence, or add a headers-injecting variant to amqptracing for parity.

#### 450. [LOW] NewPublisher doc claims it panics on bad config; it returns errors
`infra/messaging/kafkabackend/publisher.go:187` — docs — conf 0.85 — _reviewer-reported (unverified)_

Doc comment: 'Panics if brokers is empty or a SASL / TLS misconfiguration is detected — these are configuration errors that must surface at startup.' Actual behavior: NewPublisherWithConfig returns errors from Clone/ValidateConfig/buildTransport (tests assert require.Error). Callers reading the doc may skip error handling expecting a panic. Related: WithBatchTimeout's doc says 'Values <= 0 fall back to kafka-go's default' but negative values panic.

**Suggested fix:** Reword to 'Returns an error if…'; fix WithBatchTimeout doc to say only 0 falls back, negatives panic.

#### 451. [LOW] Publish doc inverts the durability guarantee ('non-nil return' should be 'nil return')
`infra/messaging/kafkabackend/publisher.go:276` — docs — conf 0.90 — _reviewer-reported (unverified)_

The Publish doc says 'A non-nil return therefore guarantees the message will not be lost to a broker crash.' The intended meaning is the opposite: a NIL return (successful, acked write) carries the durability guarantee. As written it asserts that failed publishes are durable.

**Suggested fix:** Change to 'A nil return therefore guarantees…'.

#### 452. [LOW] Unrecoverable fetch errors never terminate Consume or reach the caller
`infra/messaging/kafkabackend/subscriber.go:332` — error-handling — conf 0.60 — _reviewer-reported (unverified)_

Any non-context FetchMessage error (SASL auth failure, ACL denial, deleted topic) is logged at Warn, counted as fetch_error, then retried forever at 500ms intervals. Consume only ever returns nil (ctx cancel) or a pre-loop validation error, so a permanently broken consumer looks identical to a healthy idle one to the supervising code; operators must notice logs/metrics.

**Suggested fix:** Detect persistent/fatal fetch errors (e.g. N consecutive failures or kafka error classification) and return an error from Consume.

#### 453. [LOW] messagingtest doc claims conformance checks RunPublisher does not perform
`infra/messaging/messagingtest/doc.go:14` — docs — conf 0.85 — _reviewer-reported (unverified)_

doc.go says the suite asserts 'Publish on a nil receiver returns ErrInvalidPublisher' and 'Publish with empty exchange + routing key … the suite asserts only that the call doesn't panic'. RunPublisher (conformance.go:21-32) runs neither check — it covers nil ctx, cancelled ctx, sequential happy path, concurrency, and message-shape only. Backend authors relying on the documented surface get false assurance.

**Suggested fix:** Either add the two documented subtests to RunPublisher or delete those bullets from doc.go.

#### 454. [LOW] metrics.go const block is not gofmt-aligned (CI lint failure)
`infra/messaging/natsbackend/metrics.go:22` — build-ci — conf 0.97 — _verified_

The natsConsumeOutcome* const block (L17-25) is misaligned: gofmt -l flags metrics.go. natsConsumeOutcomeAcked/AckFailed/Retry/NakFailed/Permanent/TermFailed need an extra alignment space to match the longer DecodeError/ValidateError/HandlerPanic identifiers. make lint / gofmt -l runs in CI and would fail on this file.

**Suggested fix:** Run gofmt -w on metrics.go to fix const-block alignment.

#### 455. [LOW] Doc comments reference non-existent Connection.Close (method is Stop)
`infra/messaging/natsbackend/natsbackend.go:68` — docs — conf 0.90 — _low/info (unverified)_

Package and field docs reference [Connection.Close] (L68, L373, L460) but the only shutdown method is Stop (L463). Godoc cross-links are broken and the closeDrainTimeout/DrainTimeout rationale points at a method that does not exist, misleading readers about the shutdown API.

**Suggested fix:** Replace [Connection.Close] references with [Connection.Stop].

#### 456. [LOW] Publish skips metrics for context/route validation failures
`infra/messaging/natsbackend/natsbackend.go:666` — consistency — conf 0.70 — _low/info (unverified)_

The observePublish defer (L678-680) is registered only after ValidatePublishContext (L670) and ValidatePublishRoute (L673) return. A cancelled context or invalid route therefore records no nats_published_total / publish_duration sample, unlike size-limit or marshal failures which are counted. Operators lose visibility into a class of publish rejections.

**Suggested fix:** Set started/outcome and register the metrics defer before the context and route validation checks.

#### 457. [LOW] deliveryHeaderMaps stops at first over-budget header, non-deterministically dropping fitting headers
`infra/messaging/natsbackend/natsbackend.go:1034` — correctness — conf 0.55 — _low/info (unverified)_

When a header's cost exceeds the remaining byteBudget the loop breaks (L1034-1035), so every header iterated after a single large one is dropped. Because Go map iteration order is random, which small headers survive is non-deterministic across deliveries. This matches the AMQP sibling so it is intentional kit convention, but it means trace/correlation headers can vanish unpredictably when one large header is present.

**Suggested fix:** Consider continue instead of break so small headers still fit within the remaining budget (would need matching change in amqpbackend for consistency).

#### 458. [LOW] validatePayload discards the jsonschema failure cause entirely
`infra/messaging/schema.go:181` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

On validation failure the function returns bare fmt.Errorf("schema validation failed"), dropping which keyword/field failed. The sibling unmarshal path (line 172) uses redact.WrapError, which preserves the chain for errors.Is/As and logs while rendering redacted text — so the redaction goal does not require total discard. Operators debugging rejected messages get zero triage signal.

**Suggested fix:** Return redact.WrapError("schema validation failed", err) so the cause survives in the chain with safe rendering.

#### 459. [LOW] ComputeBindings silently discards NormalizeBindingSpecs warnings
`infra/messaging/topology.go:189` — consistency — conf 0.70 — _reviewer-reported (unverified)_

NormalizeBindingSpecs exists so 'operators see, in the consumer's startup log, that the kit picked the default' retry policy, but ComputeBindings does `_ = NormalizeBindingSpecs(specs)`. Consumer services using ComputeBindings (the documented no-broker path for obtaining Bindings) get DefaultRetryPolicy applied with zero operator-visible signal, defeating the warning mechanism's stated purpose.

**Suggested fix:** Return the warnings from ComputeBindings (additive second return is breaking — instead add ComputeBindingsWithWarnings, or accept a *slog.Logger option).

#### 460. [LOW] Backoff schedule comment overstates retries: lists 10 delays / ~17 minutes, actual is 9 delays / ~13.5 minutes
`infra/outbox/relay.go:26` — docs — conf 0.80 — _reviewer-reported (unverified)_

With maxAttempts=10, handlePublishError calls IncrementAttempts only for nextAttempt 1..9 (the 10th attempt goes straight to MarkFailed), so the delays are 2,4,8,16,32,64,128,256,300s = 810s ≈ 13.5min. The comment at relay.go:25-27 lists ten delays ending in two 300s entries and claims ~17 minutes, which will mislead operators tuning maxAttempts/staleDuration.

**Suggested fix:** Correct the comment to nine delays totaling ~13.5 minutes for the default configuration.

#### 461. [LOW] TestRelay_Stop orders Start vs Stop with a bare 20ms sleep — Stop-before-Start makes the test fail
`infra/outbox/relay_test.go:283` — testing — conf 0.60 — _reviewer-reported (unverified)_

If the Start goroutine has not run within 20ms (plausible on loaded CI under -race), Stop sets stopped=true first; Start then returns the "already stopped" error, and require.NoError(t, <-done) fails. Sibling tests use startSignalStore.waitForFetch for deterministic ordering; this test does not.

**Suggested fix:** Use newStartSignalStore()/waitForFetch (already defined in this file) instead of time.Sleep to guarantee Start ran.

#### 462. [LOW] TestRelay_LongPublishDoesNotDuplicate relies on real-time heartbeat cadence (100ms) inside a 200ms stale window — flaky under -race/loaded CI
`infra/outbox/relay_test.go:689` — testing — conf 0.50 — _reviewer-reported (unverified)_

staleDuration=200ms clamps the heartbeat to minHeartbeatInterval=100ms, so two consecutively delayed heartbeat ticks (a 200ms goroutine stall, common under -race on saturated CI) let the fake's ResetStaleProcessing reset the row mid-publish, producing the duplicate the test asserts against (pub.count()==1 fails). The margin between heartbeat cadence and stale window is only 2x wall-clock.

**Suggested fix:** Widen the margin (e.g., staleDuration 1s, publish delay 3s) or inject a fake clock so the test is not wall-clock sensitive.

#### 463. [LOW] Claimer.Heartbeat doc claims "the relay logs unexpectedly low counts" — the relay discards the count
`infra/outbox/store.go:30` — docs — conf 0.85 — _reviewer-reported (unverified)_

store.go:30-33 documents that the returned touched-row count is used by the relay to log unexpectedly low counts. In relay.go:489 the count is assigned to _ and only the error is checked. A zero count means the claim was lost (stale-reset/reclaimed); detecting it could cancel the in-flight publish and shrink the duplicate window, but today it is silently ignored.

**Suggested fix:** Either implement the documented check (cancel publishCtx when heartbeat touches 0 rows) or fix the interface comment.

#### 464. [LOW] ValidateRedis and Options() still disagree on AllowPlaintext fields path
`infra/redis/config.go:220` — consistency — conf 0.75 — _reviewer-reported (unverified)_

With AllowPlaintext=true and Host-only config (no password), Config.Options() succeeds but Fields.ValidateRedis fails with 'REDIS_PASSWORD is required' because the password check at line 220 ignores AllowPlaintext. The Options() comment claims wave 66 made the two paths agree; they disagree in the opposite direction, and the documented intent of the opt-out ('keeps local-dev fixtures working') only holds via REDIS_URL, not via REDIS_HOST.

**Suggested fix:** Skip the password requirement when AllowPlaintext is set, or document that fields-path always requires a password.

#### 465. [LOW] FailFastPolicy detection misses pointer form
`infra/redis/degradation.go:126` — api-design — conf 0.85 — _reviewer-reported (unverified)_

newFeatureHealthCheck uses `_, isFailFast := fc.Policy.(FailFastPolicy)` — a value-type assertion only. A caller passing &FailFastPolicy{} (compiles fine, satisfies DegradationPolicy via value-receiver methods) is silently treated as non-critical/degraded instead of unhealthy/critical, inverting the intended fail-fast health semantics.

**Suggested fix:** Also match *FailFastPolicy, or switch on Policy.Name() == "fail-fast".

#### 466. [LOW] Package doc claims connect options/password are cleared from memory — no code does this
`infra/redis/doc.go:19` — docs — conf 0.90 — _reviewer-reported (unverified)_

doc.go states 'the connection options (including any password) are cleared from memory after the client is created'. connectInternal/cloneOptions never zero or clear anything; the caller's *redis.Options and the clone retain the password indefinitely. A false security claim in canonical docs misleads adopters auditing secret hygiene.

**Suggested fix:** Delete the claim, or actually zero the cloned options' Password/CredentialsProvider after client construction.

#### 467. [LOW] TestConnection_Close_Idempotent never calls Close
`infra/redis/health_test.go:106` — testing — conf 0.95 — _reviewer-reported (unverified)_

The test named Close_Idempotent constructs a Connection, toggles the healthy flag under the mutex, and asserts Healthy() is false. Close() is never invoked, so the test verifies nothing about idempotency or close behavior (TestConnect_Close in connection_test.go covers it, making this test misleading dead weight).

**Suggested fix:** Delete it or rewrite to actually call Close() twice on a bare Connection.

#### 468. [LOW] metricsHook and pool-metric tests assert no metric output
`infra/redis/metrics_test.go:38` — testing — conf 0.85 — _reviewer-reported (unverified)_

TestMetricsHook_ProcessHook, TestMetricsHook_Pipeline, and the CollectPoolMetrics tests only assert that Redis commands succeed; none read the registry (testutil.ToFloat64/GatherAndCompare). The hooks could observe nothing, use wrong labels, or skip error counting entirely and all tests would still pass — the error-counting branch (commandErrors, redis.Nil exclusion) is fully unasserted.

**Suggested fix:** Gather from the test registry and assert command_duration/command_errors samples, including a redis.Nil case.

#### 469. [LOW] spawnRefresh spawns one goroutine per refresh-due Get
`infra/secrets/cache.go:214` — performance — conf 0.70 — _reviewer-reported (unverified)_

Every hit in the refresh-due window spawns a goroutine; they coalesce in singleflight but each blocks in wg.Wait until the leader finishes (up to the 10s refresh timeout). On the advertised hot path (every DB connection) a slow backend yields RPS x 10s parked goroutines per key, repeating each refresh cycle. A cheap in-flight flag would avoid the churn.

**Suggested fix:** Track a per-key refreshing flag (or check sf.m) and skip spawning when a refresh is already in flight.

#### 470. [LOW] Background refresh (stale-while-revalidate) path entirely untested
`infra/secrets/cache_test.go:52` — testing — conf 0.80 — _reviewer-reported (unverified)_

No test exercises spawnRefresh: nothing verifies a refresh-due hit returns the cached value and asynchronously updates the entry, that refresh errors keep serving the old value, or the refreshes/refreshErrors/staleFallbacks counters. The injectable cfg.now exists but no test uses it, so the package's core differentiating behavior ships unverified, and the cache_test would not have caught the zeroing bugs either.

**Suggested fix:** Add tests using an injected clock: refresh-due hit triggers update; failed refresh preserves entry; assert metrics.

#### 471. [LOW] Version comment says 'expose N' but code stores full resource name
`infra/secrets/gcpsm/gcpsm.go:88` — docs — conf 0.90 — _reviewer-reported (unverified)_

The comment claims the version number N is extracted from 'projects/P/secrets/S/versions/N', but the code assigns the entire resp.Name. The test pins the full-path behavior, so Secret.Version is inconsistent across backends: awssm returns a bare VersionId and vaultkv a bare integer, while gcpsm returns a full resource path.

**Suggested fix:** Either parse the trailing version segment as the comment says, or fix the comment and document the inconsistency.

#### 472. [LOW] validateDatabaseHost permits '=', space, and ':' for non-IP hosts
`infra/sqldb/config.go:285` — security — conf 0.50 — _low/info (unverified)_

The blocklist rejects ) / ' \ NUL @ CR LF but allows '=', whitespace, and ':'. A host like 'localhost sslmode=disable' or 'h=1 x=2' passes validation. Kit-internal consumers build the DSN via url.URL{Host: net.JoinHostPort(...)} which encodes safely, but a consumer hand-building a libpq keyword DSN from Config.Host could inject extra keywords.

**Suggested fix:** Switch to an allowlist for hostnames (RFC 1123 label chars + dots) instead of a character blocklist.

#### 473. [LOW] Doc claims pgx adapter satisfies Pinger, but signatures differ
`infra/sqldb/health.go:11` — docs — conf 0.82 — _low/info (unverified)_

The comment states 'The pgx adapter (infra/sqldb/pgx) satisfies this interface'. pgx.Pool.Ping is Ping(ctx context.Context) error (pgx.go:231) while Pinger requires Ping() error. *pgx.Pool does NOT satisfy Pinger; callers need a context-dropping shim, which also silently defeats the health timeout.

**Suggested fix:** Correct the doc, or ship a ctx-aware Pinger so pgx.Pool satisfies it directly.

#### 474. [LOW] lastSSLMode raw substring scan can be fooled by sslmode-like substrings
`infra/sqldb/pgx/pgx.go:562` — security — conf 0.55 — _low/info (unverified)_

lastSSLMode scans for 'sslmode=' anywhere in the DSN. A later DSN value containing the literal substring (e.g. ?sslmode=require&application_name=sslmode=verify-full) makes it return 'verify-full' (last-wins), so the require check is skipped and an unverified-TLS connection is accepted while pgx actually dials with sslmode=require. Plaintext is still independently blocked via the parsed config, so impact is require→accepted, not plaintext.

**Suggested fix:** Read sslmode from the structured parse (pcfg.ConnConfig.RuntimeParams / TLS posture) rather than re-scanning the raw DSN string.

#### 475. [LOW] TestExportPoolMetrics_ExportsStats asserts nothing meaningful
`infra/sqldb/pinger_metrics_test.go:117` — testing — conf 0.80 — _verified_

After running ExportPoolMetrics it only asserts GreaterOrEqual(open, 0) and GreaterOrEqual(idle, 0). Gauges default to 0 and are non-negative, so these assertions pass even if the export loop never set anything (e.g. if the tick/select were broken). The test gives false confidence the export works.

**Suggested fix:** Assert the open gauge equals the held-connection count (>=1 given SetMaxOpenConns(1) + Conn held), or use a synchronized fake DB whose Stats are known.

#### 476. [LOW] Package doc references nonexistent option and wrong Replicas type
`infra/sqldb/readreplica/doc.go:21` — docs — conf 0.75 — _low/info (unverified)_

Doc mentions [AcquireOption.WithReadAfterWrite] which does not exist (only WithReadOnly is defined). The quick-start also passes Replicas: []*pgx.Pool{...}, but Config.Replicas is []Acquirer and *pgx.Pool lacks an Acquire method and has Close() error (not Close()), so it does not satisfy Acquirer — the example will not compile.

**Suggested fix:** Drop the WithReadAfterWrite reference and update the example to wrap pools as Acquirer (e.g. via primary.Pool()) so it compiles.

#### 477. [LOW] VerifyChecksum compares hex digests case-sensitively
`infra/storage/checksum.go:87` — api-design — conf 0.60 — _reviewer-reported (unverified)_

got is lowercase from hex.EncodeToString, compared with `got != v.expected`. An expected digest stored or supplied in uppercase hex (common from external tools or other SDKs) always fails verification even when content is intact, surfacing as a spurious ErrValidation checksum mismatch.

**Suggested fix:** Normalize with strings.ToLower (or use hex.DecodeString + bytes.Equal) before comparing.

#### 478. [LOW] verifyReader returns (0, nil) forever after a mismatch detected with n==0
`infra/storage/checksum.go:100` — correctness — conf 0.85 — _reviewer-reported (unverified)_

In the EOF branch, when the checksum mismatches and n==0, the code sets v.done=true and returns (0, mismatchErr) but never assigns v.err. Every subsequent Read hits the `if v.done` fast path and returns (0, v.err) = (0, nil) — an io.Reader that makes no progress and never re-reports the error, which can spin callers that retry after errors (e.g. bufio until ErrNoProgress). The matching and n>0 paths correctly buffer the error.

**Suggested fix:** Set v.err = mismatchErr before the `return 0, mismatchErr` in the n==0 branch.

#### 479. [LOW] Dead test fixture: cb2 created, 'tripped', and never asserted
`infra/storage/circuitbreaker/breaker_test.go:339` — testing — conf 0.90 — _reviewer-reported (unverified)_

In TestAsLister_CircuitBreakerBlocksOpenCircuit, cb2 is built around presignedListerCBBackend, a Get is issued, then comments concede it cannot trip (membackend returns not-found) and the test abandons cb2 for cb3 without any assertion on cb2. The block is confusing dead code. Presign/URL blocked-by-open-circuit paths also remain untested anywhere in the package.

**Suggested fix:** Delete the cb2 block; add open-circuit tests for PresignGetURL/PresignPutURL/URL/Copy forwarding.

#### 480. [LOW] Get returns populated ObjectMeta alongside errors
`infra/storage/encryption/encryption.go:308` — api-design — conf 0.70 — _reviewer-reported (unverified)_

On backend-read, size-limit, NewGCM, and decrypt failures, Get returns the backend's meta together with a non-nil error (e.g. `return nil, meta, storage.WrapSafe(...)`). Sibling backends (azure, gcs, s3) return storage.ObjectMeta{} with errors. Callers that inspect meta without checking err first can act on metadata for content that failed authentication/decryption.

**Suggested fix:** Return storage.ObjectMeta{} on all error paths for consistency with sibling backends.

#### 481. [LOW] gcsbackend API surface diverges from sibling backends
`infra/storage/gcsbackend/config.go:58` — consistency — conf 0.75 — _reviewer-reported (unverified)_

gcsbackend.LoadConfig() takes no arguments while azurebackend/s3backend/sftpbackend all use LoadConfig(envPrefix, environment string); gcsbackend also lacks the Healthy() method that azurebackend and sftpbackend expose. Multi-backend wiring code cannot treat the providers uniformly.

**Suggested fix:** Add LoadConfig(envPrefix, environment) variant (additive; keep old signature or document divergence) and a Healthy() method mirroring azurebackend.

#### 482. [LOW] localFileError default branch drops the cause from the error chain
`infra/storage/localbackend/local.go:363` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

Non-sentinel causes (EIO, EDQUOT, arbitrary reader errors during io.Copy) become bare "localbackend: <op> failed" with no %w, so errors.Is/As cannot reach them. membackend wraps the same class of failure with storage.WrapSafe, which redacts the message but preserves the chain (its test asserts ErrorIs(err, readErr)). Same event, different observability across sibling backends.

**Suggested fix:** Use redact.WrapError (chain-preserving, message-redacting) in the default branch instead of discarding the cause.

#### 483. [LOW] Manager.Has and Names ignore the closed state
`infra/storage/manager.go:199` — consistency — conf 0.80 — _reviewer-reported (unverified)_

After Close, Backend returns ErrManagerClosed and Default/Register/SetDefault panic, but Has still returns true and Names still returns all names. Code gating on Has(name) before MustBackend(name) will pass the check and then panic post-shutdown, defeating the diagnosable-race intent documented on ErrManagerClosed.

**Suggested fix:** Make Has return false and Names return nil (or document the exemption) once closed.

#### 484. [LOW] MigrateCount aborts on first invalid listed key while Migrate records and continues
`infra/storage/migrate.go:206` — consistency — conf 0.60 — _reviewer-reported (unverified)_

Migrate treats an invalid listed key as a per-object failure (recordError + continue), but MigrateCount returns an error immediately on the same condition. A bucket containing one legacy key with a space makes the progress-bar count fail entirely while the actual migration would proceed, so the documented pairing (count then migrate) breaks.

**Suggested fix:** Skip-and-count (or count separately) invalid keys in MigrateCount to mirror Migrate's tolerance.

#### 485. [LOW] TestRetryStorage_RespectsContext asserts nothing about cancellation
`infra/storage/retry/retry_test.go:145` — testing — conf 0.90 — _reviewer-reported (unverified)_

The test pre-cancels ctx, injects an always-transient Delete error, and asserts only assert.Error(t, err). Because the injected error alone guarantees a non-nil result after retries, the test passes even if the retry loop completely ignores the cancelled context. It pins no attempt count and no error identity.

**Suggested fix:** Assert require.ErrorIs(err, context.Canceled) and that deleteFn was invoked zero times.

#### 486. [LOW] TestAsLister_RetryRetriesUnderlyingErrors tests presign, and no test iterates List through the wrapper
`infra/storage/retry/retry_test.go:291` — testing — conf 0.85 — _reviewer-reported (unverified)_

The test named for Lister exercises PresignGetURL retries only. Beyond the validation-failure probe, no test confirms a retry-wrapped Lister actually yields objects from a real backend (membackend implements Lister), so capability forwarding of List items is unverified.

**Suggested fix:** Rename to reflect presign, and add a test iterating objects via storage.AsLister(New(membackend.New())).

#### 487. [LOW] WithConfig after New silently diverges from the built AWS client
`infra/storage/s3backend/s3.go:81` — api-design — conf 0.70 — _reviewer-reported (unverified)_

In NewContext the S3 client is constructed from cfg before options run. WithConfig then replaces b.cfg without rebuilding the client or re-running Validate, so Endpoint/Region/credential changes affect URL() and SSE policy but not actual requests — a silent split-brain. It is documented as 'primarily useful in tests' but nothing prevents production use via New().

**Suggested fix:** Validate the new cfg inside WithConfig, and document explicitly that it never rebuilds the SDK client (or restrict bucket/SSE/URL fields).

#### 488. [LOW] defaultMetrics() registers global collectors even with custom registerer
`infra/storage/s3backend/s3.go:160` — api-design — conf 0.85 — _reviewer-reported (unverified)_

NewContext and NewWithClient assign metrics: defaultMetrics() in the struct literal before options run. sync.OnceValue then registers storage_s3_* collectors into prometheus.DefaultRegisterer as a side effect of every first construction — even when WithMetricsRegisterer(customReg) is passed, leaving dead zero-sample collectors in the global registry. If the default registry already holds an incompatible collector with that name, construction panics despite the custom registerer. Same pattern exists in gcs/azure/sftp backends (systemic).

**Suggested fix:** Leave b.metrics nil in the literal; after applying options, set b.metrics = defaultMetrics() only if still nil.

#### 489. [LOW] Get maps only *types.NoSuchKey; bodyless 404s miss ErrObjectNotFound
`infra/storage/s3backend/s3.go:361` — consistency — conf 0.45 — _reviewer-reported (unverified)_

Get checks errors.As(*types.NoSuchKey) while Delete/Exists use isS3NotFound (NoSuchKey or NotFound). S3-compatible endpoints or intermediary proxies returning 404 without a parseable XML body yield a GenericAPIError with code "NotFound" (UseStatusCode fallback in the SDK deserializer), which neither path matches — Get returns a generic error instead of storage.ErrObjectNotFound and the miss is counted as an op error in metrics.

**Suggested fix:** Extend isS3NotFound to also match smithy.APIError codes "NoSuchKey"/"NotFound", and use it in Get.

#### 490. [LOW] List error-path test passes vacuously if no error is yielded
`infra/storage/s3backend/s3_test.go:689` — testing — conf 0.85 — _reviewer-reported (unverified)_

In 'yields error on S3 failure', all assertions live inside the range body. If List were broken to yield nothing on backend failure (swallowed error), the loop body never executes and the test still passes. The sibling subtest 'rejects invalid options' correctly captures seenErr and asserts after the loop.

**Suggested fix:** Capture the yielded error into a variable and require.Error/ErrorIs after the loop, matching the invalid-options subtest pattern.

#### 491. [LOW] Context cancellation ignored during SFTP I/O in Put/Get
`infra/storage/sftpbackend/sftp.go:588` — correctness — conf 0.55 — _low/info (unverified)_

ctx is used only to start the trace span; the actual SFTP transfers (io.Copy in Put, streamed Get body, MkdirAll/Rename/Stat) never observe ctx cancellation. A cancelled/deadline-exceeded request continues streaming until the underlying connection times out. This is inherent to pkg/sftp but is undocumented and inconsistent with the Storage interface promise that 'all methods accept context for cancellation'.

**Suggested fix:** Document that mid-transfer cancellation is not honored, or wrap the reader/connection to abort on ctx.Done().

#### 492. [LOW] TOCTOU between Lstat symlink check and Open/Stat on hostile server
`infra/storage/sftpbackend/sftp.go:650` — security — conf 0.55 — _verified_

rejectSymlinkPath uses Lstat to reject symlinks, then Get/Exists/Delete call Open/Stat/Remove which follow symlinks. The stated threat model is a hostile/confused SFTP server (the whole symlink-rejection machinery exists for it). A server that swaps a regular file for a symlink between the Lstat and the follow-up op can still redirect reads/deletes outside root. The check is best-effort, not atomic.

**Suggested fix:** Document the TOCTOU limitation, or where the protocol allows, prefer O_NOFOLLOW-style opens / re-Lstat-and-compare-inode after open. Mark residual risk explicitly.

#### 493. [LOW] Symlink-rejection paths skip operation metrics in Get/Delete/Exists
`infra/storage/sftpbackend/sftp.go:704` — consistency — conf 0.60 — _low/info (unverified)_

When rejectSymlinkPath fails (or returns not-found), Get (650), Delete (704) and Exists (745) return before reaching observeOp, so these operations record neither duration nor error in storage_sftp_* metrics. The happy/remote-error paths do record metrics, producing inconsistent operation counts and hiding symlink-rejection events from dashboards.

**Suggested fix:** Record observeOp (with sftpMetricErr) on the symlink-rejection return paths to keep per-operation metric counts consistent.

#### 494. [LOW] Mock ReadDir sorts results, masking the unsorted-List pagination bug
`infra/storage/sftpbackend/sftp_test.go:173` — testing — conf 0.70 — _low/info (unverified)_

mockSFTPClient.ReadDir calls sort.Slice on results, so all List/pagination tests see lexicographically ordered output that real pkg/sftp does not provide. This is precisely why the unordered-List/StartAfter defect (see list.go:148) is invisible in CI. No test exercises StartAfter pagination or cross-directory ordering against an intentionally unsorted ReadDir.

**Suggested fix:** Add a List/StartAfter test whose mock returns ReadDir entries in non-sorted order to pin the ordering/pagination contract.

#### 495. [LOW] MaxKeys test uses LessOrEqual, passing even on zero results
`infra/storage/sftpbackend/sftp_test.go:645` — testing — conf 0.70 — _low/info (unverified)_

TestSFTPBackend_List/'respects MaxKeys' seeds 3 files and asserts assert.LessOrEqual(len(results), 2). This passes if the iterator returns 0 or 1 results, so it would not catch a regression that drops all results or stops early. The deterministic input warrants an exact-count assertion.

**Suggested fix:** Assert len(results) == 2 (exact) to detect under-yielding regressions.

#### 496. [LOW] ValidateKey admits Unicode format/bidi/zero-width characters
`infra/storage/storage.go:116` — security — conf 0.75 — _reviewer-reported (unverified)_

containsInvalidKeyRune relies on unicode.IsControl, which only returns true within Latin-1; Cf code points such as U+202E (RTL override), U+200B (zero-width space), and U+FEFF pass validation and IsSpace does not catch them. Keys containing these render misleadingly in logs/CLIs (the very risk the doc comment cites) and allow visually identical but distinct keys.

**Suggested fix:** Also reject runes where unicode.Is(unicode.Cf, r) (and optionally unsafe categories like Co/Cs).

#### 497. [LOW] Streaming io.Copy error after 200 sent is unrecoverable and only returned
`infra/storage/storagehttp/serve.go:144` — error-handling — conf 0.50 — _low/info (unverified)_

In the non-seekable fallback, io.Copy may fail after Go has already flushed a 200 status on the first Write; ServeFile returns that error but the client has received a truncated body with a success status. The doc comment acknowledges this, but callers commonly map the returned error to a 500, which cannot take effect. A truncated download is silently presented as success to the client.

**Suggested fix:** Document strongly that a returned error after streaming begins means a partial/corrupt response was already sent; advise callers to log+alert rather than attempt an HTTP error.

#### 498. [LOW] readResponse discards a complete verdict when clamd closes without NUL/newline
`infra/storage/storagehttp/uploadsec/clamav/clamav.go:339` — error-handling — conf 0.55 — _low/info (unverified)_

readResponse returns ('', err) whenever Read returns a non-nil error, even if a full unterminated verdict ('stream: OK') was already accumulated and the error is io.EOF from clamd closing the socket. A complete-but-unterminated response is treated as a scanner error (fails closed). clamd normally NUL-terminates, so impact is limited, but a verdict could be lost on a clean connection close.

**Suggested fix:** On io.EOF, if the builder already holds bytes, return the accumulated string instead of discarding it as an error.

#### 499. [LOW] AllowedMIMETypes drops the cause of sniff-read failures
`infra/storage/validate.go:189` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

On a read error during MIME sniffing it returns a bare fmt.Errorf with no %w/redact.WrapError, unlike sibling validators (ChecksumValidator uses redact.WrapError). The unwrap chain is lost, so callers cannot errors.Is for context.Canceled/DeadlineExceeded or classify transients — a cancelled upload surfaces as an opaque generic failure.

**Suggested fix:** Use redact.WrapError("storage: read for MIME detection", err) to keep the chain while staying redacted.

#### 500. [INFO] pgadvisory and redislock register byte-identical metric names; fragile cross-package coupling
`infra/leaderelection/redislock/metrics.go:70` — consistency — conf 0.70 — _reviewer-reported (unverified)_

Both packages register leaderelection_callback_drain_seconds and leaderelection_callback_drain_warn_total with identical labels/help/buckets. In a process using both backends on one registry, MustRegisterOrGet silently merges them — series are indistinguishable by backend, and the merge only works while Help/Buckets stay byte-identical; any future divergence makes the second NewMetrics panic at runtime.

**Suggested fix:** Add a Subsystem ("pgadvisory"/"redislock") or a constant backend label to each package's collectors (additive, new metric names need changelog note).

#### 501. [INFO] Nil OnAcquired causes perpetual leadership flapping
`infra/leaderelection/redislock/redislock.go:359` — api-design — conf 0.70 — _reviewer-reported (unverified)_

leaderelection.Callbacks documents both callbacks may be nil, and the interface suggests IsLeader() polling. With OnAcquired nil, the cbDone goroutine signals immediately, holdLeadership returns nil, and Run releases the lock and re-acquires every retryInterval — leadership churns across the fleet forever and IsLeader flickers, breaking the poll-IsLeader usage pattern. Same in pgadvisory.go:342.

**Suggested fix:** When OnAcquired is nil, hold the term until ctx cancel or renewal failure instead of relinquishing immediately (additive behavior change worth documenting).

#### 502. [INFO] Debug ConsumeHandler delivery omits Exchange/RoutingKey transport metadata
`infra/messaging/amqpbackend/debughttp/debug.go:84` — api-design — conf 0.60 — _reviewer-reported (unverified)_

ConsumeHandler constructs messaging.Delivery{Message: msg} with Exchange, RoutingKey, SchemaVersion, and Headers all zero. Real AMQP consumption populates these, so handlers that branch on d.RoutingKey/d.Exchange or read d.SchemaVersion behave differently under the debug endpoint than in production, undermining the endpoint's purpose of exercising the real handler path.

**Suggested fix:** Populate RoutingKey from req.Type, SchemaVersion from msg.SchemaVersion, and accept optional exchange in the request body.

#### 503. [INFO] Comment references non-existent option WithReconnectMetrics
`infra/messaging/amqpbackend/metrics.go:37` — docs — conf 0.95 — _reviewer-reported (unverified)_

The defaultBrokerLabel doc comment says to "pass an explicit broker name via WithReconnectMetrics", but no such option exists anywhere in the repo — the actual API is WithConnectionMetrics(m, broker) on the connection. Misdirects users hunting for the broker-label knob.

**Suggested fix:** s/WithReconnectMetrics/WithConnectionMetrics/ in the comment.

#### 504. [INFO] Redis Publish returns underlying error without redisbackend namespace prefix
`infra/messaging/redisbackend/publisher.go:105` — consistency — conf 0.60 — _low/info (unverified)_

Publish returns p.producer.Publish's error verbatim (L104-105). The underlying error is already redact-wrapped as 'xadd: ...' but lacks the 'redisbackend:' prefix every sibling error in this package uses, so logs cannot attribute the failure to this backend layer without inspecting the wrapped chain.

**Suggested fix:** Wrap with redact.WrapError("redisbackend: publish", err) for consistent attribution.

#### 505. [INFO] Negative-assertion typed-subscription tests rely on sleeps and unsynchronized bools
`infra/messaging/subscription_test.go:248` — testing — conf 0.60 — _reviewer-reported (unverified)_

TestTypedSubscription_DecodeFailureSurfaces and _ValidationFailsForInvalidPayload sleep 150ms, cancel, then assert a plain bool `called` is false while the Start goroutine may still be running — the bool is written without synchronization, so a regression (handler invoked) manifests as a data race rather than a clean failure, and neither test asserts the decode/validate error actually surfaced to the consumer. They pass vacuously if dispatch never happens at all.

**Suggested fix:** Use atomic.Bool, wait for Start to return before asserting, and additionally assert the fake consumer observed the decode/validate error.

#### 506. [INFO] Start after a completed Start/Stop cycle reports "already stopped" as "already started"
`infra/outbox/relay.go:320` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Start checks r.started before r.stopped, so a relay that ran and was stopped reports "outbox: Relay already started" on a restart attempt (pinned by TestRelay_StartRejectsRestartAfterStop asserting "already started"). The message points operators at the wrong condition — the relay was stopped, not concurrently running.

**Suggested fix:** Check stopped before started in Start so the error names the actual state.

#### 507. [INFO] Overlapping reconnect triggers are dropped, not deferred — re-subscription can be missed
`infra/redis/connection.go:519` — correctness — conf 0.60 — _reviewer-reported (unverified)_

fireOnReconnect skips the trigger entirely when a callback is in flight. If Redis flaps (healthy→unhealthy→healthy) during a long-running callback, the second transition's trigger is lost and never replayed after the first callback completes, so script re-registration/resubscription against the new connection state may silently not happen until the next unhealthy→healthy transition.

**Suggested fix:** Record a pending-trigger bit while reconnecting and re-fire once the in-flight callback finishes.

#### 508. [INFO] Singleflight couples all coalesced Gets to the leader's context
`infra/secrets/cache.go:152` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

fetchAndStore runs with the leader caller's ctx; coalesced waiters share its outcome. A leader with a near-expired deadline (or cancelled request) fails the fetch for every waiter, including ones with generous deadlines — they then either error or fall back to stale. Standard singleflight caveat, but worth detaching the fetch context (as spawnRefresh already does) for a secrets hot path.

**Suggested fix:** Run foreground fetches on a detached bounded context (context.WithoutCancel + timeout) like spawnRefresh.

#### 509. [INFO] primary_acquires_total help text overstates what it counts
`infra/sqldb/readreplica/metrics.go:23` — consistency — conf 0.70 — _low/info (unverified)_

Help says primary acquires include 'fallbacks', but Acquire() increments replicaFallback (not primaryAcquires) on the read-only fallback path (readreplica.go:206-208). The primary.Acquire that follows is not counted in primary_acquires, so the metric and its help disagree.

**Suggested fix:** Either also Inc primaryAcquires on the fallback path, or correct the help text to exclude fallbacks.

#### 510. [INFO] Custom metadata keys likely change case on Azure round-trip; conversion untested
`infra/storage/azurebackend/azure.go:465` — correctness — conf 0.50 — _reviewer-reported (unverified)_

Azure transmits custom metadata as x-ms-meta-* HTTP headers; the Go SDK returns keys with canonical header casing (e.g. Put Custom{"foo"} can come back as Custom{"Foo"}). fromAzureMetadata/toAzureMetadata preserve whatever the SDK yields, so key casing is not round-trip stable, unlike S3 (lowercased). Neither helper has any test, and no Azurite-based round-trip test exists.

**Suggested fix:** Normalize metadata keys (e.g. lowercase) in fromAzureMetadata to match s3backend, and add unit tests for both converters.

#### 511. [INFO] ChecksumValidator breaks Copy/CopyAcross/Migrate into validated backends
`infra/storage/checksum.go:25` — api-design — conf 0.60 — _reviewer-reported (unverified)_

ChecksumValidator hard-requires io.ReadSeeker, but genericCopy/copyObject feed the destination Put a non-seekable Get stream. Any backend configured with this validator therefore rejects every storage.Copy/CopyAcross/Migrate transfer with ErrValidation, an interop constraint not documented on the validator or the copy helpers.

**Suggested fix:** Document the limitation, or fall back to a spool-to-temp reader when the input is not seekable.

#### 512. [INFO] maxPresignTTL hard-coded at 1 hour with no override
`infra/storage/s3backend/presign.go:17` — api-design — conf 0.80 — _reviewer-reported (unverified)_

The 1-hour cap is a package constant; AWS itself allows up to 12h (STS) / 7 days (IAM). Consumers needing longer-lived URLs (e.g. emailed download links) have no escape hatch and must bypass the kit's presigner entirely, losing its key validation and SSE signing.

**Suggested fix:** Additive: add WithMaxPresignTTL(d) Option (clamped to 7 days) defaulting to 1h; keep current behavior unchanged.

#### 513. [INFO] Presign operations lack otel spans and metrics unlike all other ops
`infra/storage/s3backend/presign.go:20` — consistency — conf 0.75 — _reviewer-reported (unverified)_

Put/Get/Delete/Exists/List/Copy each start an otel span and record observeOp metrics; PresignGetURL/PresignPutURL record neither, so presign failures (e.g. credential resolution errors inside the presigner) are invisible in storage_s3_* metrics and traces.

**Suggested fix:** Wrap both presign methods with the same tracer span and observeOp("presign_get"/"presign_put") pattern.

#### 514. [INFO] validateGIFEnd accepts non-GIF version strings
`infra/storage/storagehttp/uploadsec/uploadsec.go:785` — correctness — conf 0.60 — _low/info (unverified)_

The signature check only compares body[:3]=='GIF' and ignores the version bytes (GIF87a/GIF89a), unlike the doc comment which references both versions. gif.Decode rejects bad versions earlier so there is no exploitable gap, but the validator's signature check is looser than documented.

**Suggested fix:** Compare against 'GIF87a'/'GIF89a' for consistency with the documented contract.


### app — application builder / module wiring

_23 findings — 7 medium, 10 low, 6 info_

#### 515. [MEDIUM] Loopback plaintext exemption is dead: construction passes but dial always rejects or silently upgrades
`app/amqp/amqp.go:260` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Module('amqp://localhost') skips the construction panic (isLoopbackHost) and the docs say local-dev loopback fixtures 'bypass the check'. But Init only passes amqpbackend.WithoutTLS when the app-level WithoutTLS option was given; backend normalizeDialURL (connection.go:330) then either errors ('amqp URL must use amqps or WithTLS') when no TLS is configured, or silently rewrites amqp:// to amqps:// when service TLS exists — failing the handshake against a plaintext local broker. With lazy connect the failure surfaces only at first use.

**Suggested fix:** Either thread the loopback exemption into the backend dial options, or change docs/tests to state WithoutTLS is required even for loopback.

#### 516. [MEDIUM] gRPC listen failure discards the entire root cause
`app/grpc/grpc.go:268` — error-handling — conf 0.65 — _reviewer-reported (unverified)_

serve() returns fmt.Errorf("gRPC listen failed") with no %w and no log of the underlying net.Listen error. TestModule_ServeListenErrorDoesNotReflectAddress pins this to avoid leaking the address, but the errno cause (EADDRINUSE vs EACCES vs invalid addr) is also lost and never logged, so a production port conflict surfaces as an undiagnosable one-liner from the lifecycle runner.

**Suggested fix:** Log the cause via m.logger with redact.Error(err) (kit already does this elsewhere) before returning the sanitized error, or wrap only the syscall errno.

#### 517. [MEDIUM] jwt module Init and issuer/audience option mapping completely untested
`app/jwt/jwt.go:167` — testing — conf 0.75 — _reviewer-reported (unverified)_

jwt_test.go only covers construction-time panics and nil accessors. Init's security-critical mapping (allowAnyIssuer -> WithAllowAnyIssuer, expectedIssuer -> WithExpectedIssuer, audience -> WithExpectedAudience/WithAllowAnyAudience), the httpclient lookup, provider construction, and metrics registration have zero coverage. app/production_defaults_test.go:232 confirms the old Builder-level audience-pinning test was removed in favor of this package, but no replacement Init test exists.

**Suggested fix:** Add Init tests via app.TestModuleContext with a stub HTTPClientProvider asserting the constructed jwtutil options for all four policy combinations.

#### 518. [MEDIUM] StartupTimeout (FR-013) is cooperative only; hanging module Init still blocks startup forever
`app/module.go:506` — correctness — conf 0.75 — _reviewer-reported (unverified)_

initOneModule calls m.Init(ctx, mc) synchronously with no watchdog. The FR-013 comments (builder.go:440, StartupTimeout doc) claim a hung module 'cannot block startup forever', but the deadline only works if the module threads ctx into its blocking calls. The kit's own app/amqp module discards ctx in Init. A module hanging in non-ctx-aware code (DNS, vendor SDK) blocks RunContext indefinitely. No test exercises StartupTimeout against a hanging Init.

**Suggested fix:** Run Init in a goroutine and select on ctx.Done (accepting the orphaned goroutine), or document the cooperative requirement and soften FR-013 claims.

#### 519. [MEDIUM] Data race on m.pool between health-check closure and Stop during shutdown
`app/postgres/postgres.go:155` — concurrency — conf 0.60 — _reviewer-reported (unverified)_

HealthChecks' Check closure reads the m.pool field at invocation time; Stop (line 176) writes m.pool = nil during module cleanup. health.runCheck abandons timed-out check goroutines (observability/health/health.go:495), and the internal server's 5s Shutdown can give up with handlers still running, so an abandoned/lingering check can read m.pool concurrently with the cleanup write — an unsynchronized race the race detector will flag. Impact is benign (Pool.Ping handles nil receiver) but it is a real memory-model violation.

**Suggested fix:** Capture the pool in a local variable when building the DependencyCheck (like app/redis does), or guard the field with atomic.Pointer.

#### 520. [MEDIUM] Main --health hardcodes port 9090, ignoring INTERNAL_PORT
`app/serviceboot.go:29` — correctness — conf 0.85 — _reviewer-reported (unverified)_

app.Main's --health branch calls health.RunHealthCheck(9090) unconditionally, but the internal ops port is configurable via INTERNAL_PORT (config.go:79, documented in docs/ai/bootstrap.md). A service that overrides INTERNAL_PORT and uses the documented Docker HEALTHCHECK --health flag probes the wrong port and is permanently reported unhealthy. RunHealthCheckOptions exists but Main never reads the env var.

**Suggested fix:** Parse INTERNAL_PORT (default 9090) in Main before invoking RunHealthCheck, mirroring LoadBaseConfig.

#### 521. [MEDIUM] Validator error messages direct operators to nonexistent APIs
`app/validate.go:126` — docs — conf 0.90 — _reviewer-reported (unverified)_

The TLS error says 'call http.AllowPlaintext() / WithoutTLS' and the loopback error says 'call http.AllowInternalNonLoopback() / AllowInternalNonLoopback' (line 149). The actual app/http options are WithoutTLS() and WithInternalNonLoopback() (app/http/http.go:76,99); there is no AllowPlaintext or AllowInternalNonLoopback function, and Builder has no WithoutTLS/AllowInternalNonLoopback methods since wave 94. Operators following the remediation text get compile errors.

**Suggested fix:** Update both fmt.Errorf strings to name http.WithoutTLS() and http.WithInternalNonLoopback().

#### 522. [LOW] Package doc names the wrong import path for this module
`app/auditlog/auditlog.go:19` — docs — conf 0.90 — _reviewer-reported (unverified)_

The sibling-package list says '[github.com/bds421/rho-kit/app/v2/auditlog] — THIS package', but go.mod declares the module as github.com/bds421/rho-kit/app/auditlog/v2 (lockstep /v2 suffix convention). The godoc link is broken and the stated path is unimportable; per AGENTS conventions every other reference in the file uses the correct app/auditlog/v2 form.

**Suggested fix:** Change the doc link to github.com/bds421/rho-kit/app/auditlog/v2.

#### 523. [LOW] Nine files in this unit are not gofmt-clean; CI does not gate formatting
`app/builder.go:4` — build-ci — conf 0.95 — _reviewer-reported (unverified)_

gofmt -l flags app/builder.go (import order crypto/tls before context), app/builder_helpers.go (trailing blank line), app/module.go (doc-comment indentation), app/module_test_helpers_test.go, app/http_test_stub_test.go, app/actionlog/actionlog.go, app/approval/approval.go, app/auditlog/auditlog.go, app/amqp/amqp_test.go. `make ci` runs golangci-lint with no .golangci config, so no formatter check runs and unformatted code lands on main.

**Suggested fix:** Run gofmt -s -w on the listed files and add a check-fmt gate (gofmt -l must be empty) to `make ci`.

#### 524. [LOW] validateDependencyCheck discards both the `where` parameter and the validation error
`app/builder_helpers.go:79` — error-handling — conf 0.90 — _reviewer-reported (unverified)_

The function takes `where` (e.g. "AddHealthCheck" vs "Infrastructure.AddHealthCheck") and receives a descriptive error from health.ValidateDependencyCheck, but panics with the fixed string "app: health check invalid" — losing which call site and which rule failed. Module-collected checks (builder.go:471) wrap the error with %w, so diagnostics are inconsistent between the two paths.

**Suggested fix:** Panic with fmt.Sprintf("app: %s: invalid health check: %v", where, err) — err is kit-controlled validator output, not secret-bearing.

#### 525. [LOW] lookupElector returns nil elector without guard, panicking inside Init
`app/cron/cron.go:108` — correctness — conf 0.50 — _reviewer-reported (unverified)_

If a third-party module named "leader-election" implements app.ElectorProvider but Elector() returns nil, lookupElector returns (nil, true) and the `leader.IsLeader` method-value expression at line 78 panics on the nil interface. initOneModule's recover converts this to an opaque "module init failed" error rather than a clear diagnostic. app/leader guarantees non-nil, so only foreign providers trigger it.

**Suggested fix:** Guard `if e := ep.Elector(); e != nil { return e, true }` and return false otherwise.

#### 526. [LOW] OpenFeature provider is never shut down
`app/flags/flags.go:85` — resource-leak — conf 0.60 — _reviewer-reported (unverified)_

Init installs the provider via openfeature.SetNamedProviderAndWait (flags/flags.go:91) but Stop is a no-op and nothing calls openfeature.Shutdown or the provider's shutdown hook. Providers with background goroutines and buffered analytics events (LaunchDarkly, flagd streaming) neither drain nor flush during the kit's otherwise-careful graceful shutdown — inconsistent with paseto, which wires provider.Close into the Runner.

**Suggested fix:** Shut the named provider down in Stop (or via Runner.AddFunc) and document the lifecycle ownership.

#### 527. [LOW] mc.Module("httpclient") panics when absent, making the 'not registered' error branch unreachable
`app/jwt/jwt.go:168` — error-handling — conf 0.85 — _reviewer-reported (unverified)_

ModuleContext.Module panics if the name is missing (app/module.go:359), so the `if !ok { return errors.New("...httpclient module not registered or unexpected type") }` branch can only fire on a wrong type, never on absence. Production always has the builtin httpclient, but anyone driving jwtModule.Init via app.TestModuleContext gets a panic instead of the intended actionable error; sibling app/budget correctly uses LookupModule.

**Suggested fix:** Use mc.LookupModule("httpclient") and nil-check, so the documented graceful error path is actually reachable.

#### 528. [LOW] SLO checker hardwired to prometheus.DefaultGatherer with no override
`app/slo/slo.go:57` — api-design — conf 0.70 — _reviewer-reported (unverified)_

Init always builds the checker from prometheus.DefaultGatherer. Sibling bridges (app/jwt.WithRegisterer, app/postgres.WithRegisterer) support non-default registries; a service that routes its HTTP/SLI metrics to a custom registry gets SLO checks silently evaluated against a gatherer that lacks the series, so breaches can never be detected and /slo reports no-data without error.

**Suggested fix:** Add a WithGatherer(prometheus.Gatherer) option mirroring the sibling WithRegisterer pattern (additive, no API break).

#### 529. [LOW] Module-owned storage.Manager is never Closed; backend lifecycle ownership undocumented
`app/storage/storage.go:147` — api-design — conf 0.65 — _reviewer-reported (unverified)_

Init constructs the Manager and registers caller backends, but Stop is a no-op and Manager.Close (which closes io.Closer backends in reverse order, infra/storage/manager.go:215) is never invoked. Inconsistent with siblings: paseto closes its caller-constructed Provider on shutdown, nats/redis/postgres close their handles. Buffered/pooled backends (SFTP, encryption decorators) skip their close path on graceful shutdown, and neither the package doc nor Stop documents that callers retain close responsibility.

**Suggested fix:** Call m.manager.Close() in Stop, or explicitly document that backend lifecycle stays with the caller.

#### 530. [LOW] TestModule_ClonesOptionSlices cannot fail if the clone is removed
`app/storage/storage_test.go:58` — testing — conf 0.85 — _reviewer-reported (unverified)_

The test claims to pin the wave-99 defensive-clone fix but only asserts HealthChecks() returns one entry with the expected name; it never mutates a caller-held slice after Module() returns. Deleting the clone at storage.go:116-117 leaves this test green, so the regression it claims to pin is unprotected.

**Suggested fix:** Keep a reference to a shared slice, mutate it after Module() returns, and assert the module's state is unchanged — or rename/drop the test.

#### 531. [LOW] Stale godoc links to Builder methods removed in the wave-94 migration
`app/validate.go:69` — docs — conf 0.85 — _reviewer-reported (unverified)_

Validate's doc comment links [Builder.WithoutTLS], [Builder.AllowInternalNonLoopback], [Builder.WithoutJWTIssuer], [Builder.WithoutJWTAudience]; isLoopbackHost's comment (line 14) and config.go:36 also reference [Builder.AllowInternalNonLoopback]. None of these methods exist on Builder anymore — the opt-outs moved to app/http options and app/jwt construction. All four godoc links are dangling, and the guidance points readers at an API surface that was deleted.

**Suggested fix:** Rewrite the doc comments to reference http.WithoutTLS, http.WithInternalNonLoopback, and the app/jwt construction-time checks.

#### 532. [INFO] Interface nil-checks across bridge constructors miss typed-nil values
`app/authz/authz.go:43` — consistency — conf 0.70 — _reviewer-reported (unverified)_

Module(decider) guards `decider == nil`, which passes a typed-nil like (*MyDecider)(nil); the broken decider is then published and first fails at request time deep in middleware. Same pattern in budget.Module, storage.Module/WithNamed, leader.Module. Construction-time fail-fast is the kit's stated convention, but it only covers untyped nil.

**Suggested fix:** Optionally reject typed-nil via reflect.ValueOf(decider).IsNil() for pointer/func kinds, or document the limitation once.

#### 533. [INFO] append() aliasing hazard when resolving HTTP config
`app/builder.go:326` — correctness — conf 0.85 — _reviewer-reported (unverified)_

resolveHTTPConfig(append(allModules, deferredUserModules...)) appends into allModules' backing array — capacity is exactly len(builtin)+len(b.modules), so no reallocation occurs and deferred modules are written in place; line 327 then appends the identical elements, so the code is correct today only because both appends write the same values. Any refactor that changes the capacity computation or appends different content turns this into silent slice corruption.

**Suggested fix:** Build the final allModules slice first (move line 327 up), then call resolveHTTPConfig(allModules).

#### 534. [INFO] Dead import-keeper vars in cron tests
`app/cron/cron_test.go:89` — testing — conf 0.90 — _reviewer-reported (unverified)_

`var _ = lifecycle.NewRunner` and `var _ = slog.Default` exist solely to keep otherwise-unused imports compiling (comment admits it). The lifecycle and slog imports serve no test purpose; this is dead code that obscures real usage and contradicts the repo's otherwise clean test hygiene.

**Suggested fix:** Delete the two vars and the unused imports.

#### 535. [INFO] Internal migration jargon ('waves 88-98') in published godoc
`app/eventbus/eventbus.go:19` — docs — conf 0.85 — _reviewer-reported (unverified)_

The package doc ends with 'consistent with waves 88-98', an internal project-planning reference meaningless to external consumers of the published v2 module. Other packages reference audit IDs (FR-xxx) which at least appear consistently, but wave numbers have no anchor anywhere in the published docs.

**Suggested fix:** Replace with a reference to the lazy-adapter convention (e.g. 'matching the other app/* bridge modules').

#### 536. [INFO] Multiple HTTPConfigProvider modules: first silently wins, no conflict detection
`app/http_resolve.go:42` — api-design — conf 0.70 — _reviewer-reported (unverified)_

resolveHTTPConfig returns the first module implementing HTTPConfigProvider and ignores any later one without error or log. Duplicate app/http.Module registrations are caught by the module-name panic, but a custom module that also implements HTTPConfigProvider alongside app/http.Module is silently ignored (or silently overrides, depending on registration order), and Validate/Run may even consult different providers if a TracingProvider implements it (Run reorders allModules; serverTLSOptions scans b.modules).

**Suggested fix:** Panic or log when more than one registered module implements HTTPConfigProvider.

#### 537. [INFO] Kit-wide outbound HTTP client timeout hardcoded to 10s with no override
`app/httpclient_module.go:82` — api-design — conf 0.60 — _reviewer-reported (unverified)_

The always-on httpclient module builds the shared outbound *http.Client with a fixed 10-second timeout (both tracing and plain paths). There is no Builder or app/http option to tune it, so any service doing long-running outbound calls (large uploads, slow third-party APIs, SSE) through the kit client (also consumed by app/jwt JWKS fetches and other bridges via HTTPClientProvider) gets mid-flight timeouts and must hand-roll its own client, losing the shared TLS-reload wiring.

**Suggested fix:** Add an additive option (e.g. http.WithOutboundClientTimeout) threaded into newHTTPClientModule.


### runtime/io/resilience — eventbus, lifecycle, saga, cron, retry, circuitbreaker, atomicfile, progress, centrifuge

_31 findings — 1 high, 11 medium, 15 low, 4 info_

#### 538. [HIGH] Node metrics are never wired: WithMetricsRegisterer is a silent no-op and n.metrics is always nil
`realtime/centrifuge/node.go:74` — api-design — conf 0.95 — _verified_

config has a `metrics` field but no exported option sets it; there is no WithMetrics. NewNode copies c.metrics (always nil) and never calls NewMetrics with c.registerer. WithMetricsRegisterer only sets c.registerer, which is read nowhere. Result: every observe* call early-returns on a nil receiver, so connects/disconnects/subscribes/publishes metrics are NEVER emitted. Sibling infra/redis.WithMetricsRegisterer actually builds metrics (c.metrics = NewMetrics(WithRegisterer(reg))).

**Suggested fix:** In NewNode build metrics from c.registerer (n.metrics = NewMetrics(WithRegisterer(c.registerer))) or add a WithMetrics(*Metrics) option and document the wiring; mirror infra/redis.

#### 539. [MEDIUM] TestNewMetrics_RegistersCollectors asserts nothing
`realtime/centrifuge/node_test.go:188` — testing — conf 0.92 — _verified_

The loop over `expected` only does `_ = names[n] // touch to silence unused`; it never asserts that the expected metric families were registered/gathered. The test passes regardless of whether collectors exist, which is exactly why the dead-metrics wiring bug above went unnoticed.

**Suggested fix:** Assert require.True(t, names[n]) for each expected family, after force-observing one labelset on each vec so Gather emits them (as the circuitbreaker metrics test does).

#### 540. [MEDIUM] ExecuteCtx doc contradicts the actual default success predicate for context.Canceled
`resilience/circuitbreaker/circuitbreaker.go:291` — docs — conf 0.90 — _verified_

The doc says 'by default ctx.Canceled and ctx.DeadlineExceeded count as failures.' The real default (defaultIsSuccessful, line 60-65, wired at line 229) treats context.Canceled as SUCCESS (returns true) and only DeadlineExceeded as failure. Operators relying on this doc will mis-tune retries/alerting around cancellation-driven trips.

**Suggested fix:** Correct the doc to state context.Canceled is treated as success by default while DeadlineExceeded counts as a failure (matching defaultIsSuccessful).

#### 541. [MEDIUM] ExecuteCtx panics on nil context instead of returning an error (inconsistent with bulkhead)
`resilience/circuitbreaker/circuitbreaker.go:299` — error-handling — conf 0.85 — _verified_

ExecuteCtx calls ctx.Err() with no nil guard, so ExecuteCtx(nil, fn) is a nil-pointer dereference panic. The sibling bulkhead.ExecuteCtx explicitly returns 'requires a non-nil context'. There is no nil-ctx test here, so the footgun is untested and inconsistent across the resilience suite.

**Suggested fix:** Add `if ctx == nil { return errors.New("circuitbreaker: ExecuteCtx requires a non-nil context") }` before ctx.Err(), and add a contract test mirroring bulkhead.

#### 542. [MEDIUM] callOutcome hardcodes the default predicate; diverges from actual breaker accounting under custom IsSuccessful
`resilience/circuitbreaker/metrics.go:94` — correctness — conf 0.85 — _verified_

callOutcome buckets err==nil and context.Canceled as success and everything else as fail. But the breaker's pass/fail is decided by its IsSuccessful predicate (WithPermanentSuccess/WithIsSuccessful). With WithPermanentSuccess, apperror.Permanent errors are counted as success by the breaker yet labeled 'fail' here; a custom predicate treating Canceled as failure is still labeled 'success'. The doc claims the metric 'matches breaker accounting' — false for any non-default predicate.

**Suggested fix:** Thread the configured IsSuccessful into callOutcome (or capture the predicate on CircuitBreaker) so success/fail labeling matches what the breaker actually counts.

#### 543. [MEDIUM] Loop accepts WithMaxElapsedTime but never enforces it
`resilience/retry/retry.go:283` — api-design — conf 0.92 — _verified_

Loop builds Policy from opts (including WithMaxElapsedTime) and validates it, but the Loop body never checks p.MaxElapsedTime — only doWithPolicy does (line 483). A caller writing Loop(ctx, ..., WithMaxElapsedTime(5*time.Minute)) gets no abort; the loop runs until ctx cancel. The option's doc 'aborts the retry loop' is silently false for Loop. No current Loop caller passes it, so impact is latent.

**Suggested fix:** Enforce MaxElapsedTime in Loop (track loopStart, break when exceeded) or document/panic that Loop ignores it.

#### 544. [MEDIUM] Budget.Used() returns remaining time (opposite of its name) and is untested
`resilience/timeoutbudget/budget.go:83` — api-design — conf 0.70 — _verified_

Used() returns deadline.Sub(now) — the time REMAINING (ignoring reservation), not time consumed. doc.go advertises Used() so observability can 'record how the budget was spent', so an operator logging Used() reads remaining as if it were elapsed. The method has zero callers and zero test coverage in this v2 public API.

**Suggested fix:** Rename to Remaining-style or change body to total-elapsed (now - start); add a test. Keep old name as deprecated alias if needed to stay additive.

#### 545. [MEDIUM] WithReservation restore clobbers concurrent reservations despite 'safe for concurrent use'
`resilience/timeoutbudget/budget.go:112` — concurrency — conf 0.80 — _verified_

restore captures prev and does b.reservation = prev (absolute overwrite), not a subtraction. Budget is documented 'Safe for concurrent use' and the package's purpose is concurrent fan-out. Two goroutines reserving concurrently both read prev=0; the first restore wipes the other's still-active reservation. Only strict LIFO (as the tests use) works; concurrent/overlapping use corrupts the held-back time.

**Suggested fix:** Make restore subtract its own d (b.reservation -= d) under the mutex, idempotency-guarded, instead of restoring an absolute snapshot.

#### 546. [MEDIUM] Start leaks the robfig cron goroutine when parent ctx is cancelled without Stop
`runtime/cron/cron.go:210` — resource-leak — conf 0.78 — _verified_

Start calls s.cron.Start() (which spawns robfig's run() goroutine + timers) then returns once startedCtx.Done() fires. s.cron.Stop() is only ever called from Scheduler.Stop(). When a caller cancels the parent ctx directly (a documented Start termination: 'blocks until ctx is cancelled') without calling Stop, run() keeps looping forever — wrapJob skips ticks but the goroutine and timers never exit.

**Suggested fix:** In Start, after <-startedCtx.Done() returns, call s.cron.Stop() to drain the underlying scheduler goroutine.

#### 547. [MEDIUM] TestRunner_StopTimeout does not exercise the timeout it names
`runtime/lifecycle/runner_test.go:189` — testing — conf 0.85 — _verified_

newTestComponent's Stop returns immediately, so the 100ms stopTimeout, perStep clamps, and salvage path are never triggered. The test only asserts require.NoError after a clean cancel-driven shutdown — functionally a duplicate of TestRunner_CleanShutdown. The actual budget/timeout/salvage logic (the part most likely to regress) has no coverage.

**Suggested fix:** Add a component whose Stop blocks past the budget and assert it observes ctx.Done() / the shutdown completes within the expected bound.

#### 548. [MEDIUM] Resume's bounded concurrency does not actually isolate stuck sagas beyond resumeConcurrency
`runtime/saga/executor.go:217` — concurrency — conf 0.62 — _verified_

Resume acquires sem before spawning each goroutine (line 217). The docstring/const comment (lines 183-197) claim concurrency 'isolates a stuck saga to its own slot' so one stuck saga cannot stall the process. But with >= resumeConcurrency (8) sagas whose Forward ignores ctx and blocks forever, all slots fill, the main loop blocks on `sem <- struct{}{}`, goroutines never return (`<-sem` never fires), and wg.Wait()/Resume never return — a goroutine leak and full stall. There is no per-instance timeout and ctx cancellation is not enforced by the executor between steps for an in-step block.

**Suggested fix:** Bound each instance with a per-instance context timeout, or document that resumeConcurrency stuck sagas can still stall Resume. The 'isolation' claim should be qualified.

#### 549. [MEDIUM] rollBack passes live (cancellable) ctx to Compensate despite 'detached context' comment
`runtime/saga/saga.go:204` — correctness — conf 0.85 — _verified_

Lines 199-203 claim rollback uses 'a fresh detached context ... so a cancelled parent doesn't abort the rollback midway', but line 204 calls step.Compensate(ctx, state) with the original ctx. Run's own ctx-cancellation path (line 178) triggers rollBack, so an already-cancelled ctx is handed to every Compensate; any ctx-respecting compensation aborts immediately, contradicting the documented best-effort rollback. The cancellation test only uses ctx-ignoring Compensate funcs, so the gap is untested.

**Suggested fix:** Either pass context.WithoutCancel(ctx) to Compensate to honor the documented contract, or fix the comment to state cancellation aborts rollback. Add a ctx-respecting Compensate test.

#### 550. [LOW] Doc references nonexistent symbol LoadBounded
`io/atomicfile/atomicfile.go:18` — docs — conf 0.95 — _low/info (unverified)_

The MaxLoadBytes doc comment says 'Override via [LoadBounded] when the caller knows a different bound', but no LoadBounded function exists anywhere in the repo (grep confirms only this comment). The doc link is broken and the implied override capability does not exist, so callers cannot actually change the 16 MiB cap.

**Suggested fix:** Either add a LoadBounded[T any](path string, maxBytes int64) function, or remove the dangling reference from the comment.

#### 551. [LOW] Quick-start references node.SockJSHandler which does not exist
`realtime/centrifuge/doc.go:46` — docs — conf 0.85 — _low/info (unverified)_

The package example calls node.SockJSHandler("/connection/sockjs"), but Node only defines WebsocketHandler(). Copy-pasting the documented quick start does not compile.

**Suggested fix:** Remove the SockJSHandler line or implement a SockJSHandler method; keep the doc in sync with the actual Node surface.

#### 552. [LOW] Stop does not unblock Start; Start only returns when the Start ctx is cancelled
`realtime/centrifuge/node.go:117` — api-design — conf 0.60 — _low/info (unverified)_

Start blocks on <-ctx.Done(); Stop shuts down the centrifuge node with a separate ctx but never cancels the Start ctx. TestNode_StartStop must call cancel() after Stop to get Start to return. A caller that stops the node without cancelling the original Start ctx leaks the Start goroutine. It works under lifecycle.Runner only because the run ctx is cancelled on shutdown.

**Suggested fix:** Either document that Start's ctx must be cancelled to return, or have Stop cancel an internally-derived Start context so Stop alone unblocks Start.

#### 553. [LOW] OnDisconnect always records reason=clean; disconnectReasonStale is dead and help text is misleading
`realtime/centrifuge/node.go:197` — correctness — conf 0.80 — _low/info (unverified)_

OnDisconnect ignores e.Disconnect (which carries the real code/reason) and unconditionally records disconnectReasonClean. disconnectReasonStale is never used. The disconnects_total help claims 'stale=server kicked', a bucket that can never be emitted, so even if metrics were wired the reason label would always be 'clean'.

**Suggested fix:** Derive the reason from e.Disconnect (e.g. classify server-initiated/expired codes as stale) or drop the unused constant and fix the help text.

#### 554. [LOW] valueToString drops all non-string/non-error log fields to empty
`realtime/centrifuge/node.go:265` — correctness — conf 0.70 — _low/info (unverified)_

centrifuge LogEntry.Fields commonly contain ints/bools (client/user counts, durations). valueToString returns "" for any type other than string/error, so makeLogHandler emits empty values for those fields, silently discarding useful diagnostic context in bridged log lines.

**Suggested fix:** Handle common scalar types (int/int64/uint64/float64/bool) explicitly, or fall back to fmt.Sprint for the default case rather than returning empty.

#### 555. [LOW] Policy.Delay doc claims fixed values but jitter randomizes the result
`resilience/retry/retry.go:417` — docs — conf 0.75 — _low/info (unverified)_

Delay's doc says 'Attempt 0 returns BaseDelay, attempt 1 returns BaseDelay*Factor'. That holds only with Jitter==0. With the DefaultPolicy/WorkerPolicy jitter of 0.25, Delay(0) returns a randomized value in [BaseDelay*(1-jitter), ...], not BaseDelay. Callers using Delay() for deterministic scheduling will be surprised.

**Suggested fix:** Document that returned delays include jitter when Policy.Jitter>0, or compute Delay from the un-randomized current interval.

#### 556. [LOW] TestDo_stableReset asserts nothing about reset behavior
`resilience/retry/retry_test.go:321` — testing — conf 0.82 — _low/info (unverified)_

The test computes gapAfterStable then discards it (`_ = gapAfterStable // Timing is unreliable`) and has no other assertion on the StableReset effect. It only exercises the code path for panics/coverage; it cannot catch a regression where StableReset stops resetting the backoff (e.g. the bo.Reset()/attempt=0 lines being removed).

**Suggested fix:** Use an injected/deterministic delay capture (e.g. OnRetry recording wait durations) and assert the post-stable delay drops back toward BaseDelay.

#### 557. [LOW] doc.go references nonexistent Reservations() method
`resilience/timeoutbudget/doc.go:40` — docs — conf 0.90 — _low/info (unverified)_

The package doc lists `Used()`, `Remaining()`, and `Reservations()` as the introspection API, but the actual method is `Reservation()` (singular). The plural form does not exist, so the documented observability surface is wrong.

**Suggested fix:** Fix the doc to reference Reservation() (and correct the Used() description per the Used finding).

#### 558. [LOW] Publish re-validates event name on every call, including no-handler path
`runtime/eventbus/eventbus.go:431` — performance — conf 0.50 — _low/info (unverified)_

ValidateStaticLabelValue iterates every rune of eventName on each Publish (before taking the RLock), even though Subscribe already validated registered names and even when no handlers exist (returns nil at line 442). The package doc and the inline note (line 435-437) flag 100K+/sec hot paths, where this O(len(name)) scan per publish is pure overhead for already-known events.

**Suggested fix:** Validate only at Subscribe and on the publish path for events that have handlers, or skip re-validation when len(handlers)==0 path is taken.

#### 559. [LOW] Unbounded-async Publish keeps spawning goroutines after Stop
`runtime/eventbus/eventbus.go:507` — consistency — conf 0.62 — _low/info (unverified)_

When WithUnboundedAsync() is set, b.pool is nil and dispatchAsync always does `go b.runAsync(...)`, ignoring b.stopped. After Stop(), Publish neither returns ErrStopped nor stops dispatching, unlike the bounded path which returns ErrStopped via submit. ErrStopped's doc (line 43) says it is 'returned by Publish when the bus has been stopped' without qualification, so the unbounded mode silently diverges.

**Suggested fix:** Check b.stopped in dispatchAsync's pool==nil branch and return ErrStopped under OnFullError semantics, or document that unbounded-async Stop is a no-op.

#### 560. [LOW] Multiple default-registerer buses share and clobber pool gauges
`runtime/eventbus/metrics.go:51` — correctness — conf 0.70 — _low/info (unverified)_

New() without WithRegisterer uses prometheus.DefaultRegisterer; MustRegisterOrGet then returns the SAME activeWorkers/queueDepth gauges for a second Bus. queueDepth.Set()/Set(0) (pool.go:122,195) are absolute writes, so two buses in one process clobber each other's gauge and Stop on one zeroes the shared queue_depth while the other still has queued events. Counters are additive so they tolerate sharing; gauges do not.

**Suggested fix:** Document that distinct buses need distinct registerers, or use a per-bus ConstLabel (e.g. bus id) so series are not shared.

#### 561. [LOW] perStepMinimum 'at least 1s' comment is false when stopTimeout < 1s
`runtime/lifecycle/runner.go:300` — docs — conf 0.78 — _low/info (unverified)_

The comment claims 'every component gets at least 1s of stop time'. perStep is clamped up to perStepMinimum (1s), but stepCtx = WithTimeout(sharedCtx, perStep) is still bounded by sharedCtx (= stopTimeout). With WithStopTimeout(100ms), the effective per-step deadline is 100ms, not 1s. The clamp is silently overridden by the parent deadline.

**Suggested fix:** Reword the comment to 'at least 1s unless the global stopTimeout is smaller', matching actual behavior.

#### 562. [LOW] Salvage budget violates documented stopTimeout 'hard ceiling'
`runtime/lifecycle/runner.go:333` — correctness — conf 0.82 — _verified_

Once sharedCtx (the stopTimeout) is exhausted, each remaining component's stepCtx is derived from `parent` (forceCtx, background-based) with a fresh 1s salvageBudget, not from sharedCtx. So total shutdown can reach stopTimeout + (remainingComponents x ~1s), and a Stop that respects ctx observes Done only after 1s, not immediately. WithStopTimeout's doc (line 59) claims stopTimeout is 'the hard ceiling, after which every remaining Stop observes ctx.Done()' — the code contradicts this.

**Suggested fix:** Derive the salvage context from sharedCtx (so it is already-cancelled), or document that salvage adds up to salvageBudget per remaining component beyond stopTimeout.

#### 563. [LOW] Sleep-based startup synchronization is flaky
`runtime/lifecycle/runner_test.go:68` — testing — conf 0.70 — _low/info (unverified)_

Several lifecycle tests (CleanShutdown:68, AddFunc:169, ReverseOrderShutdown:395, StopEmitsStoppingLog:557, StopErrorLogIncludesElapsed:626, StopTimeout:201) use time.Sleep(50ms) to wait for components to reach their blocking select before cancelling. Under load/CI this can fire cancel before Start runs, making started.Load() races or premature shutdown assertions flaky. The eventbus suite avoids this with a canary (waitForWorkers).

**Suggested fix:** Replace fixed sleeps with a started channel/atomic the component closes/sets, then require.Eventually or block on it before cancel.

#### 564. [LOW] Put returns ErrInstanceNotFound for an empty-ID write (wrong sentinel)
`runtime/saga/memory_state.go:32` — error-handling — conf 0.60 — _low/info (unverified)_

Put returns ErrInstanceNotFound when inst.ID == "" (line 32). The comment admits this is a programmer-bug path, but reusing the not-found sentinel for a validation failure means a caller doing errors.Is(err, ErrInstanceNotFound) on a Put result misclassifies a missing-ID misuse as 'instance absent'. A distinct validation error (or panic, matching the package's fail-fast style elsewhere) would be clearer.

**Suggested fix:** Return a dedicated `errors.New("saga: Put requires a non-empty Instance.ID")` instead of the not-found sentinel.

#### 565. [INFO] Mixed && / || fire condition is correct but error-prone
`io/progress/progress.go:123` — api-design — conf 0.70 — _low/info (unverified)_

The expression `shouldFire && n > 0 || (shouldFire && err != nil)` relies on Go operator precedence and is hard to verify at a glance. It is functionally correct (the n==0,err==nil case already returns early), but the redundant `shouldFire` and unparenthesized first clause invite future regressions.

**Suggested fix:** Rewrite as `if shouldFire && (n > 0 || err != nil)` for clarity; behavior is unchanged.

#### 566. [INFO] Pre-cancelled ctx records attempts=0 in the histogram
`resilience/retry/metrics.go:92` — correctness — conf 0.55 — _low/info (unverified)_

When ctx is already cancelled, doWithPolicy returns at the top before incrementing totalAttempts, so recordOutcome observes attempts=0 with outcome=failed_ctx_cancelled. The histogram's smallest bucket is 1, so 0-attempt terminations sit below all buckets — slightly misleading 'attempts' data for the cancelled-before-first-call case. Functionally harmless.

**Suggested fix:** Optionally count the skipped pre-cancel as 0 explicitly in docs, or only observe attempts when >=1.

#### 567. [INFO] Backoff wrapper exposes shared mutable state with no documented thread-safety
`resilience/retry/retry.go:524` — concurrency — conf 0.80 — _low/info (unverified)_

Backoff wraps a single *backoff.ExponentialBackOff, which the upstream library explicitly documents as 'not thread-safe'. Backoff.Next()/Reset() add no synchronization and the doc says nothing about concurrency. Concurrent use from multiple goroutines is a data race. The current in-repo caller (amqpbackend reconnect) uses it single-goroutine, so no active bug.

**Suggested fix:** Document Backoff as single-goroutine-only, or guard with a mutex.

#### 568. [INFO] Default auto-started pool leaks 8 worker goroutines if Stop is never called
`runtime/eventbus/eventbus.go:255` — resource-leak — conf 0.55 — _low/info (unverified)_

New() with no options auto-starts an 8-worker pool (startWorkers). If the caller never invokes Stop (the very 'tests, scripts, simple programs' use case the auto-start targets), those 8 goroutines run for process lifetime. Documented intent, but it means each plain eventbus.New() permanently leaks 8 goroutines, surprising in tests that create many buses.

**Suggested fix:** Consider lazy worker start on first async dispatch, or a finalizer-based stop; at minimum keep the Stop guidance prominent in the constructor doc.


### observability — auditlog, health, logging, metrics, tracing, slo, pprof, dashboards

_14 findings — 4 medium, 6 low, 4 info_

#### 569. [MEDIUM] RetentionJob deletion permanently breaks VerifyChain genesis check
`observability/auditlog/retention.go:32` — correctness — conf 0.80 — _verified_

DeleteBefore removes the oldest events, but VerifyChain (auditlog.go:648 / chain.go:125) requires event[0].PrevHMAC to be empty/zero. After the first retention run the new oldest event has a non-empty PrevHMAC, so VerifyChain returns ErrChainBroken forever. Both retention and tamper-evidence are prominently documented features yet are mutually exclusive; operators wiring the documented @daily job will see chain verification fail.

**Suggested fix:** Document the incompatibility loudly, or have RangeChain/VerifyChain tolerate a non-genesis head after a recorded retention watermark (additive option).

#### 570. [MEDIUM] traceHandler injects trace_id/span_id under any open slog group
`observability/logging/logging.go:124` — correctness — conf 0.78 — _verified_

Handle() calls record.AddAttrs(trace_id, span_id), but slog applies the WithGroup qualifier to all record attrs at emit time. A logger from New(...).WithGroup("req") nests trace_id/span_id under "req" instead of top-level, breaking the documented trace/log correlation that pipelines key off. WithGroup is exported public API, so any group-using consumer is affected.

**Suggested fix:** Capture group-free handler at construction for ID injection, or document that grouped loggers nest IDs. Additive: add a non-grouped sub-handler reference used only for the two trace attrs.

#### 571. [MEDIUM] Doc example calls pyroscope.Module which does not exist
`observability/pyroscope/doc.go:39` — docs — conf 0.95 — _verified_

The package example shows `app.New(...).With(pyroscope.Module(pyroscope.Config{...}))`, but the package only exports `Component` (pyroscope.go) and `Option`/`WithLogger`. There is no `Module` symbol anywhere in the module, so a user copying the documented golden path gets a compile error. Misleads API discovery for the primary advertised usage.

**Suggested fix:** Either add a `Module` adapter (additive) or change the example to use Component with the lifecycle runner as the package's quick-start already shows.

#### 572. [MEDIUM] Missing error/success label silently reports 0% error rate (false-green SLO)
`observability/slo/slo.go:352` — error-handling — conf 0.60 — _verified_

evaluateErrorRate sums all series as total and only series matching the status filter as matched. If the gathered counter lacks the configured/default `status` label (or values are not 3 chars so '5..' never matches), matched=0 and the SLO reports 0% errors / 100% success instead of no-data. This is the same silent-failure class the code documents fixing for the old 'code' label, but it persists whenever the label is absent.

**Suggested fix:** Return NaN when no series carries filter.Name at all (label entirely absent), distinguishing 'no error label' from 'zero errors'.

#### 573. [LOW] Postgres Store does not implement RetentionStore.DeleteBefore
`observability/auditlog/postgres/store.go:49` — api-design — conf 0.72 — _low/info (unverified)_

RetentionJob requires a RetentionStore (DeleteBefore), but the only bundled production-grade Store (postgres) implements just the base Store interface. doc.go shows RetentionJob(store, ...) but a *postgres.Store won't satisfy RetentionStore, so the documented retention wiring fails to compile with the real store; only the test-only Memory-style stores can be used (and MemoryStore itself also lacks DeleteBefore).

**Suggested fix:** Add DeleteBefore to postgres.Store (DELETE WHERE occurred_at < $1) so the documented RetentionJob example is usable with the production store.

#### 574. [LOW] TestTraceHandler_withGroup asserts only wrapper type, not nesting behavior
`observability/logging/logging_test.go:204` — testing — conf 0.70 — _low/info (unverified)_

The test confirms WithGroup returns a *traceHandler but never emits a record through a grouped handler to verify where trace_id/span_id land. This is exactly the path that exhibits the mis-nesting bug, so the test gives false confidence in the WithGroup path.

**Suggested fix:** Emit InfoContext through New(...).WithGroup("g") with a span context and assert trace_id placement in the JSON output.

#### 575. [LOW] Global mutex taken on every observation serializes the hot path
`observability/promutil/labelguard/labelguard.go:195` — performance — conf 0.60 — _low/info (unverified)_

permit() calls vecName(vec) on every ObserveCounter/ObserveHistogram, which acquires the package-wide vecNameMu even for cache hits. The vec ops themselves are lock-free in client_golang, but this guard turns every guarded observation into a contended critical section across all vecs sharing the AllowedLabels, contradicting the package's stated 'observation-time, hot-path' purpose.

**Suggested fix:** Cache vec names in an atomic.Pointer to an immutable map (copy-on-write) or sync.Map so cache hits are lock-free.

#### 576. [LOW] Package doc says go_max_rss_bytes is Linux-only but it works on Darwin
`observability/runtimemetrics/runtimemetrics.go:14` — docs — conf 0.90 — _low/info (unverified)_

The package overview lists 'go_max_rss_bytes (gauge, Linux only)'. The actual implementation (rss_unix.go build tag `linux || darwin`, rss_darwin.go) emits the metric on Darwin too, and the Desc help string correctly says 'Linux/Darwin'. The overview is wrong and would mislead operators running on macOS into thinking the series is absent.

**Suggested fix:** Change the overview bullet to '(gauge, Linux/Darwin only)' to match rss_unix.go and the Desc help text.

#### 577. [LOW] Counter named with reserved _sum suffix (go_gc_pause_seconds_sum)
`observability/runtimemetrics/runtimemetrics.go:59` — api-design — conf 0.55 — _low/info (unverified)_

go_gc_pause_seconds_sum is registered as a standalone CounterValue. The _sum suffix is the Prometheus convention for the auto-generated child series of a histogram/summary. Recording rules and tooling may misinterpret it, and registering any future histogram/summary named go_gc_pause_seconds in the same registry would collide on the _sum series.

**Suggested fix:** Rename to go_gc_pause_seconds_total (matches counter convention) — BREAKING for dashboards; provide as v3 candidate or add the renamed metric additively.

#### 578. [LOW] First TracerProvider is created, registered for cleanup, then discarded unused
`observability/tracing/tenant_sampler_test.go:61` — testing — conf 0.85 — _low/info (unverified)_

TestTenantSampler_OverrideRouteForKnownTenant builds tp at line 61 and registers t.Cleanup to Shutdown it (line 62), then immediately overwrites tp at line 65 with the exporter-backed provider. The first provider is never used; the second (actually exercised) provider is never Shutdown. Harmless but misleading and a minor resource smell.

**Suggested fix:** Construct the exporter-backed provider once and register cleanup on that single instance.

#### 579. [INFO] canonicalEvent serializes prevHMAC twice (param and event.PrevHMAC field)
`observability/auditlog/chain.go:56` — consistency — conf 0.85 — _low/info (unverified)_

canonicalEvent includes prevHMAC (param, line 56) and event.PrevHMAC (field, line 66). All callers pass event.PrevHMAC as the param (auditlog.go:480, :641), so the same value is HMAC'd twice. Harmless for security (consistent on both sign and verify) but redundant and a future-maintenance trap if a caller ever passes a differing param.

**Suggested fix:** Drop one of the two encodings (prefer keeping event.PrevHMAC and removing the param) to make the canonical form unambiguous; note this changes the on-disk HMAC format.

#### 580. [INFO] redactedURL rarely emits [INVALID URL] because url.Parse is lenient
`observability/logattr/logattr.go:177` — correctness — conf 0.60 — _low/info (unverified)_

url.Parse accepts almost any string (e.g. "javascript:alert(1)", "not a url") without error, so the [INVALID URL] branch only triggers for control-char/opaque-scheme malformations like "://invalid". The value is fully redacted afterwards regardless, so the branch is mostly cosmetic, but the doc/comment implies meaningful validation that does not occur.

**Suggested fix:** Either drop the parse (it adds nothing since output is fully redacted) or clarify that it only catches gross syntactic errors.

#### 581. [INFO] Label NAMES validated with value validator, allowing non-name characters
`observability/promutil/labelguard/labelguard.go:108` — consistency — conf 0.55 — _low/info (unverified)_

New validates allowlist label NAMES with promutil.ValidateStaticLabelValue, which permits '.' and ':' (valid for label values, invalid for Prometheus label names per [a-zA-Z_][a-zA-Z0-9_]*). Harmless today because names are only used as map keys, but it accepts allowlists keyed by names that can never match a real Prometheus label, masking wiring mistakes.

**Suggested fix:** Validate label names with promutil.ValidateMetricNamePart (the identifier-shaped check) instead of the value validator.

#### 582. [INFO] Per-scrape cost claim understates the work done
`observability/runtimemetrics/runtimemetrics.go:17` — docs — conf 0.70 — _low/info (unverified)_

Doc says 'Cost is one mallocless ReadMemStats per scrape', but Collect also calls runtime.NumGoroutine and runtime.ThreadCreateProfile(nil) on every scrape. ReadMemStats also stops the world briefly. The claim is misleading about the true per-scrape cost.

**Suggested fix:** Adjust the doc to mention NumGoroutine/ThreadCreateProfile and that ReadMemStats has a small STW cost.


### auth+authz — oauth2, authz, openfga

_7 findings — 1 high, 2 medium, 4 low_

#### 583. [HIGH] Open redirect: post-login redirect_to is attacker-controlled and unvalidated
`auth/oauth2/client.go:322` — security — conf 0.85 — _reviewer-reported (unverified)_

handleLogin (line 221) stores the raw `redirect_to` query param into StateEntry.RedirectTo; handleCallback redirects to it verbatim via http.Redirect after a successful login. Any absolute URL (e.g. https://evil.com) is honored. An attacker sends victims /oauth/login?redirect_to=https://evil.com for phishing/token relay. The kit already ships httpx.SafeRedirect(target, allowedHosts...) for exactly this, but it is not used here.

**Suggested fix:** Pass redirect_to through httpx.SafeRedirect (or require a relative/same-host path) before persisting/redirecting; reject absolute off-host targets.

#### 584. [MEDIUM] WithoutPKCE on a public client (no ClientSecret) yields an unprotected code flow
`auth/oauth2/client.go:77` — security — conf 0.50 — _reviewer-reported (unverified)_

NewClient never verifies that disabling PKCE is paired with a confidential client. A public client (ClientSecret nil) plus WithoutPKCE() produces a flow with neither PKCE nor a client secret, i.e. no authorization-code-interception protection — exactly the configuration RFC 7636 forbids. The doc says 'confidential clients only' but nothing enforces it; the kit could fail closed.

**Suggested fix:** In NewClient, return an error when usePKCE is false and cfg.ClientSecret is nil/empty.

#### 585. [MEDIUM] MemoryStateStore/MemorySessionStore never prune abandoned entries (unbounded growth)
`auth/oauth2/memory_stores.go:96` — resource-leak — conf 0.70 — _reviewer-reported (unverified)_

Both stores only expire entries lazily on Get of the exact key (or Delete); there is no background janitor. Every /login Put creates a state entry that, if the callback never arrives (abandoned login), lingers forever — an unbounded memory leak / DoS vector for the documented single-process production use. doc.go:65 claims 'Stale state entries are pruned by the store's TTL', which the code does not do. No test covers prune-on-abandon.

**Suggested fix:** Add a sweep goroutine or opportunistic eviction on Put; or correct doc.go to state expiry is lazy-only and entries persist until accessed/deleted.

#### 586. [LOW] Callback reflects provider error params and verify error text into the response
`auth/oauth2/client.go:252` — security — conf 0.50 — _reviewer-reported (unverified)_

handleCallback echoes attacker-influenced query params `error`/`error_description` (line 252) and the id_token verify error (line 290) into the response body. http.Error uses text/plain + nosniff so this is not XSS, but it reflects arbitrary attacker content and can surface internal verification detail to the caller.

**Suggested fix:** Return fixed sentinel messages to the client and log the provider/verify detail server-side via the existing slog logger.

#### 587. [LOW] Logout clear-cookie omits Domain set by sessionCookie
`auth/oauth2/client.go:334` — correctness — conf 0.70 — _reviewer-reported (unverified)_

sessionCookie() sets Domain=c.cookieDomain, but handleLogout's clearing cookie omits Domain. When WithCookieDomain is configured, the browser will not match/remove the domain-scoped session cookie, so the stale cookie value lingers client-side. Impact is limited because the server-side session is deleted (subsequent Get returns ErrSessionNotFound), but the set/clear attributes are inconsistent.

**Suggested fix:** Mirror Domain (and any future attributes) from sessionCookie in the logout clearing cookie.

#### 588. [LOW] Doc links reference non-existent [Memory]/[NewMemory] symbols
`authz/memory.go:8` — docs — conf 0.90 — _reviewer-reported (unverified)_

Comments call the type [Memory] and the constructor [NewMemory] (lines 8, 25, and authz.go:51), but the actual symbols are MemoryStore and NewMemoryStore. godoc cross-references will be dead links and the prose is misleading.

**Suggested fix:** Update the doc comments to MemoryStore / NewMemoryStore.

#### 589. [LOW] Doc references non-existent ErrScopeAlreadyRegistered sentinel
`authz/scope.go:43` — docs — conf 0.85 — _reviewer-reported (unverified)_

Register's doc says it 'Returns ErrScopeAlreadyRegistered if the same scope name was registered with a different description', but no such exported error exists; the function returns an ad-hoc fmt.Errorf. Callers cannot errors.Is against the documented sentinel.

**Suggested fix:** Either define and return a real ErrScopeAlreadyRegistered (wrapped) or remove the named-error claim from the doc.


### flags — feature flags

_4 findings — 1 medium, 2 low, 1 info_

#### 590. [MEDIUM] SetEvalErrorHook, Client.Object/ObjectE, and generic Object/ObjectE have zero tests repo-wide
`flags/flags.go:280` — testing — conf 0.85 — _reviewer-reported (unverified)_

Grep across the repo finds no test or usage of SetEvalErrorHook, the generic Object/ObjectE free functions, or Client.Object/ObjectE. The FR-034 error-observation path (observeError, finishEval, hook concurrency via atomic.Pointer) and the generic type-mismatch branch (ObjectE returning an explicit error) are entirely unexercised, despite being the package's headline audit fixes.

**Suggested fix:** Add tests: hook fires on invalid key/provider error, nil-hook clear, ObjectE type-mismatch error, Object fallback-on-mismatch.

#### 591. [LOW] finishEval returns nil error when ErrorCode is set but ErrorMessage is empty
`flags/flags.go:292` — error-handling — conf 0.55 — _reviewer-reported (unverified)_

If EvaluationDetails carries a non-empty ErrorCode with an empty ErrorMessage and the SDK returned nil err, finishEval fires the hook (with empty message, nil err) but returns nil — so BoolE/StringE/etc. report success despite the provider signalling an error code. Callers using the E-variants specifically to detect provider regressions (the FR-034 rationale) miss this case.

**Suggested fix:** Synthesize an error from d.ErrorCode when ErrorMessage is empty, e.g. errors.New(string(d.ErrorCode)).

#### 592. [LOW] evalCtx 'case string' branch is unreachable dead code
`flags/flags.go:342` — correctness — conf 0.75 — _reviewer-reported (unverified)_

ctx.Value(userKeyCtx{}) can only hold a userKeyValue: userKeyCtx is unexported and WithUserKey is the sole writer, always storing validUserKeyValue(...). The string case (lines 342-352), including its own validation path, can never execute, yet it duplicates validation logic that can silently drift from the live path.

**Suggested fix:** Delete the string case or add a test proving why it exists.

#### 593. [INFO] MustNew panic discards the underlying provider-init error
`flags/flags.go:101` — error-handling — conf 0.60 — _reviewer-reported (unverified)_

MustNew panics with the fixed string "client configuration is invalid", dropping the wrapped SetNamedProviderAndWait error — so a startup provider failure (auth, network, SDK load) gives no diagnostic. This follows the kit's constant-panic-message convention (no secret reflection), but unlike key-validation panics, provider-init errors here are the primary debugging signal at boot.

**Suggested fix:** Log the error before panicking, or panic with a redact-safe wrapper that keeps the error class.


### cmd+tools — kit-doctor, kit-migrate, kit-new, kit-verify, kit-catalog, release tooling

_33 findings — 4 high, 9 medium, 18 low, 2 info_

#### 594. [HIGH] Critical-severity centrifuge-missing-jwt-auth rule never fires: wrong module path
`cmd/kit-doctor/rules/centrifuge_missing_jwt_auth.go:29` — correctness — conf 0.95 — _reviewer-reported (unverified)_

centrifugeImports lists github.com/bds421/rho-kit/realtime/v2/centrifuge, but realtime has no go.mod; the module is github.com/bds421/rho-kit/realtime/centrifuge/v2 (realtime/centrifuge/go.mod), and examples/realtime-broadcast/internal/app/app.go imports that path. The rule's import match therefore never succeeds against real consumer code, so an unauthenticated centrifuge.NewNode (rated Critical by the rule itself) is never flagged.

**Suggested fix:** Change centrifugeImports to github.com/bds421/rho-kit/realtime/centrifuge/v2 and fix the engine_test.go fixtures to match.

#### 595. [HIGH] Both websocket rules are dead: import path does not exist
`cmd/kit-doctor/rules/websocket_any_origin_unsafe.go:30` — correctness — conf 0.95 — _reviewer-reported (unverified)_

websocketImports lists github.com/bds421/rho-kit/httpx/v2/websocket, but httpx/websocket has its own go.mod with module path github.com/bds421/rho-kit/httpx/websocket/v2. Real consumers (testing/integrationtest/websocket/echo_test.go) import the latter; the listed path is unresolvable. importAliasesFor never matches, so websocket-any-origin-unsafe (High, cross-site WebSocket hijacking) and websocket-missing-max-connections never fire on any real consumer code.

**Suggested fix:** Change websocketImports to github.com/bds421/rho-kit/httpx/websocket/v2 (optionally keep the old path too); update engine_test.go fixtures.

#### 596. [HIGH] auditlog and outbox migrations share goose version 20260514000001; publish-all yields a directory goose refuses to run
`cmd/kit-migrate/main.go:34` — correctness — conf 0.78 — _reviewer-reported (unverified)_

registry ships observability/auditlog/postgres/migrations/20260514000001_create_audit_log_events.sql and infra/outbox/postgres/migrations/20260514000001_create_outbox_entries.sql — identical numeric goose version prefix. The documented primary invocation `kit-migrate publish --to=./migrations` (no component filter) copies both into one directory; pressly/goose errors with "found duplicate migration version" on collect, so `goose up` fails for any service using both components. kit-migrate has no duplicate-version guard.

**Suggested fix:** Renumber one migration in a patch release and add a duplicate-version-prefix check to buildPublishPlan that fails publish/check with a clear message.

#### 597. [HIGH] Shipped-wave detection far looser than documented contract
`tools/check-doc-rot/main.go:83` — correctness — conf 0.78 — _verified_

Doc (lines 32-33) says a wave counts as shipped only if a commit matches ^(feat|fix|...)\(v2\).*wave N. commitWaveRE is just '(?i)wave\s+(\d+)' with no prefix anchor, so any subject mentioning 'wave N' (style(v2), fix(kit-doctor), 'docs(audit): record Wave 4+5', even a revert/wip) marks it shipped. A stale 'wave N' doc claim passes if any commit casually names N, defeating the anti-rot guarantee.

**Suggested fix:** Anchor commitWaveRE to the documented conventional-commit shape, or update the doc to match the loose behavior. Also handle 'Wave 4+5'.

#### 598. [MEDIUM] Test fixtures pin the same wrong import paths as the dead rules
`cmd/kit-doctor/engine_test.go:1234` — testing — conf 0.90 — _reviewer-reported (unverified)_

All websocket/centrifuge tests (lines 1234, 1250, 1266, 1282, 1298, 1314, 1330, 1346) write fixtures importing httpx/v2/websocket and realtime/v2/centrifuge — paths that cannot resolve in a real build. Because scan() only parses (never compiles), the suite passes while the rules are inert against actual consumer code. The tests assert the rule mechanism, not the real-world contract.

**Suggested fix:** Use the canonical module paths in fixtures; add a test asserting each rule's import constants exist as modules/packages in the workspace.

#### 599. [MEDIUM] Interactive mode exits 1 even when the operator applied every fix
`cmd/kit-doctor/main.go:85` — correctness — conf 0.85 — _reviewer-reported (unverified)_

runInteractive returns the applied-fix count, but main discards it and computes exitCode(repoFindings, floor) from the original pre-fix findings. All repo-check findings are High, so with the default floor an operator who answers "y" to every prompt and has every fix succeed still gets exit 1. The inline comment says exit-1 is for findings the operator DECLINED to fix; the code cannot distinguish declined from applied.

**Suggested fix:** Have runInteractive report which findings were successfully fixed (or re-run checkers after fixes) and exclude them from the exit-code computation.

#### 600. [MEDIUM] cmdCheck (the CI drift gate) has zero test coverage
`cmd/kit-migrate/main.go:187` — testing — conf 0.90 — _reviewer-reported (unverified)_

main_test.go covers parse, list, publish drift refusal, and symlink rejection, but no test ever invokes the `check` subcommand. cmdCheck is 65 lines with distinct exit-code semantics (0 sync / 2 drift / 1 error), symlink rejection, and IsNotExist skipping — all untested. Its doc explicitly positions it as a pre-merge CI guard, making regressions here silently break the gate.

**Suggested fix:** Add tests: check on in-sync dir (exit 0), drifted file (exit 2 + message), symlinked target (exit 1).

#### 601. [MEDIUM] kit-migrate check exits 0 with 'OK: 0 migration(s) in sync' when --to points at a nonexistent directory
`cmd/kit-migrate/main.go:249` — api-design — conf 0.75 — _reviewer-reported (unverified)_

If targetDir does not exist (e.g. a typo'd path in CI), rejectSymlinkPathComponents returns nil on IsNotExist, every os.ReadFile hits IsNotExist and continues, checked stays 0, and the command prints OK and exits 0. A drift gate that silently passes when aimed at the wrong directory is a false-negative footgun; the doc only blesses absent files, not an absent directory.

**Suggested fix:** Error (or at least warn and exit non-zero) when targetDir does not exist; consider flagging checked==0 runs.

#### 602. [MEDIUM] Alloc regression check cannot fire for zero-alloc baselines
`tools/check-bench-regression/main.go:133` — correctness — conf 0.80 — _verified_

Condition is r.Allocs > base.Allocs*2 && r.Allocs > base.Allocs+1. For a zero-alloc hot path (base.Allocs=0) this requires r.Allocs>0 AND r.Allocs>1, so a 0->1 alloc regression is never caught. The header doc (lines 24-25) advertises 'alloc count doubling is loud' and zero-alloc paths (e.g. BenchmarkConnLimiter_Parallel 0 in baseline) are the ones most worth protecting.

**Suggested fix:** Special-case base.Allocs==0 to flag any r.Allocs>0, or drop the +1 guard for the zero baseline.

#### 603. [MEDIUM] bytes/op parsed and baselined but never compared
`tools/check-bench-regression/main.go:133` — correctness — conf 0.74 — _verified_

Bytes is parsed (line 209), stored in the baseline file (line 278), and printed in the 'NOT in baseline' message, but no comparison uses it. A benchmark that doubles bytes/op while holding ns/op and allocs/op stable passes silently, contradicting the tool's stated goal of catching per-call cost regressions (header lines 6-11).

**Suggested fix:** Add a bytes/op tolerance check mirroring the allocs check, or remove Bytes from the baseline format to avoid implying it is enforced.

#### 604. [MEDIUM] Sub-1ns benchmarks silently dropped and unprotected against ns regression
`tools/check-bench-regression/main.go:206` — correctness — conf 0.72 — _verified_

Benchmarks printing '0.0000 ns/op' parse to nsPerOp==0 and are skipped (line 207), so they vanish from results and never trigger the 'NOT in baseline' notice. A benchmark baselined at 0 ns/op (BenchmarkWrapError_NilError 0) also bypasses the ns check at line 129 (base.NsPerOp>0 is false). Net: very fast helpers have no ns-regression coverage at all.

**Suggested fix:** Use -benchtime with higher count or detect dropped benchmarks; treat a result present-now but absent-from-results as an error rather than silently skipping.

#### 605. [MEDIUM] JSON dashboard scan only harvests the 'expr' key, missing other PromQL fields
`tools/check-dashboard-labels/main.go:315` — correctness — conf 0.60 — _verified_

collectStrings(doc, "expr") only pulls fields literally named 'expr'. Grafana templating variables (templating.list[].query, e.g. label_values(metric, l)) and any panel using a differently-named PromQL field are never scanned, so label drift there is invisible. The test at main_test.go:217 even includes a 'query' field that is silently ignored.

**Suggested fix:** Also collect 'query' (and other known PromQL-bearing keys), or document the expr-only scope as an explicit limitation in CI output.

#### 606. [MEDIUM] Error-wrap gate uses a closed identifier whitelist, missing most local error vars
`tools/check-fmt-errorf-wrap/main.go:202` — security — conf 0.82 — _verified_

isErrorIdent only matches 9 hardcoded names (err, perr, ...). The doc claims it flags any identifier ('typically named err...'). Local backend errors like marshalErr (data/cache/compute.go:457-468 wrapping json.Marshal errors with ': %w') are NOT flagged, so the wave-136 trust-boundary leakage gate misses exactly the locals it was built to catch.

**Suggested fix:** Flag any *ast.Ident last-arg (excluding pkg-qualified sentinels), not a fixed name set; allowlist exceptions via the existing kit:ok comment marker.

#### 607. [LOW] Manifest writers swallow stdout/CSV errors and exit 0
`cmd/kit-catalog/main.go:349` — error-handling — conf 0.60 — _reviewer-reported (unverified)_

emitJSON (`_ = enc.Encode`), emitCSV (`_ = w.Write`), and the deferred `w.Flush()` ignore all write errors and never check w.Error(). On a broken pipe or short write the output can be silently truncated while the process still exits 0, so fleet-automation consuming the manifest can act on incomplete data without any signal.

**Suggested fix:** Check Encode/Write/Flush (csv: w.Error()) results and fail() with a non-zero exit on write error.

#### 608. [LOW] One unreadable directory aborts the entire scan
`cmd/kit-doctor/engine.go:30` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

The WalkDir callback returns err unchanged on directory access errors, so a single permission-denied subdirectory fails the whole scan with exit 2 — inconsistent with the deliberate per-file lenience (unreadable or unparseable files become Warning findings and the scan continues).

**Suggested fix:** On directory errors, emit an io-error Warning finding and return filepath.SkipDir instead of propagating the error.

#### 609. [LOW] Repo findings without Fix are invisible yet still drive exit-1
`cmd/kit-doctor/main.go:79` — error-handling — conf 0.75 — _reviewer-reported (unverified)_

Repo-level findings are excluded from standard text/json output by design, and runInteractive silently skips findings with nil Fix (its comment claims they "already appear in the standard text output" — false for repo findings). A repo-check-error Warning (runRepoCheckers, no Fix) is therefore never displayed anywhere, but with -strict=warning it still triggers os.Exit(1), giving an exit-1 with no visible cause.

**Suggested fix:** Print Fix-less repo findings in interactive output (without a prompt), or exclude them from the repo exit-code computation.

#### 610. [LOW] Suppression marker matched by substring anywhere inside a comment
`cmd/kit-doctor/rules/exemptions.go:149` — correctness — conf 0.70 — _reviewer-reported (unverified)_

matchesSuppression uses strings.Index, so any comment that merely mentions the marker suppresses the finding on that line or the line below — e.g. "// TODO: consider kit-doctor:allow default-http-client here" silences the rule today. This contradicts the package doc's claim that suppressions "must be deliberate; the linter never matches by substring".

**Suggested fix:** Require the marker at the start of the comment body (after optional whitespace), matching the documented format.

#### 611. [LOW] Exported rules package relies on unsynchronized global mutable state
`cmd/kit-doctor/rules/helpers.go:173` — concurrency — conf 0.80 — _reviewer-reported (unverified)_

parents (set via the exported SetCurrentFile) and packageCache (exemptions.go:74) are plain package-level maps with no synchronization. The rules package is exported from module cmd/kit-doctor/v2 and Rule.Run is a public interface; any caller running rules concurrently (e.g. a parallel scanner) races on both maps and gets wrong parent lookups. Also, forgetting SetCurrentFile silently degrades chainTop instead of failing.

**Suggested fix:** Pass parent maps per-scan (e.g. a Scan context struct) instead of globals; additive API, keep SetCurrentFile as a deprecated shim.

#### 612. [LOW] Option rules report Critical false positives for spread/variable-held options
`cmd/kit-doctor/rules/jwt_missing_claims.go:54` — correctness — conf 0.85 — _reviewer-reported (unverified)_

callHasOption only matches literal call-expression args. jwt.Module(url, opts...) (call.Ellipsis set) or opts built in a variable always yields Critical/High findings even when opts contains WithIssuer/WithAudience. Same FP class hits idempotency-user-extractor, centrifuge-missing-jwt-auth, websocket-missing-max-connections, http-server-error-log, and chainRegistersRateLimitModule (m := ratelimit.IP(...); .With(m)). Conditional option building is common in real services, so noisy Critical findings will train operators to suppress.

**Suggested fix:** Skip (or downgrade to Info "cannot verify statically") when call.Ellipsis.IsValid() or when non-literal args could carry options.

#### 613. [LOW] Kit self-detection by path substring is fragile in both directions
`cmd/kit-doctor/rules/kit_primitive_collision.go:111` — correctness — conf 0.80 — _reviewer-reported (unverified)_

Skipping files whose path contains "/rho-kit/" misfires both ways: a rho-kit checkout under any other directory name (e.g. ~/src/kit) flags the kit's own clock/retry/cache packages, while any consumer repo whose path happens to contain a rho-kit directory segment is silently never checked — the rule's entire purpose defeated by checkout location.

**Suggested fix:** Detect the kit by module path via packageAtPath (prefix github.com/bds421/rho-kit/) instead of filesystem path substring.

#### 614. [LOW] Comment claims per-directory dedup but rule emits one finding per file
`cmd/kit-doctor/rules/kit_primitive_collision.go:124` — correctness — conf 0.90 — _reviewer-reported (unverified)_

The comment says "Only flag once per package directory ... Use the directory path as the de-dup key", but no dedup exists: Run returns a finding for every non-test file whose package name collides, so a 10-file package clock yields 10 identical Info findings. The comment describes code that was never written.

**Suggested fix:** Implement the dedup (e.g. track seen directories in scan state) or fix the comment to match actual behavior.

#### 615. [LOW] cmd/kit-migrate/main.go and core/apperror/errors.go are not gofmt-clean
`cmd/kit-migrate/main.go:25` — build-ci — conf 0.97 — _reviewer-reported (unverified)_

gofmt -l flags both files: kit-migrate's import block is unsorted (observability/auditlog import sorted before infra/outbox), and apperror/errors.go lines 136-138 have hand-aligned extra spaces before braces that gofmt collapses. Indicates the repo's lint gate does not enforce gofmt on these modules, so drift will accumulate.

**Suggested fix:** Run gofmt -w on both files; ensure golangci-lint enables gofmt for all workspace modules.

#### 616. [LOW] Underlying errors discarded without %w throughout kit-migrate ('reading migrations failed', 'reading migration target failed')
`cmd/kit-migrate/main.go:107` — error-handling — conf 0.70 — _reviewer-reported (unverified)_

cmdList line 107, buildPublishPlan lines 308/314/324/343 replace real errors (permission denied, I/O error, not-a-directory) with constant strings carrying no cause and no %w. Tests show this is deliberate path-sanitization, but for a local CLI the operator supplied the path themselves; they cannot distinguish EACCES from corruption. Inconsistent too: lines 327/330 in the same function do wrap with %w.

**Suggested fix:** Wrap with %w consistently; if paths must be hidden, strip only the path, not the errno class.

#### 617. [LOW] ModulePath is rendered into go.mod and Go source with no validation beyond non-empty
`cmd/kit-new/scaffold.go:128` — security — conf 0.75 — _reviewer-reported (unverified)_

ServiceName is strictly kebab-case validated, but ModulePath only checks emptiness before being template-rendered into go.mod's module line and import strings in main.go/wire.go. A path with spaces, quotes, or newlines produces a silently broken tree (or content injection into generated files) discovered only at go mod tidy. Inconsistent rigor versus the sibling validation.

**Suggested fix:** Validate with golang.org/x/mod/module.CheckPath (or a conservative regexp) and reject without echoing the value.

#### 618. [LOW] Flag parse errors printed twice to stderr
`cmd/kit-verify/main.go:112` — error-handling — conf 0.80 — _reviewer-reported (unverified)_

parseConfig uses flag.ContinueOnError with fs.SetOutput(stderr), so the flag package itself prints the error plus usage to stderr; run() then prints the same returned err again via fmt.Fprintln(stderr, err). Every bad-flag invocation emits the message twice, cluttering CI logs.

**Suggested fix:** Skip the re-print for flag-parse errors (e.g. fs.SetOutput(io.Discard) and print once, or return a sentinel).

#### 619. [LOW] Header probes FAIL on legitimate duplicate header lines with a misleading detail message
`cmd/kit-verify/main.go:551` — correctness — conf 0.65 — _reviewer-reported (unverified)_

singletonResponseHeader requires exactly one header line; expectHeader/expectHeaderPresent then report "does not contain expected value"/"missing". Per RFC 9110, multiple Cache-Control or X-Frame-Options lines are legal and semantically merged (common when a proxy appends its own). A service correctly emitting no-store twice fails readiness-no-store with a detail that misdirects debugging toward the value rather than multiplicity.

**Suggested fix:** Join multiple values for list-valued headers (or emit a 'duplicate header' detail) instead of failing as missing/mismatched.

#### 620. [LOW] Tooling unit has near-zero test coverage outside dashboard-labels
`tools/check-dashboard-labels/main_test.go:126` — testing — conf 0.80 — _low/info (unverified)_

Of the five tools in this unit, only check-dashboard-labels has tests; check-bench-regression, check-doc-rot, check-fmt-errorf-wrap, and release-planner have none. These are release/CI gates where a false negative ships a real regression or drift, yet core parsers (parseBenchOutput, parseGoMod, dependencyLevels, scanFile/loadShippedWaves) are untested.

**Suggested fix:** Add table-driven tests for the parsers and threshold logic of the four untested tools; they are pure functions and easy to cover.

#### 621. [LOW] future-wave regex misses 'tracked for ... wave' at line end
`tools/check-doc-rot/main.go:76` — correctness — conf 0.70 — _low/info (unverified)_

The 'tracked.{1,30}for.{1,30}wave[^0-9]' alternative requires a non-digit character AFTER 'wave', so a phrase ending exactly at 'wave' with no trailing punctuation (e.g. 'tracked for the next wave') is not flagged. Verified empirically. Common phrasings are covered by the 'future wave'/'follow-up wave' alternatives, so impact is narrow.

**Suggested fix:** Change the trailing [^0-9] to (?:[^0-9]|$) so an end-of-line 'wave' still matches.

#### 622. [LOW] One unreadable Markdown file aborts the whole doc-rot scan
`tools/check-doc-rot/main.go:137` — error-handling — conf 0.55 — _low/info (unverified)_

scanFile's open error is returned from the WalkDir callback (lines 137-139), aborting the entire walk and exiting 2. A single permission-denied or transient read failure on one doc fails the whole gate instead of being reported and skipped.

**Suggested fix:** Log and continue (return nil) on per-file open errors, or accumulate them, rather than aborting the walk.

#### 623. [LOW] fmt.Errorf detection misses aliased fmt imports
`tools/check-fmt-errorf-wrap/main.go:190` — correctness — conf 0.60 — _low/info (unverified)_

isFmtErrorf requires the receiver ident to be literally 'fmt'. An aliased import (import f "fmt"; f.Errorf(...)) or dot-import bypasses the gate entirely since detection is syntactic, not type-resolved. Uncommon but a real evasion path for an AST gate.

**Suggested fix:** Resolve the fmt import name from the file's import specs, or document that aliased fmt imports are not covered.

#### 624. [LOW] ChangedSinceTag computed via per-module git diff but never read
`tools/release-planner/main.go:73` — performance — conf 0.90 — _verified_

m.ChangedSinceTag = gitHasChanges(m.PreviousTag, dir) runs one 'git diff --quiet ref..HEAD -- dir' subprocess for every tagged module (~103 modules), but the field is never consumed anywhere in the program. Pure dead computation that adds ~100 git invocations per run.

**Suggested fix:** Remove the ChangedSinceTag field, gitHasChanges, and the per-module loop body, or actually surface the result in output.

#### 625. [INFO] go.work.sum staleness heuristic can be permanently un-fixable
`cmd/kit-doctor/repochecks.go:276` — correctness — conf 0.50 — _reviewer-reported (unverified)_

goWorkSumLooksStale flags when go.work.sum is smaller than the largest go.sum, but `go work sync` does not guarantee that outcome (a module go.sum can legitimately contain more hashes than the workspace build list needs). In such workspaces the High finding reappears every interactive run after a successful fix, feeding the exit-1 problem at main.go:85. Also the comment claims a dry-run against a copy that the code never performs.

**Suggested fix:** Compare go.work.sum mtime/content against `go work sync` dry output, or downgrade to Warning; fix the misleading comment.

#### 626. [INFO] JSON output serializes Severity as bare int with no labels
`cmd/kit-doctor/rules/rules.go:58` — api-design — conf 0.85 — _reviewer-reported (unverified)_

Finding has no json tags except Fix, so -format=json emits {"Severity": 3, ...}. Consumers must hardcode the 0–3 enum mapping, and any future reordering of the Severity constants silently changes the meaning of archived JSON output. The text format uses readable labels; JSON does not.

**Suggested fix:** Add MarshalJSON emitting the String() label (additive, but note it changes the current JSON contract — gate or document).


### testing — integrationtest suites, kittest helpers

_10 findings — 8 low, 2 info_

#### 627. [LOW] Fixed 100ms sleep to allow ack completion is timing-flaky
`testing/integrationtest/amqpbackend/consumer_test.go:133` — testing — conf 0.60 — _low/info (unverified)_

TestConsumeOnce_AckOnSuccess closes 'done' inside the handler then sleeps a fixed 100ms before checking the queue is empty via ch.Get. Under CI load the ack may not have reached the broker in 100ms, producing a false 'queue not empty' failure. Other tests in the file correctly use require.Eventually for broker-side state.

**Suggested fix:** Replace the fixed sleep with require.Eventually polling ch.Get until the queue is empty.

#### 628. [LOW] Reconnect-then-unhealthy assertion races a fast reconnect
`testing/integrationtest/amqpbackend/reconnect_test.go:167` — testing — conf 0.50 — _low/info (unverified)_

TestConnection_Reconnect_HealthTransitions asserts conn.Healthy() becomes false within 5s after closing connections, but if the library reconnects faster than the 50ms poll observes the down state, Healthy() may already be true again and the require.Eventually times out. The unhealthy window is not guaranteed observable.

**Suggested fix:** Treat the intermediate unhealthy state as best-effort, or block reconnection (e.g. pause container) so the down window is deterministic.

#### 629. [LOW] doc.go claims ApplyTo/Scheduler test coverage that does not exist
`testing/integrationtest/cronpg/doc.go:1` — docs — conf 0.85 — _low/info (unverified)_

The package doc states tests cover "ApplyTo round-trip to a runtime/cron.Scheduler with a synthetic jobs map", but store_integration_test.go contains no ApplyTo or Scheduler test (grep confirms zero matches). A hostile reader trusting the doc would assume coverage that isn't there.

**Suggested fix:** Remove the ApplyTo/Scheduler claim from doc.go or add the described test.

#### 630. [LOW] Contradictory comment and redundant double-FlushDB in conformance factory
`testing/integrationtest/idempotencyredis/conformance_integration_test.go:22` — docs — conf 0.80 — _low/info (unverified)_

The comment says "FLUSHDB would be destructive against a shared Redis" immediately before calling client.FlushDB (line 24), which IS FLUSHDB. Also redisClient(t) (line 19) already registers a redistest.FlushDB cleanup, so this is a redundant second wipe of the shared container per subtest.

**Suggested fix:** Drop the inner FlushDB cleanup (redisClient already flushes) and fix the contradictory comment.

#### 631. [LOW] Data race: attempts read without mutex in failure-path t.Fatalf
`testing/integrationtest/kafkabackend/integration_test.go:262` — concurrency — conf 0.85 — _low/info (unverified)_

attempts (a plain int) is incremented under mu by the consumer goroutine (line 228) but read without mu in the ctx.Done() t.Fatalf at line 262. On timeout the consumer goroutine is still running, so -race flags a genuine data race — though only on an already-failing run.

**Suggested fix:** Make attempts an atomic.Int32, or snapshot it under mu before the Fatalf.

#### 632. [LOW] wg.Done() inside handler panics if handler is invoked more than once
`testing/integrationtest/redisbackend/integration_test.go:73` — testing — conf 0.50 — _low/info (unverified)_

wg.Add(1) with wg.Done() called inside the consume handler assumes exactly one delivery. With WithoutRetry:true and a clean return this holds, but any redelivery/duplicate invocation calls wg.Done() twice → panic: negative WaitGroup counter, masking the real condition.

**Suggested fix:** Guard with sync.Once around wg.Done()+cancel, or use a buffered done channel signalled once.

#### 633. [LOW] redisClient omits FlushDB cleanup against shared container, unlike all siblings
`testing/integrationtest/rediscache/integration_test.go:26` — testing — conf 0.60 — _low/info (unverified)_

redistest.Start uses sync.Once so the Redis container/keyspace is shared across the whole test process. Every other redis integration test in this unit registers t.Cleanup(redistest.FlushDB); this file does not. redistest's own doc warns missing flush causes order-dependence and -shuffle=on failures from leftover keys.

**Suggested fix:** Add t.Cleanup(func(){ redistest.FlushDB(t) }) in redisClient for consistency and shuffle-safety.

#### 634. [LOW] json.Unmarshal error ignored in assertion path
`testing/integrationtest/redisstream/integration_test.go:148` — error-handling — conf 0.70 — _low/info (unverified)_

json.Unmarshal(received.Payload, &payload) discards its error. If decoding ever fails, payload stays the zero map and the subsequent assert.Equal("hello", payload["data"]) fails with a confusing message instead of surfacing the real unmarshal error.

**Suggested fix:** require.NoError(t, json.Unmarshal(received.Payload, &payload)) before asserting on payload.

#### 635. [INFO] Comment claims newTestDB sets a generous pool, but the caller does
`testing/integrationtest/lockpgadvisory/conformance_integration_test.go:21` — docs — conf 0.80 — _low/info (unverified)_

The comment ends "newTestDB allocates a generous pool", yet newTestDB (in pgadvisory_test.go) never calls SetMaxOpenConns; the conformance test itself sets db.SetMaxOpenConns(32) at line 25. Minor inaccuracy that could mislead someone editing newTestDB.

**Suggested fix:** Reword the comment to credit the SetMaxOpenConns(32) call in this factory.

#### 636. [INFO] Comment references a non-existent TestMain layer
`testing/integrationtest/natsbackend/conformance_integration_test.go:22` — docs — conf 0.75 — _low/info (unverified)_

The comment claims the stream is set up "ONCE at the TestMain layer", but there is no TestMain; setup happens inline in TestNATSPublisher_Conformance. Misleading for maintainers reasoning about lifecycle/ordering.

**Suggested fix:** Reword to say setup happens once at the top of the conformance test function.


### examples — sample services

_8 findings — 3 medium, 5 low_

#### 637. [MEDIUM] run() returns before srv.Shutdown finishes draining in-flight requests
`examples/agentic-service/internal/app/app.go:162` — concurrency — conf 0.82 — _reviewer-reported (unverified)_

The shutdown goroutine calls srv.Shutdown(5s) on ctx.Done(), but ListenAndServe returns http.ErrServerClosed immediately when Shutdown begins, and run() then returns nil without synchronizing with the goroutine. main() exits while in-flight requests are still draining, truncating them — the 'graceful shutdown' is not graceful. This is a reference example whose pattern will be copied; the other two examples avoid it by using app.Builder/lifecycle.Runner. Also, if ListenAndServe fails at bind time the goroutine leaks until process exit.

**Suggested fix:** After ListenAndServe returns ErrServerClosed, wait on a done channel closed by the shutdown goroutine after srv.Shutdown returns, and propagate its error.

#### 638. [MEDIUM] Idempotency silently disabled for keys the store rejects (spaces, control chars, over-length)
`examples/saga-coordinator/internal/app/app.go:283` — correctness — conf 0.70 — _reviewer-reported (unverified)_

handleOrder only checks idemKey != "", but idempotency.ValidateKey rejects keys with whitespace/control chars or >MaxKeyLen. For such keys, store.Get returns an error which lookupCache swallows (treated as miss, line 252) and TryLock fails so storeCache silently skips caching (line 270). Result: the saga re-executes on every retry — the exact double-charge scenario this example exists to prevent — with no error or log.

**Suggested fix:** Validate the header with idem.ValidateKey in handleOrder and return 400 on failure; at minimum log swallowed store errors.

#### 639. [MEDIUM] keyedMutex.holders grows unboundedly, keyed by attacker-controlled Idempotency-Key header
`examples/saga-coordinator/internal/app/app.go:348` — resource-leak — conf 0.75 — _reviewer-reported (unverified)_

keyedMutex.Lock creates and permanently retains one *sync.Mutex per distinct Idempotency-Key; nothing ever deletes map entries. The key comes straight from the client header, so unique keys per request grow the map without bound (memory DoS over time). The completed sync.Map (line 130) similarly retains every OrderState forever. The MemoryStore TTLs its entries, but these two maps do not — and the example exists to teach the production pattern.

**Suggested fix:** Reference-count and delete holder mutexes on release (or use singleflight-style keyed locking); bound or TTL the completed map.

#### 640. [LOW] require assertions invoked from spawned goroutines in bulkhead test
`examples/api-gateway/internal/app/app_test.go:166` — testing — conf 0.70 — _reviewer-reported (unverified)_

TestGateway_BulkheadFullReturns503's send() calls require.NoError (which calls t.FailNow) and is executed from worker goroutines (lines 172-177). FailNow from a non-test goroutine only exits that goroutine; the failure is recorded but the test keeps running, and if the deferred resp.Body.Close path is skipped the drain logic still relies on wg.Done via defer — behavior on failure is confusing and can mask the real error.

**Suggested fix:** Have goroutine send() return (int, error) and assert on the main test goroutine, or use t.Error-based assertions in goroutines.

#### 641. [LOW] JWKS test relies on a single Read returning the whole body; readAll matches EOF by string
`examples/realtime-broadcast/internal/app/app_test.go:171` — testing — conf 0.75 — _reviewer-reported (unverified)_

TestJWKSEndpoint_ExposesPublicKey does one resp.Body.Read into a 4096-byte buffer and assumes it captures the full JWKS — a short read (legal for any io.Reader, especially HTTP chunked bodies) makes ParseKeySet fail flakily. The local readAll helper (line 232, duplicated in webhook-receiver app_test.go:189) compares err.Error() == "EOF" instead of errors.Is(err, io.EOF).

**Suggested fix:** Use io.ReadAll in both test files; delete the hand-rolled readAll helpers.

#### 642. [LOW] Four files fail gofmt
`examples/saga-coordinator/internal/app/app.go:135` — build-ci — conf 0.85 — _reviewer-reported (unverified)_

gofmt -l flags examples/{realtime-broadcast,saga-coordinator,webhook-receiver}/internal/app/app.go and grpcx/client/client_test.go: misordered imports (app/v2 before app/http/v2 in all three example app.go files), stepBundle field alignment (saga app.go:136-138), and fakeHealthSrv struct layout (client_test.go:40). Indicates these files bypassed the formatting gate.

**Suggested fix:** Run gofmt -w on the four files; ensure make lint covers examples and test files.

#### 643. [LOW] Idempotency store calls use context.Background() instead of request context
`examples/saga-coordinator/internal/app/app.go:251` — correctness — conf 0.75 — _reviewer-reported (unverified)_

lookupCache (line 251) and storeCache (lines 269, 273) pass context.Background() even though runSaga has the request ctx in scope. Harmless for MemoryStore, but the surrounding comments tell readers to swap in pgstore/redisstore, where dropping cancellation/deadlines on store I/O is exactly the anti-pattern the kit elsewhere forbids.

**Suggested fix:** Thread the runSaga ctx into lookupCache/storeCache.

#### 644. [LOW] Test-only failOnce type shipped in production file with wrong comment
`examples/saga-coordinator/internal/app/app.go:388` — consistency — conf 0.85 — _reviewer-reported (unverified)_

failOnce is only referenced from app_test.go but lives in app.go, so it compiles into the binary. Its comment is also wrong twice: it names a non-existent "failAtStep" and claims it "fails on the Nth call", while fail() unconditionally returns an error on every call.

**Suggested fix:** Move failOnce into app_test.go and fix the comment.


### build+ci — Makefile, go.work, GitHub Actions workflows, release shell scripts, module/version hygiene

_27 findings — 1 high, 7 medium, 15 low, 4 info_

#### 645. [HIGH] go mod tidy failure swallowed with an echo; release proceeds to commit and tag a module with wrong requires
`tools/release-version.sh:78` — error-handling — conf 0.85 — _verified_

`go mod tidy ... || echo "  (tidy issue in $dir)"` neither sets a failure flag nor aborts. If tidy fails (network, unresolved tag, sum mismatch) the loop continues, the commit is made, and the level's tags are created and pushed unconditionally. A tidy failure means the module's go.sum/require set is wrong, but the release driver tags it anyway and moves to the next level — producing a bad published release that the operator only sees via a buried log line.

**Suggested fix:** Capture failures and abort before tagging, e.g. set fail=1 on tidy error and `exit 1` after the loop (matches rehearse which lets tidy fail hard under set -e).

#### 646. [MEDIUM] Dashboard metric/label drift checks exist but run in no workflow
`.github/workflows/dashboards.yml:27` — build-ci — conf 0.85 — _verified_

tools/check-dashboard-metrics.sh and check-dashboard-labels.sh (and make check-dashboards) detect metric-rename and label drift between Go code and dashboards, but grep shows none are invoked by any workflow nor by make ci/release-candidate. dashboards.yml only validates JSON/YAML syntax. A metric rename that breaks a dashboard query passes CI undetected.

**Suggested fix:** Add make check-dashboard-metrics and check-dashboard-labels to dashboards.yml (or make ci).

#### 647. [MEDIUM] promtool downloaded without checksum verification
`.github/workflows/dashboards.yml:40` — security — conf 0.80 — _verified_

The Prometheus tarball is fetched over HTTPS, extracted, and the resulting binary is sudo mv'd into /usr/local/bin with no SHA256/signature check (only the version is pinned). A compromised release asset or registry MITM would execute an attacker-controlled binary as root in CI, poisoning the dashboards gate.

**Suggested fix:** Pin and verify the published SHA256 of the tarball (sha256sum -c) before extracting, or vendor promtool.

#### 648. [MEDIUM] Heaviest workflow lacks timeout-minutes and concurrency control
`.github/workflows/release.yml:27` — build-ci — conf 0.85 — _verified_

The readiness job runs make release-candidate (vulncheck + integration + coverage + kit-doctor + rehearsal) yet sets no timeout-minutes, so a hang inherits the 6-hour default (ci.yml/supply-chain.yml all cap theirs). It also has no concurrency block, so synchronize pushes to a release-candidate-labeled PR or to release/** spawn overlapping multi-hour runs.

**Suggested fix:** Add timeout-minutes (e.g. 45) and a concurrency group with cancel-in-progress like ci.yml.

#### 649. [MEDIUM] pgx version skew: apikey/postgres pins v5.9.2 while 10 sibling Postgres adapters use v5.10.0
`data/apikey/postgres/go.mod:8` — consistency — conf 0.92 — _verified_

data/apikey/postgres directly requires github.com/jackc/pgx/v5 v5.9.2, but app/postgres, data/actionlog/postgres, data/approval/postgres, infra/sqldb/pgx, infra/outbox/postgres, infra/sqldb/readreplica, observability/auditlog/postgres and others all require v5.10.0. store.go truly imports pgx. go.work MVS unifies everything to v5.10.0 locally, masking the skew; a downstream consumer importing only data/apikey/postgres/v2 resolves the older v5.9.2 (missing the minor's fixes). It is the sole skewed dep among 283 externals. Neither check-direct-dependency-allowlist.sh nor check-heavy-dependency-boundaries.sh compares versions, so no gate catches this.

**Suggested fix:** Bump data/apikey/postgres to pgx v5.10.0 (GOWORK=off go mod tidy), commit go.sum, and consider a version-skew gate over direct deps.

#### 650. [MEDIUM] Helper tool's 280-line test suite is never executed by any CI gate
`tools/check-dashboard-labels/main_test.go:1` — testing — conf 0.90 — _verified_

tools/check-dashboard-labels is intentionally outside go.work (run via GOWORK=off go run main.go). But make test / test-race iterate WORKSPACE_MODULES (derived solely from go.work, Makefile:13), so this module is skipped, and check-dashboard-labels.sh only does `go run main.go` (never `go test`). The result: 280 lines of tests for the label-validation logic — the logic that gates dashboard correctness — never run anywhere. ci.yml just calls `make ci`. The same gap silently covers check-bench-regression, check-doc-rot, check-fmt-errorf-wrap source.

**Suggested fix:** Add a CI step / Make target that runs `cd tools/<mod> && GOWORK=off go test ./...` (and go vet) for each tools/check-* module.

#### 651. [MEDIUM] Rehearsal verify regex is unanchored, so v2.0.10 satisfies a v2.0.1 check and one matching line passes the whole gate
`tools/rehearse-v2-release.sh:220` — testing — conf 0.70 — _verified_

version_re escapes dots but the grep has no end anchor: `grep -E "...v2 v2\.0\.1"` also matches v2.0.10/v2.0.1-rc. Worse, `go list -m all | grep ...` (line 220) and the go.sum grep (line 221) pass as soon as ONE line matches; they do not assert that EVERY internal module resolved to $VERSION. A module stuck at the previous version would not fail the rehearsal, defeating the purpose of the end-state verification.

**Suggested fix:** Anchor the version (`${version_re}$`) and assert no internal module is at any other version (e.g. grep -v the expected version returns empty).

#### 652. [MEDIUM] Combined git add of go.mod+go.sum fails atomically when go.sum absent, silently dropping the require bump
`tools/release-version.sh:84` — error-handling — conf 0.90 — _verified_

git add of multiple pathspecs is atomic: if a module has no go.sum (e.g. internal-only deps that tidy didn't materialize, or zero deps), the missing-pathspec error makes git stage NEITHER file. The 2>/dev/null||true swallows it, so the bumped go.mod is excluded from the release commit, yet the module's tag is still created/pushed at HEAD pointing at the un-bumped go.mod. Downstream consumers then resolve a stale internal require. rehearse-v2-release.sh (lines 163-164) does this correctly with two separate adds.

**Suggested fix:** Split into two adds like rehearse: `git add -A "$dir/go.mod"` then `git add -A "$dir/go.sum" 2>/dev/null || true`.

#### 653. [LOW] Critical release/governance files not covered by CODEOWNERS
`.github/CODEOWNERS:1` — security — conf 0.90 — _verified_

tools/release-version.sh — the actual script that pushes commits to main (line 89) and tags atomically (lines 99,105-106) — is NOT owned, while non-mutating helpers like plan-module-release.sh are. CODEOWNERS also does not protect itself, .github/dependabot.yml, go.work, or the Makefile. An attacker/contributor could edit the tag-pushing script or strip security ownership without @bds421/security review.

**Suggested fix:** Add rules for tools/release-version.sh, /.github/CODEOWNERS, /.github/dependabot.yml, go.work, and Makefile to @bds421/security.

#### 654. [LOW] CODEOWNERS references missing docs/audit/SUPPLY_CHAIN.md
`.github/CODEOWNERS:5` — build-ci — conf 0.95 — _low/info (unverified)_

Line 5 assigns ownership of docs/audit/SUPPLY_CHAIN.md, but docs/audit/ contains only README.md, THREAT_MODEL.md, and dependency-allowlist.txt. The file does not exist, so the rule never matches and the supply-chain doc the comment claims to protect is unguarded.

**Suggested fix:** Create the file or remove the rule; verify the intended path (README.md vs SUPPLY_CHAIN.md).

#### 655. [LOW] CODEOWNERS owns docs/release/ which .gitignore excludes from the repo
`.github/CODEOWNERS:8` — build-ci — conf 0.90 — _low/info (unverified)_

.gitignore line 43 ignores docs/release/ (confirmed via git check-ignore), so no file under that path can ever be committed or reviewed. The CODEOWNERS rule on line 8 is therefore permanently inert.

**Suggested fix:** Remove the docs/release/ ownership rule, or un-ignore the directory if release docs are meant to be tracked and reviewed.

#### 656. [LOW] CODEOWNERS references non-existent script tools/drop-internal-replaces.sh
`.github/CODEOWNERS:14` — build-ci — conf 0.97 — _verified_

Line 14 assigns ownership of tools/drop-internal-replaces.sh, but no such file exists on disk (verified via ls and repo-wide grep; only this CODEOWNERS line mentions it). GitHub silently ignores ownership rules for non-existent paths, so this is a dead rule that gives false confidence the replace-drop tooling is review-gated.

**Suggested fix:** Remove the stale line or restore the script. If replace-drop is now part of release-version.sh/rehearse, point ownership there.

#### 657. [LOW] check-release-team target missing from .PHONY
`Makefile:175` — build-ci — conf 0.92 — _low/info (unverified)_

Every other make target is listed in the .PHONY declaration on line 1, but check-release-team (defined at line 175) is omitted. If a file or directory named check-release-team ever appears in the repo root, make would treat the target as up-to-date and skip it, silently bypassing the release-team preflight.

**Suggested fix:** Add check-release-team to the .PHONY list on line 1.

#### 658. [LOW] release-bin -X main.commit/main.date ldflags are silent no-ops for kit-doctor
`Makefile:178` — build-ci — conf 0.85 — _low/info (unverified)_

release-bin injects -X main.commit and -X main.date, but cmd/kit-doctor's main package declares no commit/date package-level vars (grep found none across its .go files). Go's linker silently ignores -X for missing symbols, so the build-provenance the comment (lines 178-183) advertises is never embedded; only -trimpath/-buildid reproducibility actually holds.

**Suggested fix:** Add `var commit, date string` to cmd/kit-doctor/main.go, or drop the misleading -X flags.

#### 659. [LOW] Dynamic awk regex treats '.' and '/' in the internal prefix as metacharacters
`tools/check-direct-dependency-allowlist.sh:73` — correctness — conf 0.55 — _low/info (unverified)_

`$1 !~ "^" internal` builds a regex from the literal string `github.com/bds421/rho-kit/`; the dots are ERE wildcards, so a hypothetical module like `githubXcom/bds421/rho-kit/...` would be wrongly classified as internal and skipped from allowlist enforcement. Not exploitable today but a latent false-negative in a trust-boundary gate.

**Suggested fix:** Use index()/prefix string comparison instead of regex: `if (index($1, internal) != 1) print`.

#### 660. [LOW] Boundary allow-list references many per-adapter integrationtest go.mod paths that do not exist
`tools/check-heavy-dependency-boundaries.sh:101` — build-ci — conf 0.78 — _low/info (unverified)_

allowed_for_boundary_dep permits paths like data/queue/riverqueue/integrationtest/go.mod, infra/messaging/amqpbackend/integrationtest/go.mod, infra/messaging/natsbackend/integrationtest/go.mod, httpx/websocket/integrationtest/go.mod, etc. None of these modules exist on disk (only testing/integrationtest/go.mod does). The patterns are harmless permissive allowances today, but they are dead config that misleads maintainers into thinking per-adapter integration modules are an established pattern, and would silently auto-approve heavy deps if such a path were ever created without review.

**Suggested fix:** Prune the non-existent integrationtest paths or replace them with the actual */integrationtest glob already covered by testing/integrationtest.

#### 661. [LOW] license whitespace strip only removes a single leading space, not the full prefix
`tools/check-licenses.sh:123` — correctness — conf 0.60 — _low/info (unverified)_

`license="${license##[[:space:]]}"` uses a single-character class (not `*[[:space:]]`), so it strips at most one leading whitespace char. With go-licenses' comma-delimited CSV and IFS=, the license field has no leading space, so this is effectively dead code — but if the CSV format ever changes, a leading-space license string would silently miss the allowlist exact-match and fail the gate spuriously.

**Suggested fix:** Use `${license##*[[:space:]]}` for trailing-strip semantics, or trim with a robust pattern; or document the dependence on go-licenses CSV having no spaces.

#### 662. [LOW] rg and find code paths for v0.0.0 detection scan different file sets
`tools/check-publishable.sh:36` — consistency — conf 0.50 — _low/info (unverified)_

The rg branch globs go.mod and excludes .claude/dist but not .git, and (being rg) honors .gitignore; the find branch prunes .git/.claude/dist but ignores .gitignore. So whether a gitignored or .git-internal go.mod is considered depends on which tool is installed, making the gate's result tool-dependent rather than deterministic across contributor/CI environments.

**Suggested fix:** Make both branches scan the identical set (e.g. drive both from `find_go_mods`, or pass `--no-ignore --hidden` plus matching globs to rg).

#### 663. [LOW] Tidy gate enumerates from go.work, so the 4 tools/check-* modules are never tidy-checked
`tools/check-tidy.sh:19` — build-ci — conf 0.82 — _low/info (unverified)_

check-tidy.sh derives its module list from go.work's use block (line 19), and Makefile lint/vet/build/tidy do the same via WORKSPACE_MODULES. The four tools/check-* go.mod files (tracked, on disk, stdlib-only today) are excluded from tidy/lint/vet/build entirely. They are clean now, but any future dep added to a tool would never be tidy-verified, reintroducing exactly the missing-require pseudo-version bug class this script was written to prevent.

**Suggested fix:** Extend check-tidy.sh (and lint/vet) to also walk on-disk go.mod files outside go.work, e.g. via git ls-files '*go.mod' with GOWORK=off.

#### 664. [LOW] exec >(tee log) can truncate the rehearsal log on early exit and is not reaped
`tools/rehearse-v2-release.sh:54` — error-handling — conf 0.45 — _low/info (unverified)_

Redirecting all output through a process-substitution `tee` means the script can exit (and the EXIT trap run) before tee flushes its final buffer, so the canonical checked-in rehearsal log can be missing the last lines / the 'Rehearsal passed.' marker even on success. The tee process is also not waited on.

**Suggested fix:** Either tee a temp file and copy it on success, or add a trailing `sync`/explicit wait, to guarantee the log captures the full run.

#### 665. [LOW] internal-dep grep over-matches the module's own path, indirect requires, and replace targets
`tools/release-version.sh:74` — correctness — conf 0.75 — _verified_

`grep -hoE 'github\.com/bds421/rho-kit/[^[:space:]]+' "$dir/go.mod"` matches the `module` line, `// indirect` requires, and any replace LHS/RHS — not just direct requires. Each match is then `go mod edit -require`'d to $VERSION, creating a transient self-require and bumping indirect pins. It is only saved by the subsequent `go mod tidy` cleaning it up — but that tidy's failure is swallowed (line 78), so the over-match is not robust. Same code in rehearse line 154.

**Suggested fix:** Use `go mod edit -json` / `go list -m -f` to enumerate actual direct require module paths instead of grepping the raw file.

#### 666. [LOW] git push origin main without --force-with-lease/upstream check can clobber or fail confusingly mid-release
`tools/release-version.sh:89` — correctness — conf 0.55 — _low/info (unverified)_

Each level pushes main directly. If origin/main advanced concurrently the push is rejected and set -e aborts after earlier levels' tags are already on origin, leaving a half-released state with no rollback. The script documents disabling branch protection but does not fetch/verify main is at the expected commit before the run.

**Suggested fix:** Add a preflight `git fetch && git merge-base --is-ancestor origin/main HEAD` check, and consider --atomic for the branch+tag sequence per level.

#### 667. [LOW] grep in command substitution aborts the whole release under set -e if a level has zero tags
`tools/release-version.sh:98` — correctness — conf 0.80 — _verified_

`tags_args=$(echo "$level_tags" | grep . | tr '\n' ' ')` — when $level_tags is empty, grep exits 1; an assignment from a failing command substitution triggers set -e (verified on bash 5 and macOS bash 3.2). The release aborts mid-run, after earlier levels' tags were already pushed to origin, leaving a partially-tagged release. Same pattern at line 64 (`grep -c .`). Only safe because every plan level currently has ≥1 module/tag.

**Suggested fix:** Append `|| true` to the grep pipelines, or guard for empty levels explicitly before tagging/counting.

#### 668. [INFO] Build-cache key includes github.sha, growing the cache unboundedly
`.github/workflows/ci.yml:56` — build-ci — conf 0.60 — _low/info (unverified)_

key embeds ${{ github.sha }}, so every commit writes a fresh ~107-module build cache entry that is never reused (only restore-keys fall back). This relies on GitHub's 10GB LRU eviction and can thrash large caches; a PR can also populate a base-branch build cache that a later main run restores via restore-keys (low-likelihood Go build-cache poisoning).

**Suggested fix:** Drop the github.sha suffix or scope cache restore to trusted refs; rely on go.sum hash plus a date/run salt.

#### 669. [INFO] govulncheck severity classifier defaults to MEDIUM and can only escalate
`.github/workflows/supply-chain.yml:177` — build-ci — conf 0.55 — _low/info (unverified)_

The Python parser defaults every called finding to MEDIUM (line 177) and only ever raises severity from CVSS substrings or database_specific; it never assigns LOW. With default FAIL_LEVEL=HIGH this is fine, but lowering VULN_FAIL_LEVEL to LOW/MEDIUM would treat all called vulns as >=MEDIUM. Mitigated because the job is continue-on-error (inform-only).

**Suggested fix:** Map findings without severity metadata to LOW, or document that absent-severity findings are treated as MEDIUM.

#### 670. [INFO] .gitignore lists cmd/kit-bench-gate binary but the command does not exist
`.gitignore:7` — consistency — conf 0.85 — _low/info (unverified)_

Lines 7 and 16 ignore cmd/kit-bench-gate/kit-bench-gate and /kit-bench-gate, but there is no cmd/kit-bench-gate/ directory (only kit-catalog/doctor/migrate/new/verify exist) and no other reference in the repo. Stale ignore entry from a removed/renamed command.

**Suggested fix:** Remove the two kit-bench-gate lines from .gitignore.

#### 671. [INFO] go.work.sum carries vestigial full-module hashes for versions no module requires
`go.work.sum:1` — build-ci — conf 0.60 — _low/info (unverified)_

go.work.sum holds full h1: hashes for versions not required by any current module: go.uber.org/zap v1.27.1 (all modules require v1.28.0), cloud.google.com/go/compute v1.49.1/v1.54.0/v1.64.0, plus only a stale jackc/pgx/v5 v5.5.4/go.mod line and no entry for the v5.9.2/v5.10.0 versions actually built (those live correctly in per-module go.sum). Go tolerates extra entries so this is not a correctness bug, but the file is not the product of a clean `go work sync`.

**Suggested fix:** Run `go work sync` and commit, so go.work.sum reflects only the current build graph and stays a reliable supply-chain artifact.


### sql+templates+dashboards — SQL migrations, kit-new templates, Grafana/Prometheus dashboards

_25 findings — 1 critical, 2 high, 9 medium, 9 low, 4 info_

#### 672. [CRITICAL] Optimistic-concurrency check on updated_at breaks every multi-step saga under Postgres
`data/saga/pgstore/store.go:126` — correctness — conf 0.83 — _verified_

putUpdateOptimistic gates on `updated_at = $7`. The saga.StateStore.Put contract passes Instance by value and cannot return the new updated_at; DurableExecutor.executeInstance Gets once (executor.go:248) then calls Put repeatedly (261,303,309) without re-Getting. The first Put advances the DB updated_at to a new now(); the next Put still sends the stale value, matches 0 rows, and returns ErrConcurrentUpdate, which the executor turns into a fatal error. The memory store ignores UpdatedAt so this is invisible in unit tests, and saga/pgstore has no integration test (store_test.go:27).

**Suggested fix:** Make pgstore self-refresh: re-read its own row (or use RETURNING updated_at internally and treat a same-process advance as success), or have the executor re-Get after each Put. Additive fix; no interface break required.

#### 673. [HIGH] Docs claim executor re-reads on ErrConcurrentUpdate, but it never handles that error
`data/saga/pgstore/doc.go:22` — docs — conf 0.80 — _verified_

doc.go (and AGENTS.md:44) state the executor's loop treats ErrConcurrentUpdate as retryable and re-reads. grep shows runtime/saga/executor.go contains no reference to ErrConcurrentUpdate and no errors.Is on it — and it cannot import data/saga/pgstore without a layering inversion. Put errors propagate as fatal `saga: persist ... %w`. The retry/re-read behavior the design depends on does not exist.

**Suggested fix:** Either implement the documented retry (define a shared sentinel in runtime/saga that pgstore returns and the executor checks) or correct the docs to state Put failures are fatal.

#### 674. [HIGH] redis/storage dashboards select on `instance` label that collides with Prometheus reserved target label
`observability/dashboards/grafana/redis.json:29` — correctness — conf 0.82 — _verified_

infra/redis/metrics.go and infra/storage/*/metrics.go register a user-controlled `instance` metric label. `instance` is also Prometheus's reserved scrape target label. Under default honor_labels:false, Prometheus renames the exposed label to exported_instance, so every `instance=~"$instance"` selector and `label_values(...,instance)` variable here resolves the scrape target address, not the backend name — panels show wrong/empty series.

**Suggested fix:** Rename the Go metric label to redis_instance/storage_instance (BREAKING — v3 candidate), or document required honor_labels/relabel config and update dashboards to query exported_instance.

#### 675. [MEDIUM] Generated `lint` target runs `go fmt` (mutates, never fails) and not the repo's golangci-lint
`cmd/kit-new/templates/Makefile.tmpl:9` — build-ci — conf 0.75 — _verified_

The repo's own `make lint` runs golangci-lint and fails on findings. The generated lint target runs `go fmt ./...` + `go vet ./...`; `go fmt` rewrites files and exits 0 even when it changes them, so the CI lint step can never fail on formatting/style drift, giving false assurance. AGENTS.md.tmpl claims to "Mirror the rho-kit AGENTS.md conventions" but the lint toolchain is not mirrored.

**Suggested fix:** Make lint check-only (`gofmt -l` failing on output) or invoke golangci-lint as the repo does; keep formatting in a separate `fmt` target.

#### 676. [MEDIUM] Generated README tells users to run `kit-doctor ./...`, which errors
`cmd/kit-new/templates/README.md.tmpl:37` — docs — conf 0.85 — _verified_

kit-doctor takes a filesystem PATH and runs filepath.WalkDir(root) (cmd/kit-doctor/engine.go:25). `./...` is a Go package pattern, not a path; WalkDir on the literal `./...` directory fails and kit-doctor exits 2 (bad path). The correct invocation, used by the Makefile, is `kit-doctor .`. A user copy-pasting the README command gets an error.

**Suggested fix:** Change README.md.tmpl line 37 to `kit-doctor .` (matching the doctor Makefile target).

#### 677. [MEDIUM] Generated CI workflow has no top-level permissions block (no least-privilege)
`cmd/kit-new/templates/ci.yml.tmpl:8` — security — conf 0.80 — _verified_

The repo's own .github/workflows/ci.yml declares a `permissions:` block (line 19) to drop the default read-write GITHUB_TOKEN. The generated ci.yml omits permissions entirely, so a scaffold marketed for "secure defaults" ships a workflow running with the repo's default (often write) token scope — a supply-chain footgun if any step is compromised.

**Suggested fix:** Emit `permissions:\n  contents: read` at the workflow top level to match the repo's hardened CI.

#### 678. [MEDIUM] go.mod require block omits app/tenant/v2 that wire.go directly imports
`cmd/kit-new/templates/go.mod.tmpl:10` — build-ci — conf 0.95 — _verified_

wire.go.tmpl's Tenant branch imports kittenant "github.com/bds421/rho-kit/app/tenant/v2" (line 20), but the go.mod.tmpl Tenant require block (lines 10-15) lists app/redis/v2, data/*, infra/redis/v2 — never app/tenant/v2. With -rho-version set, `go build` without tidy fails (no required module provides package) and the pin is incomplete. The build test masks this because it runs `go mod tidy`.

**Suggested fix:** Add `github.com/bds421/rho-kit/app/tenant/v2 {{.RhoVersion}}` to the {{if .Tenant}} block, and assert it in TestScaffold_TenantFlag_WiresTenantWrappers.

#### 679. [MEDIUM] main.go comment claims empty version falls back to vcs build info; it does not
`cmd/kit-new/templates/main.go.tmpl:17` — docs — conf 0.80 — _verified_

The comment says empty version "falls back to the embedded build info (vcs.revision + vcs.modified)". The actual sink, health.ResolveVersion (observability/health/health.go:178), only checks the APP_VERSION env var and otherwise returns the value verbatim — there is no debug.ReadBuildInfo / vcs.revision logic anywhere in app/ or observability/health/. With `go run` and no ldflags/APP_VERSION, the version field is empty, not a pseudo-version.

**Suggested fix:** Rewrite the comment to state the real fallback (APP_VERSION env), or actually wire debug.ReadBuildInfo if the documented behavior is desired.

#### 680. [MEDIUM] Build-time version reaches logs but not the Builder's /version probe
`cmd/kit-new/templates/wire.go.tmpl:166` — correctness — conf 0.70 — _verified_

main.go passes the ldflags-injected `version` to app.Main (used only for log lines), but Run() calls kitapp.New(name, "", cfg) with a hardcoded empty version. The Builder uses its own version for the internal /version + health endpoint (app/builder.go:569,605). So a service built with -ldflags -X main.version=vX reports vX in logs but the empty/APP_VERSION value on /version, confusing deploy verification.

**Suggested fix:** Thread version into Run: change main.go to call svcapp.Run(logger, version) and pass it to kitapp.New (additive signature change).

#### 681. [MEDIUM] FetchPending partial index omits next_retry_at and id, leaving backoff rows in the scan
`infra/outbox/postgres/migrations/20260514000001_create_outbox_entries.sql:36` — performance — conf 0.60 — _verified_

FetchPending (store.go:130-138) filters `status='pending' AND (next_retry_at IS NULL OR next_retry_at <= NOW()) ORDER BY created_at, id`. The partial index idx_outbox_pending_ready is only on (created_at) WHERE status='pending'. When many pending rows are deferred via exponential backoff (IncrementAttempts sets next_retry_at), Postgres still reads them in created_at order and discards each via heap recheck, and re-sorts by id within ties — O(deferred) work per claim under retry storms.

**Suggested fix:** Index (next_retry_at, created_at, id) WHERE status='pending' so deferred rows are skipped by the index and the ORDER BY is satisfied directly.

#### 682. [MEDIUM] Idle-close spike ratio fires spuriously when baseline is zero
`observability/dashboards/prometheus/alerts-coordination.yaml:132` — correctness — conf 0.70 — _verified_

RhoKitGRPCStreamIdleClosesSpike computes rate(idle_closed[10m]) / clamp_min(rate(idle_closed[10m] offset 1h), 0.0001) > 5. When the 1h-ago baseline is 0 (common: low traffic, fresh deploy, off-hours), the denominator becomes 0.0001, so any current rate >=0.0005/s yields a ratio >5 and pages a 'warn'. The clamp_min divide-by-zero guard turns into a false-positive generator.

**Suggested fix:** Add an absolute floor on the numerator, e.g. `and rate(grpc_server_streams_idle_closed_total[10m]) > 0.05`, so the ratio only fires above a meaningful current rate.

#### 683. [MEDIUM] increase() applied to a gauge metric (redis_pool_timeouts)
`observability/dashboards/prometheus/alerts-saturation.yaml:100` — correctness — conf 0.72 — _verified_

infra/redis/metrics.go registers connectionPoolTimeouts as a GaugeVec set to the cumulative go-redis PoolStats().Timeouts snapshot. The alert uses increase(redis_pool_timeouts[5m]); increase() is a counter function that does reset extrapolation. On client recreation the gauge drops to a low value and increase() treats it as a counter reset, producing phantom spikes. The rule comment itself admits it's a 'counter as a gauge'.

**Suggested fix:** Expose timeouts as a Counter via delta tracking (like sqldb WaitCount) and keep increase(); or change the alert to delta(redis_pool_timeouts[5m]).

#### 684. [LOW] doctor/sqlc/migrate targets use @latest, making CI non-reproducible
`cmd/kit-new/templates/Makefile.tmpl:17` — build-ci — conf 0.60 — _low/info (unverified)_

doctor (line 17) runs kit-doctor/v2@latest, and sqlc (22) / migrate (27) also use @latest. The `doctor` step runs in CI and gates the build (default -strict=high exits 1); pinning to @latest means a new kit-doctor release can silently change findings and break a previously-green CI, and requires network on every run.

**Suggested fix:** Pin kit-doctor (and sqlc/migrate) to a tagged version, e.g. wire {{.RhoVersion}} or a dedicated tool-version field, instead of @latest.

#### 685. [LOW] Generated CI pins stale action major versions vs the repo's own CI
`cmd/kit-new/templates/ci.yml.tmpl:11` — build-ci — conf 0.80 — _low/info (unverified)_

The generated workflow uses actions/checkout@v4 (line 11) and actions/setup-go@v5 (line 12), while the repo's own .github/workflows/ci.yml uses checkout@v6 and setup-go@v6. The scaffold ships an older toolchain than the kit it's generated from.

**Suggested fix:** Bump to actions/checkout@v6 and actions/setup-go@v6 to match the repo and keep them updated when the repo bumps.

#### 686. [LOW] sqlc.yaml comment references non-existent app.Builder.WithPostgres
`cmd/kit-new/templates/sqlc.yaml.tmpl:19` — docs — conf 0.85 — _low/info (unverified)_

The comment reads "pgx/v5 driver — matches what app.Builder.WithPostgres opens." There is no WithPostgres method on Builder anywhere in the codebase; the v2 Postgres path is kitpostgres.Module(pgxbackend.Config{...}). Stale comment left over from a pre-v2 API.

**Suggested fix:** Update the comment to reference app/postgres.Module / pgxbackend, the actual v2 wiring used in wire.go.tmpl.

#### 687. [LOW] Generated wire.go import block is not gofmt-clean (out-of-order paths)
`cmd/kit-new/templates/wire.go.tmpl:16` — consistency — conf 0.70 — _low/info (unverified)_

Within the single contiguous import group, app/v2 (line 16) precedes app/ratelimit/v2 (line 17); gofmt sorts by import path, where app/ratelimit/v2 < app/v2, so gofmt reorders these (and app/v2 relative to app/redis/v2, app/tenant/v2 in the Tenant case). Every generated wire.go is therefore not gofmt-clean. The generated `lint` target's `go fmt` silently rewrites it rather than flagging it, so CI hides the drift.

**Suggested fix:** Order template imports by full import path (ratelimit, redis, tenant, then app/v2) so the rendered file is gofmt-clean as written.

#### 688. [LOW] cron and saga pgstore ship no //go:embed Migrations, unlike the other 5 packages
`data/cron/pgstore/doc.go:25` — consistency — conf 0.70 — _low/info (unverified)_

actionlog, apikey, idempotency, outbox, auditlog each provide a migrations.go with `//go:embed migrations/*.sql` so consumers get the SQL compiled into the binary for migrate.Up(Dir: postgres.Migrations). cron/pgstore and saga/pgstore have a migrations/ dir but no embed (verified: no embed in either pkg), forcing consumers to ship the .sql files out-of-band. doc.go vaguely says 'the kit's existing migration runner reads them' without exposing an fs.FS.

**Suggested fix:** Add a migrations.go with an embedded Migrations embed.FS to each, matching the other five data/infra packages.

#### 689. [LOW] CREATE INDEX lacks IF NOT EXISTS, unlike every other migration
`data/idempotency/pgstore/migrations/20260101000001_create_idempotency_keys.sql:10` — consistency — conf 0.78 — _low/info (unverified)_

All other 8 migrations use CREATE [UNIQUE] INDEX IF NOT EXISTS, but idx_idempotency_keys_expires_at omits it. goose default-wraps each migration in a transaction, so a mid-migration failure that gets retried (or a hand-applied partial state) would error on re-run where the sibling tables tolerate it. Pure inconsistency with the repo's otherwise-uniform defensive pattern.

**Suggested fix:** Add IF NOT EXISTS to match the convention used by the other 8 migration files.

#### 690. [LOW] saga_instances grows unbounded: terminal rows never deleted, no retention support
`data/saga/pgstore/migrations/20260601000002_create_saga_instances.sql:2` — resource-leak — conf 0.72 — _verified_

The executor sets StateCompleted/StateFailed (executor.go:308,370) but never calls Delete on completion, and the migration ships no retention/cleanup path. Completed and failed instances (each carrying input + per-step JSONB results) accumulate forever. The partial resumable index excludes terminal states so scans stay fast, but storage grows without bound. Contrast: outbox ships DeletePublishedBefore/DeleteFailedBefore and idempotency ships DeleteExpired.

**Suggested fix:** Add a DeleteCompletedBefore-style sweep (and a supporting index on (state, updated_at) for terminal states) or document operator-run retention as required.

#### 691. [LOW] Outbox index names drop the table prefix used everywhere else
`infra/outbox/postgres/migrations/20260514000001_create_outbox_entries.sql:36` — consistency — conf 0.60 — _low/info (unverified)_

Sibling tables name indexes idx_<table>_<cols> (idx_action_log_entries_*, idx_audit_log_events_*, idx_approval_requests_*). Outbox uses idx_outbox_pending_ready / idx_outbox_processing_updated / idx_outbox_published_at / idx_outbox_failed_updated — table is outbox_entries, so the prefix is inconsistent. Cosmetic, but makes cross-table operator tooling (pg_indexes greps) less uniform.

**Suggested fix:** Rename to idx_outbox_entries_* for consistency, or accept as cosmetic. Renaming an existing index would need a new migration.

#### 692. [LOW] Many recording rules are computed every 30s but never referenced by any dashboard or alert
`observability/dashboards/prometheus/recording-rules.yaml:33` — performance — conf 0.70 — _low/info (unverified)_

Only http_request_duration_seconds:p99:5m (alerts-latency) and http_ratelimit_limited_ratio:5m (alerts-ratelimit) are consumed. All dashboards compute rate()/histogram_quantile() inline from raw metrics, so http_requests:rate:5m, http_errors:rate:5m, http_error_ratio:5m, grpc_*:rate/error_ratio, every storage p95/p99, amqp/nats/redis-stream ratios, etc. are evaluated continuously but read by nothing.

**Suggested fix:** Either point dashboard panels at the recorded series, or drop the unused rules to save evaluation cost. Document if they exist purely for ad-hoc querying.

#### 693. [INFO] Append-only audit/action log tables have no retention path by design; document the trade-off
`observability/auditlog/postgres/migrations/20260514000001_create_audit_log_events.sql:2` — resource-leak — conf 0.55 — _low/info (unverified)_

audit_log_events and action_log_entries are tamper-evident HMAC chains; their stores expose no Delete/Prune (verified by grep) because deletion breaks VerifyChain. This is intentional, but it means both tables grow unboundedly for the life of the deployment with no kit-provided archival/rotation story, and operators may not realize a manual DELETE silently invalidates the chain.

**Suggested fix:** Document that these tables require external archival/partition-drop and that any row deletion breaks chain verification; consider native range partitioning guidance.

#### 694. [INFO] status_class regex `5..`/`4..` is loose vs the fixed `5xx`/`4xx` label values
`observability/dashboards/prometheus/alerts-availability.yaml:6` — correctness — conf 0.80 — _low/info (unverified)_

redmetrics.go statusClass() only ever emits the literal strings 5xx/4xx/3xx/2xx/1xx. The alerts and slo-templates match status_class=~"5.." / "4..". The anchored regex does match 5xx (5 + any + any), so it works today, but it is unnecessarily loose and would also match hypothetical 599-style values; an exact match is clearer and drift-resistant.

**Suggested fix:** Use status_class="5xx" (and "4xx") for exact equality instead of the `5..` regex.

#### 695. [INFO] Recording-rule naming convention comment doesn't match the actual rule names
`observability/dashboards/prometheus/recording-rules.yaml:5` — docs — conf 0.85 — _low/info (unverified)_

The header says the convention is `metric + :p{50,95,99}5m` (no colon before 5m), but the actual rules are named metric:p50:5m / :p95:5m / :p99:5m (with a colon). Also the chosen scheme metric:pNN:5m does not follow the level:metric:operation convention attributed to Brazil's book.

**Suggested fix:** Fix the comment to read `:pNN:5m` and drop the inaccurate attribution, or rename rules to the documented form.

#### 696. [INFO] Label checker hardcodes `instance` as standard, masking the kit's overloaded instance label
`tools/check-dashboard-labels/main.go:376` — build-ci — conf 0.60 — _low/info (unverified)_

standardLabels includes `instance`, so the tool allows instance on every metric. redis and storage metrics declare their own `instance` label and the dashboards select on it; the tool can never flag the reserved-vs-declared overload (the real risk in this unit) because it short-circuits instance before checking the declared set. Gives false confidence about the instance-collision issue.

**Suggested fix:** When a metric explicitly declares `instance`, validate against the declared set instead of unconditionally allowing it; or emit a warning when a kit metric declares a reserved scrape label name.


### docs — root docs (README/AGENTS/CHANGELOG/SECURITY/CLAUDE), docs/ recipes, package READMEs

_56 findings — 9 high, 35 medium, 11 low, 1 info_

#### 697. [HIGH] Golden-path snippet claims to compile/copy-paste but does not, and contradicts its own 'illustrative' disclaimer
`AGENTS.md:39` — docs — conf 0.92 — _verified_

Line 9 calls the Go block 'an illustrative golden-path shape'; lines 39-40 contradict it: 'a complete package main that compiles against the v2 API at HEAD; copy-paste'. It does not compile: jwt.Module (line 73) and ratelimit.IP (line 77) are used but app/jwt/v2 and app/ratelimit/v2 are never imported, and line 71 sets goredis.Options.Addr to a URL 'rediss://cache.internal:6379' (Addr is host:port, not a URL).

**Suggested fix:** Either mark the block illustrative consistently, or add the missing app/jwt/v2 and app/ratelimit/v2 imports and fix the Redis Addr to a host:port; reconcile lines 9 and 39.

#### 698. [HIGH] AGENTS.md falsely claims OAuth2 client does NOT verify ID-token signature/aud/iss
`auth/oauth2/AGENTS.md:55` — docs — conf 0.97 — _verified_

The 'ID-token verification' section (lines 55-62) states the client does 'minimum' verification — 'split-on-dot, decode payload... We do NOT verify signature, audience, or issuer claim by default.' The code uses go-oidc: provider.Verifier(&oidc.Config{ClientID}) then verifier.Verify() (client.go:182,287), which validates signature+alg+exp+aud+iss. The sibling CHANGES.md:13 and go.mod confirm this. Tells security-conscious devs to add jwtutil for verification that already happens; could cause a downgrade in trust assumptions.

**Suggested fix:** Delete/replace lines 51-62: spans full go-oidc verification already; drop the 'stdlib only / no x/oauth2 dep' and 'minimum verification' claims.

#### 699. [HIGH] Messaging Quick Start uses removed Infrastructure adapter fields (infra.Broker/Publisher/Consumer)
`docs/ai/messaging.md:32` — docs — conf 0.97 — _verified_

Lines 32,40,45,55,60,466,497 use infra.Broker.(*amqpbackend.Connection), infra.Publisher, infra.Consumer. infrastructure.go has NO such fields after the v2.0.0 lazy-adapter refactor; accessors are amqp.Connection/Publisher/Consumer(infra). Contradicts bootstrap.md/adoption.md. No Go code in the repo uses infra.Publisher. The primary messaging code sample will not compile.

**Suggested fix:** Replace infra.Broker/Publisher/Consumer with amqp.Connection(infra)/amqp.Publisher(infra)/amqp.Consumer(infra).

#### 700. [HIGH] amqpbackend.Dial and WithAllowPlaintext do not exist
`docs/ai/messaging.md:65` — docs — conf 0.97 — _verified_

Lines 65,77 use amqpbackend.Dial(url, logger,...); the constructor is amqpbackend.Connect(rawURL, logger, opts...). Lines 78,490,497 reference amqpbackend.WithAllowPlaintext()/WithAllowPlaintext which do not exist; the plaintext opt-out is amqpbackend.WithoutTLS(). testing.md line 100 repeats amqpbackend.Dial.

**Suggested fix:** Use amqpbackend.Connect(...) and amqpbackend.WithoutTLS().

#### 701. [HIGH] Stream section uses non-existent redisstream symbol names
`docs/ai/redis.md:120` — docs — conf 0.96 — _verified_

Lines 120-167 use stream.NewStreamProducer, stream.NewStreamMessage, stream.NewStreamConsumer, stream.StartStreamConsumers, stream.StreamMessage, stream.StreamBinding. The data/stream/redisstream package exports NewProducer, NewMessage, NewConsumer, StartConsumers, type Message, type Binding. None of the doc's Stream*-prefixed names exist; contradicts messaging.md which uses the correct NewProducer/NewConsumer. Whole section will not compile.

**Suggested fix:** Rename to NewProducer/NewMessage/NewConsumer/StartConsumers and types Message/Binding.

#### 702. [HIGH] Builder.WithMigrations and infra.DB do not exist (stale pre-v2 wiring)
`docs/ai/sqldb.md:84` — docs — conf 0.96 — _verified_

Lines 84,129 chain .WithMigrations(migrationsFS) on the Builder; there is no Builder.WithMigrations method — migrations are an option on the postgres module: postgres.Module(cfg, postgres.WithMigrations(fs)). Line 86 infra.DB.Pool() — Infrastructure has no DB field; accessor is postgres.Pool(infra). Both Quick Start and Migrations samples contradict bootstrap.md and won't compile.

**Suggested fix:** Use postgres.Module(cfg, postgres.WithMigrations(fs)) and postgres.Pool(infra).

#### 703. [HIGH] Kafka NewConsumer and WithSASL APIs do not exist
`infra/messaging/kafkabackend/AGENTS.md:18` — docs — conf 0.96 — _verified_

AGENTS documents NewConsumer(brokers, group, opts...) and WithSASL(...). Kafka backend has no NewConsumer (consumer is NewSubscriber(brokers []string, groupID string, topics []string, opts...) (*Subscriber,error), subscriber.go:190) and no WithSASL option — SASL is set via Config.SASLMechanism/SASLUsername/SASLPassword struct fields (client.go:37-39).

**Suggested fix:** Document NewSubscriber(brokers, groupID, topics, opts...) returning *Subscriber, and Config.SASL* fields instead of WithSASL.

#### 704. [HIGH] Cron metric names and labels are entirely fictional
`runtime/cron/AGENTS.md:62` — docs — conf 0.98 — _verified_

AGENTS lists cron_jobs_started_total / cron_jobs_completed_total / cron_jobs_failed_total / cron_jobs_skipped_total{reason} all labeled by `job`. Actual metrics (runtime/cron/metrics.go:34-53) are cron_job_runs_total{name,status}, cron_job_duration_seconds{name}, cron_job_skipped_not_leader_total{name}. No started/completed/failed triplet, no `reason` label, label is `name` not `job`.

**Suggested fix:** Rewrite metrics section to match metrics.go: cron_job_runs_total{name,status}, cron_job_duration_seconds{name}, cron_job_skipped_not_leader_total{name}.

#### 705. [HIGH] Saga Key APIs reference non-existent Workflow type and method
`runtime/saga/AGENTS.md:17` — docs — conf 0.97 — _verified_

AGENTS documents `Workflow` composing Steps and `Workflow.Run(ctx)`. No `Workflow` type exists (grep returns none). Actual API is NewDefinition(steps ...Step) (*Definition, error) plus a package-level Run(ctx context.Context, def *Definition, state any) error (saga.go:58,168). The documented method shape (receiver, 1 arg) is wrong; real Run is a function taking 3 args incl. state.

**Suggested fix:** Replace with NewDefinition(...) (*Definition,error) and Run(ctx, def, state). Note state arg and that Run is package-level, not a method.

#### 706. [MEDIUM] Stale module count: claims 77 modules; go.work has 103
`AGENTS.md:3` — docs — conf 0.93 — _verified_

AGENTS.md line 3 states 'multi-module monorepo, 77 Go modules at /v2 path suffix'. go.work lists 103 modules and 107 tracked go.mod files exist (4 of which are internal tools/ modules not at /v2). The count is off by ~26 and the '/v2 path suffix' universal claim ignores the internal tools modules. This is the canonical AI guide, so agents reason from a wrong inventory.

**Suggested fix:** Update the count to the live go.work figure (and note the tools/* helper modules are not at /v2), or derive it dynamically instead of hardcoding.

#### 707. [MEDIUM] Documented `make check-operational-readiness` target does not exist
`AGENTS.md:23` — docs — conf 0.95 — _verified_

AGENTS.md line 23 lists 'make check-operational-readiness # operational-review coverage for every module' as a command. No such target exists in the Makefile (.PHONY list and rules), and no tools/ script implements it. An agent or maintainer running the documented command gets a 'No rule to make target' error.

**Suggested fix:** Remove the check-operational-readiness command from AGENTS.md, or add the missing Makefile target/tool that implements it.

#### 708. [MEDIUM] CHANGELOG omits all releases after v2.0.0 despite four shipped versions
`CHANGELOG.md:3` — docs — conf 0.90 — _verified_

CHANGELOG lists only v2.0.0 and 'Unreleased — (no entries yet)'. Coordination tags release/v2.0.1, release/v2.0.2, release/v2.1.0 exist (plus per-module v2.0.3), and git log shows real shipped fixes since v2.0.0 (bulkhead semaphore fix fa6458ee, ristretto cache drain 5e6b3f65, saga concurrency e963d521, apikey feature 9a5b0f3b). None appear in the per-release summary the CHANGELOG claims to be.

**Suggested fix:** Add v2.0.1, v2.0.2, v2.0.3, and v2.1.0 sections summarizing the shipped fixes/features, or note the CHANGELOG is intentionally frozen at v2.0.0.

#### 709. [MEDIUM] NOTICE points at deleted SUPPLY_CHAIN.md and claims a per-release SBOM that no longer exists
`NOTICE:8` — docs — conf 0.97 — _verified_

NOTICE (lines 8-10, 14) directs consumers to docs/audit/SUPPLY_CHAIN.md §5 as 'the authoritative dependency manifest and license declarations' and claims a 'CycloneDX SBOM published with each release tag'. Commit 0724df92 deleted both SUPPLY_CHAIN.md (760 lines) and the SBOM workflow. The NOTICE — a legal/attribution artifact shipped to downstreams — now dangles to a removed file and asserts a non-existent SBOM.

**Suggested fix:** Update NOTICE to remove the SUPPLY_CHAIN.md §5 / SBOM references or restore a real dependency-manifest pointer (e.g. docs/audit/README.md + dependency-allowlist.txt).

#### 710. [MEDIUM] Adoption guidance still tells consumers to add a replace block 'until v2.0.0 is on the module proxy' — already shipped
`README.md:73` — docs — conf 0.85 — _verified_

README line 73 (and docs/ai/adoption.md lines 40-43) instruct downstream services to add a replace block 'needed until v2.0.0 is on the module proxy'. v2.0.0, v2.0.1, v2.0.2, and v2.1.0 are all tagged/released (coordination tags present). README's own 'How to publish' (lines 18-21) states the replace-drop happened at v2.0.0. New consumers would add unnecessary, misleading replace directives.

**Suggested fix:** Remove the 'until v2.0.0 is on the module proxy' replace-block guidance from README and adoption.md now that v2.x is published; consumers should require versioned tags directly.

#### 711. [MEDIUM] Templated RELEASE_NOTES_v2.md link broken in 9 in-scope CHANGES.md files
`app/CHANGES.md:7` — docs — conf 0.92 — _verified_

The boilerplate uses a fixed `../../docs/RELEASE_NOTES_v2.md` which only resolves for files exactly two levels deep. Broken in: app/CHANGES.md, crypto/CHANGES.md, grpcx/CHANGES.md (depth-1 -> ../docs/...), and data/cache/rediscache, data/idempotency/pgstore, data/idempotency/redisstore, data/lock/redislock, data/queue/redisqueue, data/stream/redisstream (depth-3 -> data/docs/...). Target is docs/RELEASE_NOTES_v2.md. Every link to the canonical v2 notes 404s in these 9 files.

**Suggested fix:** Compute the relative depth per file (../ for depth-1, ../../../docs for depth-3) or use a repo-root-relative link.

#### 712. [MEDIUM] Documented package-level envelope.Encrypt/Decrypt functions do not exist
`crypto/envelope/AGENTS.md:16` — docs — conf 0.96 — _verified_

Key APIs documents 'envelope.Encrypt(ctx, kek, plaintext, aad)' and 'envelope.Decrypt(ctx, kek, blob, aad)' as package functions taking kek per call. The real API is NewEncryptor(kek KEK) *Encryptor (envelope.go:91) then (*Encryptor).Encrypt(ctx, plaintext, aad) (:104) / .Decrypt(ctx, blob, aad) (:157); the KEK is bound at construction, not passed per-call. A dev copying these signatures gets a compile error.

**Suggested fix:** Document NewEncryptor(kek) and the Encryptor.Encrypt/Decrypt/Rewrap methods with correct signatures (no kek param).

#### 713. [MEDIUM] Documented NewPublisher constructor does not exist; type is Queue not Publisher
`data/queue/redisqueue/AGENTS.md:17` — docs — conf 0.95 — _verified_

Key APIs documents 'NewPublisher(client, opts...) — wraps asynq client' and 'Enqueue(ctx, queue, msg)'. The package has no NewPublisher and no Publisher type. The actual constructor is NewQueue(client goredis.UniversalClient, opts ...Option) *Queue (queue.go:484); Enqueue is a method on *Queue (:570). The constructor also takes a goredis client, not an asynq client. Copying the doc yields a compile error.

**Suggested fix:** Replace NewPublisher with NewQueue(client goredis.UniversalClient, opts...) *Queue; clarify the client type.

#### 714. [MEDIUM] WithPublicGRPCHealth() does not exist (wrong symbol repeated across 4 docs)
`docs/ai/bootstrap.md:194` — docs — conf 0.95 — _verified_

Line 194 'Public gRPC health is disabled unless WithPublicGRPCHealth() is called'. No such symbol exists; the opt-in is grpc.WithPublicHealth() (an app/grpc.Module Option). bootstrap.md line 125 itself correctly uses grpc.WithPublicHealth(), so the file is self-contradictory. Same wrong symbol in RELEASE_NOTES_v2.md:1231 and THREAT_MODEL.md:329.

**Suggested fix:** Refer to grpc.WithPublicHealth() everywhere.

#### 715. [MEDIUM] httpx.RequestID / httpx.SetRequestID do not exist
`docs/ai/http.md:302` — docs — conf 0.95 — _verified_

Lines 299-305 claim httpx.RequestID(r.Context()) and httpx.SetRequestID(ctx,id) live in package httpx ('not middleware/requestid'). Neither symbol exists in httpx. Request IDs are stored/read via core/contextutil.SetRequestID / core/contextutil.RequestID. LOGGING_CONVENTIONS.md line 65 repeats httpx.RequestID(ctx). Confidently-stated wrong API -> compile error for consumers.

**Suggested fix:** Use core/contextutil.RequestID(ctx) / SetRequestID(ctx,id).

#### 716. [MEDIUM] messaging.NewExactSizeLimiter / messaging.RouteLimit do not exist
`docs/ai/messaging.md:152` — docs — conf 0.95 — _verified_

Lines 152-154 call messaging.NewExactSizeLimiter(...) with messaging.RouteLimit{...}. Neither exists. The real API is messaging.NewMessageSizeLimiter(defaultMaxBytes int, overrides ...MessageSizeRouteLimit) with type messaging.MessageSizeRouteLimit. Code sample will not compile.

**Suggested fix:** Use messaging.NewMessageSizeLimiter(...) and messaging.MessageSizeRouteLimit{...}.

#### 717. [MEDIUM] amqp/nats NewMetrics take options, not a positional Registerer
`docs/ai/messaging.md:273` — docs — conf 0.95 — _verified_

Line 273 amqpbackend.NewMetrics(prometheus.DefaultRegisterer) and line 303 natsbackend.NewMetrics(prometheus.DefaultRegisterer) won't compile: both signatures are NewMetrics(opts ...MetricsOption). A prometheus.Registerer is not a MetricsOption; the registerer must go through WithRegisterer.

**Suggested fix:** Use NewMetrics(amqpbackend.WithRegisterer(prometheus.DefaultRegisterer)) / natsbackend.WithRegisterer(...).

#### 718. [MEDIUM] RabbitMQ test helper import path does not exist
`docs/ai/messaging.md:511` — docs — conf 0.95 — _verified_

Line 511 states import path infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest. No such directory exists anywhere. The RabbitMQ integration helper Start(t) lives at github.com/bds421/rho-kit/testing/kittest/v2/amqp (package amqp). testing.md lines 3,15,98 repeat the wrong path/symbol rabbitmqtest.Start.

**Suggested fix:** Point to testing/kittest/v2/amqp (package amqp, func Start(t)).

#### 719. [MEDIUM] redmetrics.NewHTTPMiddleware / WithRegisterer do not exist
`docs/ai/observability.md:81` — docs — conf 0.93 — _verified_

Line 81 redmetrics.NewHTTPMiddleware(redmetrics.WithRegisterer(reg)). Neither symbol exists. The real API is redmetrics.NewHTTP(redmetrics.WithHTTPRegisterer(reg)) returning *HTTPMetrics, then call .Middleware(routeFor). The only metrics code sample in this recipe will not compile.

**Suggested fix:** Use redmetrics.NewHTTP(redmetrics.WithHTTPRegisterer(reg)).Middleware(routeFor).

#### 720. [MEDIUM] redis.LoadRedisFields, redis.Logger, redis.ParseURL do not exist
`docs/ai/redis.md:26` — docs — conf 0.93 — _verified_

Line 26/78 redis.LoadRedisFields() -> actual is redis.LoadFields() (returns Fields). Lines 37,316,317 redis.Logger(logger) -> actual option is redis.WithLogger(l) (real tests use kitredis.WithLogger). Line 316 redis.ParseURL(url) does not exist in infra/redis. bootstrap.md line 234 also uses redis.Logger. Multiple samples will not compile.

**Suggested fix:** Use redis.LoadFields(), redis.WithLogger(l); for URL parsing use goredis.ParseURL.

#### 721. [MEDIUM] redis.WithOnReconnect callback signature is wrong
`docs/ai/redis.md:42` — docs — conf 0.90 — _verified_

Lines 42-44 show redis.WithOnReconnect(func(c *redis.Connection) error {...}). The actual signature is WithOnReconnect(fn func(ctx context.Context, conn *Connection) error) — the callback takes a leading context.Context. The doc's one-arg callback will not compile.

**Suggested fix:** Use func(ctx context.Context, c *redis.Connection) error.

#### 722. [MEDIUM] Testing JWTs example panics at runtime (missing required issuer/audience opts)
`docs/ai/security.md:253` — docs — conf 0.90 — _verified_

Lines 253-254: jwtutil.NewProviderWithKeySet(ks) with no options. The code panics unless WithExpectedIssuer/WithAllowAnyIssuer AND WithExpectedAudience/WithAllowAnyAudience are supplied (RFC 7519 confused-deputy guard). The example as written panics. Also ParseKeySetFromPEM expects a verification (public) key but the var is named testPrivKeyPEM.

**Suggested fix:** Add jwtutil.WithExpectedIssuer(...)/WithExpectedAudience(...) (or the WithAllowAny* opt-outs).

#### 723. [MEDIUM] csrf.WithSecure does not exist; csrf.RequireCSRF deprecation note references a non-existent symbol
`docs/ai/security.md:446` — docs — conf 0.90 — _verified_

Line 446 lists WithSecure(false); the real option is WithoutSecureCookieForLocalHTTP(). Lines 453-454 say csrf.RequireCSRF '(header-presence-only) is deprecated' but csrf.RequireCSRF does not exist anywhere in the repo (likely a removed pre-v2 symbol). Misleads consumers toward non-existent APIs.

**Suggested fix:** Replace WithSecure(false) with WithoutSecureCookieForLocalHTTP(); drop the RequireCSRF note.

#### 724. [MEDIUM] s3backend.LoadS3Config does not exist
`docs/ai/storage.md:48` — docs — conf 0.92 — _verified_

Quick Start line 48 s3backend.LoadS3Config('MYAPP', cfg.Environment). The actual function is s3backend.LoadConfig(envPrefix, environment). LoadS3Config exists nowhere; the Quick Start (S3) sample will not compile.

**Suggested fix:** Use s3backend.LoadConfig('MYAPP', cfg.Environment).

#### 725. [MEDIUM] Redis test sample uses several non-existent symbols
`docs/ai/testing.md:78` — docs — conf 0.92 — _verified_

Line 78 redis.ParseURL(url) (absent in infra/redis), line 80 redis.Logger(slog.Default()) (should be redis.WithLogger), line 84 rediscache.New(...) (the constructor is rediscache.NewCache; redis.md correctly uses NewCache). Line 100 amqpbackend.Dial (should be Connect). The Redis integration example will not compile.

**Suggested fix:** goredis.ParseURL, redis.WithLogger, rediscache.NewCache, amqpbackend.Connect.

#### 726. [MEDIUM] apperror.NewRateLimit shown with two args; signature takes one
`docs/ai/utilities.md:23` — docs — conf 0.92 — _verified_

Line 23 apperror.NewRateLimit('quota exceeded', 30*time.Second). The actual NewRateLimit(msg string) takes one arg; the retry-after variant is NewRateLimitWithRetryAfter(msg, retryAfter). The example will not compile.

**Suggested fix:** Use apperror.NewRateLimitWithRetryAfter('quota exceeded', 30*time.Second).

#### 727. [MEDIUM] validate.RegisterValidation / validator.FieldLevel are wrong library (go-playground), package uses jsonschema
`docs/ai/utilities.md:110` — docs — conf 0.90 — _verified_

Lines 112,386 use validate.RegisterValidation('slug', func(fl validator.FieldLevel) bool{...}) — go-playground/validator API. core/validate wraps jsonschema-go + santhosh-tekuri and exports validate.RegisterFormat(name, FormatFunc); there is no RegisterValidation and no validator.FieldLevel. The custom-validator sample contradicts the section's own jsonschema-tag design and won't compile.

**Suggested fix:** Use validate.RegisterFormat(name, fn FormatFunc); drop the go-playground validator.FieldLevel reference.

#### 728. [MEDIUM] atomicfile.WriteFile and progress.NewProgressReader do not exist
`docs/ai/utilities.md:297` — docs — conf 0.90 — _verified_

Line 297 atomicfile.WriteFile(path,data,0644) — io/atomicfile exports generic Save[T]/Load[T]/LoadOrZero[T], no WriteFile([]byte,perm). Line 305 progress.NewProgressReader(...) — the constructor is progress.NewReader(r,totalBytes,fn,opts...). Both samples will not compile.

**Suggested fix:** Use atomicfile.Save[T]; use progress.NewReader(r,totalBytes,fn).

#### 729. [MEDIUM] G-05 cites non-existent file app/internal_grpc_health.go and symbol WithPublicGRPCHealth()
`docs/audit/THREAT_MODEL.md:329` — docs — conf 0.90 — _verified_

G-05 'Where' links [app/internal_grpc_health.go] which does not exist (internal gRPC health is implemented in app/builder.go + app/module.go), and references WithPublicGRPCHealth() which does not exist (real opt-in: grpc.WithPublicHealth()). Broken link plus wrong symbol in the security-audit document's evidence column.

**Suggested fix:** Fix the file link to app/builder.go/module.go and the symbol to grpc.WithPublicHealth().

#### 730. [MEDIUM] Flagship §6 walk-through uses non-existent auth.Required and removed infra.Cache/infra.IdempStore
`docs/audit/THREAT_MODEL.md:745` — docs — conf 0.85 — _verified_

§6.1 line 745/770 use auth.Required as the JWT middleware; httpx/middleware/auth has no Required (it is auth.JWT(provider)) — contradicts http.md. Line 745 idempotency.Middleware(infra.IdempStore) and §6.3 line 826 infra.Cache.Get(ctx,key,&profile) use Infrastructure fields that no longer exist post-v2, and rediscache.Get is Get(ctx,key)([]byte,error) not a 3-arg form. The canonical wiring example would not compile.

**Suggested fix:** Use auth.JWT(jwt.Provider(infra)); access stores via the per-adapter accessors; rediscache.Get(ctx,key).

#### 731. [MEDIUM] Production-wiring snippet misuses AcquireTx return signature (won't compile)
`examples/saga-coordinator/README.md:140` — docs — conf 0.90 — _verified_

The README's production code writes `if err := locker.AcquireTx(ctx, tx, idemKey); err != nil`. pgadvisory.AcquireTx returns (bool, error) (pgadvisory.go:132), not a single error. The snippet does not compile and silently drops the acquired-bool, so a dev copying it both fails to build and, after a naive fix, may treat lock contention (false) as success.

**Suggested fix:** Show `ok, err := locker.AcquireTx(ctx, tx, idemKey)` and branch on both ok and err.

#### 732. [MEDIUM] Claims OTel spans are automatic via the metrics interceptor; both parts wrong
`grpcx/AGENTS.md:29` — docs — conf 0.90 — _verified_

Observability line 29 says 'Per-call spans are emitted automatically by the metrics interceptor.' metrics.go contains no tracing/span code. Spans come only from otelgrpc.NewServerHandler via the opt-in WithTracingStatsHandler ServerOption (tracing.go:15-16), which NewServer does NOT install by default (server.go:285-360 wires only deadline/metrics/logging/recovery). Spans are neither automatic nor from the metrics interceptor.

**Suggested fix:** State spans require WithTracingStatsHandler (otelgrpc stats handler) and are not on by default.

#### 733. [MEDIUM] Documented Metrics() constructor does not exist (it is NewMetrics)
`grpcx/interceptor/AGENTS.md:24` — docs — conf 0.93 — _verified_

Key interceptors line 24 documents 'Metrics().UnaryInterceptor() / StreamInterceptor()'. There is no Metrics() function; the constructor is NewMetrics(opts...) *GRPCMetrics (metrics.go:48) with methods UnaryInterceptor()/StreamInterceptor() (:91,:106). interceptor.Metrics() is a compile error.

**Suggested fix:** Document NewMetrics(opts...).UnaryInterceptor()/StreamInterceptor().

#### 734. [MEDIUM] Composition example calls non-existent ratelimit.IP
`httpx/websocket/AGENTS.md:47` — docs — conf 0.90 — _verified_

The example mounts `ratelimit.IP(100, time.Minute)(wsHandler)`. The httpx/middleware/ratelimit package has no IP function; IP limiting is NewLimiter(limit, window, opts...) + Middleware(rl) (ratelimit.go:112,384). The same line also passes `jwks` to auth.JWT, which actually requires *jwtutil.Provider (auth.go:52). The example does not compile.

**Suggested fix:** Use ratelimit.Middleware(ratelimit.NewLimiter(100, time.Minute)) and pass a *jwtutil.Provider to auth.JWT.

#### 735. [MEDIUM] NATS WithCredentials / WithNKeyOptions options do not exist
`infra/messaging/natsbackend/AGENTS.md:17` — docs — conf 0.93 — _verified_

AGENTS documents WithCredentials(...) and WithNKeyOptions(...). Neither exists in the package. NATS auth is configured via Config struct fields CredentialsFile and NKeyFile (natsbackend.go:151,156). Also NewConsumer's real signature is NewConsumer(conn *Connection, cfg ConsumerConfig, logger *slog.Logger, opts...) (natsbackend.go:767), not NewConsumer(conn, opts...).

**Suggested fix:** Document Config.CredentialsFile / Config.NKeyFile instead of the With* options, and the real NewConsumer signature including ConsumerConfig and logger.

#### 736. [MEDIUM] Outbox metrics attributed to wrong package with fictional names
`infra/outbox/AGENTS.md:29` — docs — conf 0.97 — _verified_

AGENTS says infra/outbox/postgres exposes outbox_retention_cleanup_seconds and outbox_entries_retained. infra/outbox/postgres has zero Prometheus instrumentation (no prometheus import). The real metrics live in infra/outbox/metrics.go and are outbox_pending_count, outbox_relay_latency_seconds, outbox_published_total, outbox_errors_total — neither documented name exists.

**Suggested fix:** Correct to infra/outbox metrics: outbox_pending_count, outbox_relay_latency_seconds, outbox_published_total, outbox_errors_total. Remove the retention metric claim.

#### 737. [MEDIUM] labelguard.Guard type does not exist
`observability/promutil/AGENTS.md:18` — docs — conf 0.95 — _verified_

AGENTS Key APIs lists `labelguard.Guard` as the type that drops/counts disallowed label values. The labelguard package has no Guard symbol (grep confirms). The exported type is AllowedLabels, constructed via labelguard.New(allowed map[string][]string, opts...) *AllowedLabels (labelguard.go:40,91).

**Suggested fix:** Replace labelguard.Guard with labelguard.New(...) returning *AllowedLabels.

#### 738. [MEDIUM] Circuit breaker constructor name and signature are wrong
`resilience/circuitbreaker/AGENTS.md:15` — docs — conf 0.97 — _verified_

AGENTS Key APIs: `New(opts...)` returns *CircuitBreaker. There is no New() (grep confirms). The only constructor is NewCircuitBreaker(threshold int, cooldownPeriod time.Duration, opts ...Option) (circuitbreaker.go:213). The doc both renames it and drops the two required positional args, so any reader coding to it fails to compile.

**Suggested fix:** Replace with NewCircuitBreaker(threshold, cooldownPeriod, opts...) and explain the two required positional args.

#### 739. [MEDIUM] DefaultPolicy base delay documented as 500ms; actual is 1s
`resilience/retry/AGENTS.md:17` — docs — conf 0.97 — _verified_

AGENTS says Do uses DefaultPolicy() with '3 retries, 500ms base, 30s cap'. DefaultPolicy() actually sets BaseDelay: 1*time.Second (retry.go:117), and the code's own docstring (retry.go:106) and the sibling webhook AGENTS both say '1s base'. The 500ms figure is wrong.

**Suggested fix:** Change '500ms base' to '1s base' to match DefaultPolicy() in retry.go.

#### 740. [MEDIUM] Lifecycle HTTPServer adapter has wrong function name
`runtime/lifecycle/AGENTS.md:22` — docs — conf 0.96 — _verified_

AGENTS documents `HTTPServer(srv)` to adapt *http.Server to Component. The actual function is NewHTTPServer(srv *http.Server) Component (component.go:33); its own panic strings say 'lifecycle: NewHTTPServer requires...'. No HTTPServer function exists. Reader code calling lifecycle.HTTPServer won't compile.

**Suggested fix:** Rename documented API to NewHTTPServer(srv).

#### 741. [LOW] `make fmt` documented as 'goimports + gofumpt' but Makefile runs plain gofmt
`AGENTS.md:27` — docs — conf 0.90 — _low/info (unverified)_

AGENTS.md line 27 says 'make fmt # goimports + gofumpt'. The Makefile fmt target (lines 78-80) runs only 'gofmt -s -w .'; neither goimports nor gofumpt is referenced anywhere in the Makefile or tools. An agent expecting import-grouping/gofumpt formatting from `make fmt` would not get it.

**Suggested fix:** Either change the fmt target to actually run goimports + gofumpt, or update the AGENTS.md comment to say 'gofmt -s'.

#### 742. [LOW] Referenced WithAllowedKeyVersions option does not exist in any adapter
`crypto/envelope/AGENTS.md:34` — docs — conf 0.85 — _low/info (unverified)_

Common mistakes line 34 advises using 'WithAllowedKeyVersions(...)' where the adapter supports it. grep across the entire repo (all *.go) finds no AllowedKeyVersions / WithAllowedKey symbol in crypto/envelope or any KMS adapter. The advice references a nonexistent knob.

**Suggested fix:** Remove the bullet or point to the real key-version-pinning mechanism if one exists.

#### 743. [LOW] Constructor written as method-style Store.New rather than package func New
`data/cron/pgstore/AGENTS.md:11` — docs — conf 0.75 — _low/info (unverified)_

Public API line 11 writes 'Store.New(db *sql.DB, opts ...Option) *Store'. New is a package-level function, New(db, opts...) *Store (store.go:58), not a method on Store. The 'Store.' prefix (used for genuine methods like Store.Add elsewhere) misleads readers into writing a nonexistent method call.

**Suggested fix:** Write 'New(db *sql.DB, opts ...Option) *Store' without the Store. prefix.

#### 744. [LOW] Self-contradicting comment references removed infra.DB/infra.Publisher fields
`docs/ai/adoption.md:122` — consistency — conf 0.85 — _low/info (unverified)_

Line 122 comment 'Register routes using infra.DB, infra.Publisher, etc.' contradicts adoption.md's own correct guidance (postgres.Pool(infra), amqp.Publisher(infra)) and the v2 Infrastructure struct, which has no DB/Publisher fields. Misleads readers of the minimal working program.

**Suggested fix:** Reword to 'Register routes using postgres.Pool(infra), amqp.Publisher(infra), etc.'

#### 745. [LOW] Manual Wiring uses redis.Logger option that does not exist
`docs/ai/bootstrap.md:234` — docs — conf 0.90 — _low/info (unverified)_

Line 234 redis.Connect(cfg.RedisOpts, redis.Logger(logger)). The option is redis.WithLogger(logger). Same wrong option name as redis.md/testing.md.

**Suggested fix:** Use redis.WithLogger(logger).

#### 746. [LOW] Storage package paths drop the infra/ prefix used by every other recipe
`docs/ai/storage.md:3` — consistency — conf 0.80 — _low/info (unverified)_

Line 3 (and the headings) list storage/s3backend, storage/retry, storage/circuitbreaker, storage/encryption, storage/storagehttp, etc. The actual repo-relative paths are infra/storage/s3backend, infra/storage/retry, ... Every other recipe's Packages header is repo-relative (e.g. crypto/encrypt). resilience.md line 144 repeats storage/retry. Readers cannot locate/import these from the stated paths.

**Suggested fix:** Prefix the storage subpackages with infra/ (infra/storage/s3backend, etc.).

#### 747. [LOW] MTLSAuth signature omits the required provider argument
`grpcx/interceptor/AGENTS.md:23` — docs — conf 0.80 — _low/info (unverified)_

Documented as 'MTLSAuthUnary(opts...) / MTLSAuthStream(opts...)'. Actual signatures require a provider first: MTLSAuthUnary(provider *jwtutil.Provider, opts ...MTLSIdentityOption) (auth.go:510) and MTLSAuthStream(...) (:541). The doc shorthand drops the mandatory first parameter.

**Suggested fix:** Show MTLSAuthUnary(provider, opts...) to reflect the required provider arg.

#### 748. [LOW] Recommended interceptor order swaps Metrics/Logging vs kit's own NewServer default
`grpcx/interceptor/AGENTS.md:28` — consistency — conf 0.60 — _low/info (unverified)_

Line 28 prescribes 'Recovery -> Metrics -> Logging -> Auth -> Deadline -> ...'. The kit's NewServer builds the chain outermost->inner as Recovery -> Logging -> Metrics -> Deadline -> caller (server.go:325-353 prepend order). The doc's Metrics-before-Logging contradicts the kit's own default, which is confusing when the doc presents itself as the canonical ordering.

**Suggested fix:** Align the documented order with NewServer (Recovery -> Logging -> Metrics) or note the difference explicitly.

#### 749. [LOW] AMQP backend constructor signatures omit required args
`infra/messaging/amqpbackend/AGENTS.md:19` — docs — conf 0.85 — _low/info (unverified)_

AGENTS shows NewPublisher(conn, opts...) / NewConsumer(conn, opts...) and DeclareAll(conn, binding). Actual: NewPublisher(conn Connector, logger *slog.Logger, opts...) (publisher.go:74), NewConsumer(conn Connector, publisher DeadLetterPublisher, logger *slog.Logger, opts...) (consumer.go:142), DeclareAll(conn Connector, bindings ...BindingSpec) (declare.go:69). Required logger / DeadLetterPublisher args are dropped.

**Suggested fix:** Show the real arg lists incl. logger and the consumer's DeadLetterPublisher; DeclareAll takes variadic BindingSpec.

#### 750. [LOW] AMQP consume-side metric label set overstated
`infra/messaging/amqpbackend/AGENTS.md:32` — docs — conf 0.85 — _low/info (unverified)_

AGENTS says consume-side labels are exchange, queue, outcome. The actual consumed_total/handler_duration_seconds vecs use only {queue, outcome} (amqpbackend/metrics.go:166,172); there is no exchange label on the consume side.

**Suggested fix:** Drop `exchange` from the documented consume-side label set.

#### 751. [LOW] Redis backend constructors take stream types, not a redis client
`infra/messaging/redisbackend/AGENTS.md:17` — docs — conf 0.85 — _low/info (unverified)_

AGENTS: NewPublisher(client, opts...) / NewConsumer(client, opts...) as if taking a redis client. Actual: NewConsumer(consumer *stream.Consumer, logger *slog.Logger) (no opts) and NewPublisher(producer *stream.Producer, opts...) (redisbackend.go:33,54). The doc's 'client' arg and consumer opts are wrong.

**Suggested fix:** Document that NewConsumer takes *stream.Consumer + logger (no opts) and NewPublisher takes *stream.Producer.

#### 752. [INFO] OpaqueLabelValue signature is (prefix, ...parts), not (name, value)
`observability/promutil/AGENTS.md:15` — docs — conf 0.80 — _low/info (unverified)_

AGENTS describes OpaqueLabelValue(name, value). Actual signature is OpaqueLabelValue(prefix string, opaqueParts ...string) string (label_value.go:115). The two-arg call form happens to work, but the second param is variadic, not a single value; the description understates that multiple parts can be joined.

**Suggested fix:** Document the variadic form: OpaqueLabelValue(prefix, parts...).


---

## Appendix — findings investigated and refuted

Raised by a reviewer but disproven by adversarial verification. Listed for transparency.

- `auth/oauth2/AGENTS.md:51` — *AGENTS.md 'Stdlib only' claim contradicts go.mod and code* — go.mod:21,24 require go-oidc/v3 and x/oauth2; client.go:11-12 imports both, uses xoauth2.Config (client.go:175) and oidc.Provider (client.go:160). AGENTS.md:51 'Stdlib only, no x/oauth2 dep' is false. But the claim itself is correct that the line is wrong; verdict applies to the line being a docs error.
- `infra/storage/sftpbackend/sftp.go:680` — *Get silently drops Stat error, returning Size=0 for valid objects* — Factually Stat error is swallowed (sftp.go:680), but the stated impact is wrong: Get returns *sftp.File which implements Seek (client.go:2098), so ServeFile takes the seekable branch (serve.go:125-133) and http.ServeContent derives length/Range via Seek, never reading meta.Size. Range/length not degraded.
- `observability/dashboards/grafana/nats.json:194` — *Terminal `validate_error` / `decode_error` outcomes missing from messaging failure panels* — Filtered failure panels indeed omit validate_error (nats.json:194) / decode+validate_error (amqp.json:133). But both dashboards have unfiltered 'Consume rate by outcome' panels (nats.json:142, amqp.json:99) that break out by outcome with no filter, so these drops ARE visible. 'invisible on every panel' is false.

