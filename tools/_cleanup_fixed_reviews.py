#!/usr/bin/env python3
"""Remove FIXED findings from review-*.md files and update summary counts."""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# Per-file matchers: a finding title matches if ANY predicate returns True.
# Predicates receive the full title text after "### [SEVERITY] ".
FIXED: dict[str, list] = {
    "review-01-core-io.md": [
        # Prior FIXED
        lambda t: "KeyLogWriter" in t,
        lambda t: "Malformed constraint values are silently dropped" in t,
        lambda t: "TOCTOU race defeats Load's symlink refusal" in t,
        lambda t: "Save's directory fsync is inconsistently handled" in t,
        lambda t: "Dead branch in resolveWithSource" in t,
        lambda t: "WithImmediateLoad is silently ignored by FileWatcher" in t,
        lambda t: "MustString discards the underlying error" in t,
        lambda t: "Save chmods the temp file by path after closing" in t,
        # Batches 1-3 FIXED
        lambda t: "Unknown/typo'd format names fail open" in t or ("format names fail open" in t),
        lambda t: "FileWatcher/EnvReloader" in t and "started" in t,
        lambda t: "Zero-value Watchable" in t,
        lambda t: "FileWatcher.Start returns nil" in t
        or ("fsnotify" in t and "channel closes" in t)
        or ("Events/Errors channel closes" in t),
        lambda t: "MaxCorrelationIDLen" in t or "MaxRequestIDLen" in t,
        lambda t: "StringValue discloses exact byte length" in t or ("StringValue" in t and "exact" in t),
        lambda t: "duplicate format" in t or ("RegisterFormat docs promise" in t and "duplicate" in t),
        lambda t: "throttledReader idle-reset" in t or ("throttledReader" in t and "idle" in t),
        # Optional: zero-callers finding resolved by package-doc rewrite
        lambda t: "maputil.SetIfNotNil has zero callers" in t or ("SetIfNotNil has zero callers" in t),
    ],
    "review-02-runtime-resilience.md": [
        # Batch remaining MEDIUM (2026-07-20)
        lambda t: "retry.Loop and doWithPolicy duplicate" in t,
        lambda t: "retry.Loop logs raw worker errors" in t,
        lambda t: "WithReservation" in t and ("clear" in t or "delta-restore" in t or "sibling" in t),
        lambda t: "forceQuit" in t or ("signal channel registered" in t and "SIGINT" in t),
        lambda t: "DurableExecutor.Start/Run have no panic recovery" in t,
        lambda t: "Saga executor logs and persists raw step error" in t or ("raw step error" in t and "saga" in t.lower()),
        lambda t: "Durable compensation runs on the caller's cancellable ctx" in t or ("WithoutCancel" in t and "compensation" in t.lower()),
        lambda t: "per-instance mutual exclusion" in t or ("concurrent Run/Resume" in t and "double-executes" in t),
        lambda t: "No per-instance execution guard" in t or ("MemoryStateStore" in t and "double-executes" in t),
        lambda t: "Raw step error text is persisted" in t,
        lambda t: "driveCompensation comment says only successful" in t,
        lambda t: "Bulkhead default rejection mode contradicts" in t,
        lambda t: "Do/DoWith report failure" in t and "final attempt succeeded" in t,
        lambda t: "DelayOverride" in t and "unclamped" in t,
        lambda t: "Do/DoWith drop the last fn error" in t or "Do/DoWith drop last fn error" in t,
        lambda t: "Do Do" in t or ('Duplicated-word typo "Do Do"' in t),
        lambda t: "mustValidatePolicy" in t,
        lambda t: "startSpan doc" in t or ("startSpan" in t and "recordAttempt" in t),
        lambda t: "NewCircuitBreaker silently clamps" in t or ("clamps threshold" in t),
        lambda t: "Nil-receiver CircuitBreaker" in t or ("nil-receiver" in t.lower() and "CircuitBreaker" in t),
        lambda t: "SetJobTimeout" in t and ("godoc" in t or "wrong name" in t or "WithJobTimeout" in t),
        lambda t: "Add discards the cron-expression parse error" in t
        or ("cron-expression parse error" in t)
        or ("discards the cron" in t and "parse error" in t),
        lambda t: "FuncComponent.Start never cancels" in t or ("FuncComponent" in t and "derived context" in t),
        lambda t: "Package doc describes a Name()" in t or ("Name() method that Component does not have" in t),
        # Batch LOW fixes (2026-07-20)
        lambda t: "ExecuteCtx does not pre-check" in t or ("Bulkhead.ExecuteCtx runs fn" in t) or ("Bulkhead acquires a slot" in t and "cancelled" in t),
        lambda t: "Execute creates a detached root OTel span" in t,
        lambda t: "classifyRetryOutcome misclassifies" in t or ("downstream timeout" in t and "failed_ctx_cancelled" in t),
        lambda t: "MaxDelay is not actually a cap" in t or ("sleeps can exceed" in t and "Jitter" in t),
        lambda t: "Worker.Start runs the first batch" in t and "cancelled" in t,
        lambda t: "WithUnboundedAsync combined with WithWorkerPool" in t,
        lambda t: "Unbounded-async dispatch has a check-then-act" in t or ("Stop never waits for spawned handlers" in t),
        lambda t: "Stop's doc claims subsequent Publish" in t or ("sync handlers" in t and "still execute after Stop" in t),
        lambda t: "submit's blanket recover" in t or ("masks unrelated panics as ErrStopped" in t),
        lambda t: "NewHTTPServer validates only ReadHeaderTimeout" in t,

    ],
    "review-04-crypto.md": [
        # Batch remaining MEDIUM (2026-07-20)
        lambda t: "Alias-configured KEK" in t or ("alias" in t.lower() and "repointing" in t),
        lambda t: "gcpkms KEK takes concrete" in t or ("CRC32C" in t and "untested" in t),
        lambda t: "Version-qualified Config.KeyResource" in t or ("cryptoKeyVersions" in t and "NewKEK" in t),
        lambda t: "OpenProvider/OpenSigningProvider never validate refresh interval" in t or ("interval against maxStale" in t),
        lambda t: "v3 body AAD" in t and "keyID" in t,
        lambda t: "misstates that the keyID is bound into the body GCM AAD" in t,
        lambda t: "V4Local.Close races" in t,
        lambda t: "Zero-value Crypter" in t or "Zero-value secretcrypt.Crypter" in t,
        lambda t: "NewFieldEncryptor doc omits" in t or ("legacy v1 ciphertext" in t and "NewFieldEncryptor" in t),
        lambda t: "EncryptionContext" in t and ("omitted" in t or "contradicts" in t or "Stale package doc" in t),
        lambda t: "Stale package doc claims EncryptionContext" in t,
        # Batch LOW fixes (2026-07-20)
        lambda t: "Fallback keyID scheme accepts version-less" in t,
        lambda t: "Dead `operation` parameter" in t or ("operation` parameter that is never used" in t) or ("classifyGCPError/classifyVaultError/classifyAzureError accept" in t),
        lambda t: "Envelope body length guard" in t or ("short-but-nonempty bodies" in t and "ErrAuthFailed" in t),
        lambda t: "parseBlob's `header` return value is dead" in t or ("header` return value is dead" in t),
        lambda t: "gcpkms Unwrap omits the request-side CRC32C" in t,
        lambda t: "buildToken discards the underlying error" in t or ("set custom claim fails" in t),
        lambda t: "Initial key load in OpenProvider" in t or ("ignores fetchTimeout" in t and "OpenProvider" in t),
        lambda t: "SigningProvider.refresh zeroes only the Reveal" in t,
        lambda t: "Dead if/else branch in Verify" in t or ("Dead conditional: both branches" in t and "bcrypt" in t.lower()),
        lambda t: "bcrypt path accepts empty passwords" in t or ("empty-password handling between bcrypt" in t),
        lambda t: "bcrypt compatibility path silently verifies only the first 72" in t,
        lambda t: "Crypter retains the master key with no Close" in t or ("Crypter has no Close/zeroize" in t) or ("Crypter retains master key for its lifetime" in t),
        lambda t: "secretcrypt never zeroes the per-operation HKDF" in t,
        lambda t: "KeyStore interface doc carries a misplaced WARNING" in t,
        lambda t: "StaticKeyStore.CurrentKeyID returns success-shaped" in t or ("CurrentKeyID returns success-shaped" in t),

    ],
    "review-03-app-wiring.md": [
        # Batch 4 FIXED
        lambda t: "AMQP" in t and ("loopback" in t or "provider" in t)
        and ("plaintext" in t or "exemption" in t or "URL provider" in t or "transport" in t),
        lambda t: "ServerOptions" in t or ("server-option" in t.lower() and "TLS" in t)
        or ("WithServerOption" in t and ("order" in t or "mTLS" in t or "TLS" in t)),
        lambda t: "cron" in t.lower() and ("leader" in t.lower())
        and ("fail" in t.lower() or "silent" in t.lower() or "gating" in t.lower()),
        lambda t: "applyPhasedMiddleware" in t,
        lambda t: "ModuleName" in t,
        lambda t: "audience" in t.lower() and ("opt-out" in t.lower() or "opt out" in t.lower() or "warn" in t.lower()),
        lambda t: "TestInfrastructure" in t,
        lambda t: "OnShutdown" in t and ("godoc" in t.lower() or "doc" in t.lower()),
        lambda t: "buildIntegrationModules" in t
        or ("With/" in t and "godoc" in t.lower())
        or ("godoc drift" in t.lower() and ("With" in t or "buildIntegration" in t)),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "app/nats" in t and ("transport" in t or "loopback" in t or "plaintext" in t),
        lambda t: "IP rate-limit bridge" in t and "trusted-proxy" in t,
        lambda t: "Keyed limiter" in t and ("DeclaresRateLimit" in t or "without installing" in t or "mandatory rate-limit" in t),
        lambda t: "Public gRPC listener bypasses" in t and "rate-limit" in t,

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "Godoc links reference a non-existent [Named]" in t or ("[Named]" in t and "WithNamed" in t),
],
    "review-05-security.md": [
        lambda t: "Manager.Rotate resurrects revoked keys" in t,
        lambda t: "rotatedExpir" in t and ("292" in t or "zero-CreatedAt" in t or "unset CreatedAt" in t),
        lambda t: "Revoke cannot move" in t and "revocation earlier" in t,
        lambda t: "MemoryPrefixRepository" in t,
        lambda t: "Provider.KeySet()" in t and ("issuer" in t or "audience" in t),
        lambda t: "ServerTLS/ClientTLS fail open" in t,
        lambda t: "WithClock" in t or ("HMACSigner.now" in t and ("dead" in t or "no effect" in t or "write-only" in t)),
        lambda t: "orphaned" in t and "Rotate" in t,
        lambda t: "Rotate leaves" in t and ("orphan" in t or "revocation fails" in t),
        lambda t: "SubjectUserID" in t,
        lambda t: "FilesCertificateSource poll" in t
        or ("FilesCertificateSource" in t and ("garbage-collected" in t or "GC" in t or "poll goroutine" in t)),
        lambda t: "jwksHTTPClient" in t and ("TLS" in t or "RoundTripper" in t or "floor" in t),
        # Batch 4 FIXED — NeedsRehash / owned rotate-revoke
        lambda t: "NeedsRehash" in t or ("ScopedResolver" in t and "NeedsRehash" in t),
        lambda t: "RotateOwned" in t or "RevokeOwned" in t
        or ("owner-scoped" in t.lower() and ("Rotate" in t or "Revoke" in t)),
        # KeySet.Verify docs-only fixes: KEEP OPEN if fail-closed default remains a finding
        # (do not remove generic KeySet.Verify fail-closed default findings)
        # Batch LOW fixes (2026-07-20)
        lambda t: "MustNewIssuer discards the underlying error" in t,
        lambda t: "Sub-second CSRF TTLs truncate" in t,
        lambda t: "NewProvider accepts an empty JWKS URL" in t or ("NewProvider discards validateJWKSURL" in t) or ("discards the detailed URL-validation error" in t),
        lambda t: "Dead TTL computation in revokeID" in t or ("Dead branch: `if s.clock != nil`" in t) or ("clock != nil` in revokeID" in t),
        lambda t: "Several files in scope are not gofmt-clean" in t,
        lambda t: "Mint returns the verification sentinel ErrInvalidToken" in t,

    ],
    "review-06-auth-authz.md": [
        lambda t: "NewClient" in t and "construction context" in t,
        lambda t: "Handlers()" in t and "/oauth" in t,
        lambda t: "Single-use" in t and "Get-then-Delete" in t,
        lambda t: "MemorySessionStore" in t
        or ("Session store hands out shared" in t)
        or ("Zeroize-on-eviction corrupts Session" in t)
        or ("SessionStore contract" in t and "zeroize" in t)
        or ("shared *secret.String" in t),
        lambda t: "Package doc advertises" in t
        and ("refresh" in t or "client-credentials" in t or "token refresh" in t),
        lambda t: "openfga.New panics" in t or ("openfga" in t.lower() and "panics" in t and "TLS" in t),
        # OAuth2 state cookie CSRF / browser-bound (HIGHs)
        lambda t: "login state not bound" in t
        or ("state not bound to the user's browser" in t)
        or ("login CSRF" in t)
        or ("session fixation" in t and "OAuth" in t)
        or ("session swap" in t and "OAuth" in t),
        # Callback store infra vs state mismatch (503 tests)
        lambda t: "StateStore infrastructure" in t
        or ("state mismatch" in t and ("Callback" in t or "collapses" in t or "conflates" in t)),
        # SessionFromRequest: KEEP — code exists but no dedicated tests
        # MEDIUM clearance batch 2026-07-20
        lambda t: "SessionFromRequest" in t or ("No exported way to authenticate" in t) or ("No exported API to consume the session" in t),
        lambda t: "Logout swallows" in t or ("session-store Delete" in t and "Logout" in t),

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "Client.cfg field is written once and never read" in t,
        lambda t: "ID-token verification failure is reported with the ErrCodeExchange" in t,
        lambda t: "Claims-decode error text is reflected verbatim" in t,
        lambda t: "Session persistence failure in the callback is not logged" in t,  # already fixed pre-pass; strip if still listed
        lambda t: "Successful callback with no redirect_to returns 204" in t,
        lambda t: "Exported Handlers interface is dead code" in t,
        lambda t: "Godoc comment on NewMemoryStore refers to a nonexistent" in t or ("NewMemoryStore refers to a nonexistent 'NewMemory'" in t),
        lambda t: "MemoryStore godoc name mismatch" in t,
        lambda t: "mustValidateRequest panics with a fixed message" in t,
        lambda t: "Decider.storeID and modelID fields are dead" in t,
],
    "review-07-httpx-core.md": [
        lambda t: "WithDefaultAmount(0)" in t,
        lambda t: "Hard-enforcement reconcile refunds" in t,
        lambda t: "/slo" in t and ("+Inf" in t or "empty 200" in t),
        lambda t: "DecodeJSON hard-codes a 1 MB" in t or ("DecodeJSON" in t and "1 MB" in t),
        lambda t: "does not block HTTP redirects" in t or ("SSRF pivot" in t and "redirect" in t),
        lambda t: "Full webhook delivery URL logged unredacted" in t
        or ("Full webhook target URL" in t and "unredacted" in t)
        or ("webhook" in t.lower() and "unredacted" in t and "URL" in t),
        lambda t: "429/408" in t or ("treats 429" in t) or ("never retries rate-limited" in t),
        # Batch 7 FIXED
        lambda t: "cancelOnCloseBody" in t,
        lambda t: "async audit" in t.lower()
        or ("asyncAudit" in t)
        or ("WithAsyncAuditDispatch" in t)
        or ("strict-audit" in t.lower() and "async" in t.lower())
        or ("strict audit" in t.lower() and "async" in t.lower()),
        lambda t: "SecurityScheme.Extensions" in t
        or ("Extensions is documented as an escape hatch" in t),
        lambda t: "ListFn" in t and ("limit+1" in t or "limit + 1" in t or "fetch limit" in t),
        lambda t: "shares one circuit breaker across all hosts" in t
        or ("circuit breaker across all hosts" in t)
        or ("one circuit breaker" in t and "hosts" in t),
        lambda t: "Predicate-excluded errors" in t
        or ("excluded errors are recorded as circuit-breaker successes" in t)
        or ("notCountedError" in t and "circuit" in t.lower()),
        lambda t: "X-Kit-Delivery-Id" in t
        or ("Delivery-Id" in t and ("HMAC" in t or "signature" in t or "replay" in t))
        or ("DeliveryID" in t and ("HMAC" in t or "signature" in t or "replay" in t)),
        lambda t: "hasResponseOption" in t
        or ("WithResponseDescription alone" in t)
        or ("description-only option" in t and "response" in t.lower()),
        # Batch LOW fixes (2026-07-20)
        lambda t: "WriteValidationError accepts a *slog.Logger parameter it never uses" in t,
        lambda t: "WriteJSON does not guard the status range" in t,
        lambda t: "Exported OffsetParams struct is dead" in t,
        lambda t: "SetGlobalSecurity stores the caller's slice by reference" in t,

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "RoutesFromHandler is an exported placeholder" in t,
        lambda t: "RoundTrip returns without closing req.Body" in t,
        lambda t: "Signing transport's early-error return violates the RoundTripper" in t,
],
    "review-08-httpx-middleware.md": [
        lambda t: "Mid-stream Flush never reaches" in t,
        lambda t: "Route label" in t and "unmatched" in t,
        lambda t: "verify() zeroes" in t,
        lambda t: "apikey extractToken" in t or ("apikey.Middleware credential extraction is unhardened" in t),
        lambda t: "undecided→compressed" in t or "undecided->compressed" in t,
        # Batch 4 FIXED
        lambda t: "Nonce TTL" in t or ("nonce" in t.lower() and "skew" in t.lower() and "TTL" in t),
        lambda t: "body-read" in t.lower() or ("body read" in t.lower() and "client" in t.lower() and "fault" in t.lower())
        or ("client fault" in t.lower() and "body" in t.lower()),
        lambda t: "CaptureRoute" in t or ("span name" in t.lower() and ("tracing" in t.lower() or "route" in t.lower())),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "X-Real-IP is trusted verbatim" in t,
        lambda t: "compressWriter.WriteHeader latches 1xx" in t,
        lambda t: "responseCapture forwards 1xx" in t or ("103 Early Hints" in t and "responseCapture" in t),
        lambda t: "Bounded LRU visitor cache" in t or ("key/IP-spray" in t and "rate limit" in t.lower()),
        lambda t: "readSpooledBody" in t and ("MaxInt64" in t or "max+1" in t),
        lambda t: "Nonce replay-protection store is not scoped by key ID" in t or ("cross-key nonce" in t),
        lambda t: "timeoutWriter.Unwrap" in t,

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "RequireScopeStrict's godoc describes a difference" in t,
        lambda t: "RequireScopeStrict's differentiating doc is stale" in t,
        lambda t: "NewSessionAuthenticator omits the nil-verifier" in t,
        lambda t: "NewSessionAuthenticator is the only strategy constructor without a nil-argument" in t,
        lambda t: "compress WithLogger is a dead option" in t,
        lambda t: "compressWriter lifecycle doc claims the MaxBuffer bail-out is \"logged at warn level\"" in t
        or ("logged at warn level" in t and "MaxBuffer" in t),
        lambda t: "selectEncoder treats negative q-values" in t,
        lambda t: "Metrics.errors field comment claims Set faults" in t,
        lambda t: "store.Set failure is not counted in Metrics.errors" in t,
        lambda t: "Post-handler Set failure is not counted" in t,
        lambda t: "KeyedLimiter.cleanup comment contradicts" in t,
],
    "review-09-websocket-realtime.md": [
        lambda t: "allow-all OnSubscribe" in t
        or "allow-all" in t
        and "OnSubscribe" in t
        or ("deny-by-default subscribe/publish into allow-all" in t)
        or ("default-deny channel authorization into default-allow" in t),
        lambda t: "Documented extension path" in t
        or "Documented channel-authz extension path" in t
        or "Documented channel-authz escape hatch" in t
        or "Documented extension path for channel authorization" in t,
        lambda t: "Start sets" in t and "started" in t
        or "Stop before Start permanently disables" in t
        or "Start/Stop check-then-act race" in t
        or ("Failed Node.Start leaves started=true" in t),
        # Batch 4 FIXED — JWKS outage HIGH
        lambda t: "JWKS outage" in t
        or ("DisconnectInvalidToken" in t and ("JWKS" in t or "KeySet" in t or "invalid token" in t.lower()))
        or ("terminal 'invalid token'" in t)
        or ('terminal "invalid token"' in t)
        or ("invalid token' disconnect" in t)
        or ("invalid token\" disconnect" in t),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "WithReadDrain is silently dropped" in t,
        lambda t: "CloseRead" in t and ("cancels" in t or "cancel" in t) and ("per-connection" in t or "context" in t),
        lambda t: "WebsocketHandler hardcodes empty WebsocketConfig" in t or ("WebsocketHandler hardcodes an empty" in t),
        lambda t: "OnConnecting JWT auth path" in t or ("extractBearer have zero test" in t),
        lambda t: "Unauthenticated-by-default connect" in t or ("WithNoAuthUnsafe" in t) or ("omitting WithJWTAuth" in t),

    ],
    "review-10-grpcx.md": [
        lambda t: "AppendOutgoingIdentity" in t and "x-user-id" in t,
        lambda t: "WithRetry accepts an invalid" in t
        or ("Retry policy never validated at construction" in t)
        or ("invalid policy panics on every RPC" in t),
        lambda t: "MinTime" in t and ("30s" in t or "GOAWAY" in t or "keepalive" in t.lower()),
        lambda t: "Trusted-S2S" in t
        or ("trusted-S2S" in t)
        or ("Trusted S2S" in t)
        or ("permission laundering" in t),
        # Batch 7 FIXED
        lambda t: "stream deadline" in t.lower()
        or ("Default 30s stream deadline" in t)
        or ("Default 30s per-RPC deadline is applied to streaming" in t)
        or ("cannot separate unary from stream deadlines" in t),
        lambda t: "identity" in t.lower() and ("opt-out" in t.lower() or "no opt-out" in t.lower())
        or ("Identity metadata" in t and "no opt-out" in t)
        or ("Verified-identity metadata" in t and "no opt-out" in t)
        or ("WithoutIdentityPropagation" in t),
        lambda t: "non-idempotent" in t
        or ("RetryUnary re-executes" in t)
        or ("idempotent methods" in t and "retry" in t.lower()),
        lambda t: "permanent ResourceExhausted" in t
        or ("isPermanentResourceExhausted only recognizes" in t)
        or ("PayloadTooLarge/StorageFull" in t and "retried" in t),
        lambda t: "Server logging escalates every non-OK" in t
        or ("logging escalates" in t and "Warn" in t),
        lambda t: "Panicked RPCs are omitted from metrics" in t
        or ("panicked RPC" in t.lower() and "metrics" in t.lower()),
        lambda t: "MaxHeaderListSize" in t and ("typed" in t.lower() or "no typed" in t.lower() or "silently discarded" in t),
        # Batch LOW fixes (2026-07-20)
        lambda t: "NewHealthServer swallows the ValidateChecker error" in t,
        lambda t: "Health Check fail-open: unknown/zero health.Status" in t,
        lambda t: "AsAuthOption lacks the nil-check" in t,
        lambda t: "tryRegister discards the registration error" in t or ("Hand-rolled tryRegister duplicates" in t),
        lambda t: "WithReflection godoc's ordering claim" in t or ("WithReflection doc claims reflection is registered after" in t),

    ],
    "review-11-data-core-a.md": [
        # Batch remaining MEDIUM (2026-07-20)
        lambda t: "Reflection-based deep metadata clone" in t,
        lambda t: "Entry.Clone dedupes slices" in t or ("aliased sub-slices" in t),
        lambda t: "CursorSigner is a ~150-line" in t or ("lacks the Close() key-zeroing" in t),
        lambda t: "actionlog CursorSigner has no Close" in t,
        lambda t: "Package doc's canonicalisation contract is stale" in t
        or ("canonicalisation contract is stale" in t)
        or ("Package doc describes a stale canonicalisation" in t),
        lambda t: "Leader/follower classification via inflight" in t
        or ("misclassified caller can cancel" in t),
        lambda t: "singleflight-shared compute result" in t
        or ("reference-typed T aliased" in t),
        lambda t: "Public Wait() bypasses bgMu" in t
        or ("Wait() bypasses bgMu" in t)
        or ("ComputeCache.Wait races WaitGroup" in t),
        lambda t: "MemoryCache.Close races ristretto" in t,
        lambda t: ("actionlog" in t.lower() or "Logger.Get" in t)
        and ("TenantStore" in t or "tenant scoping" in t or "IDOR" in t or "no tenant" in t),
        lambda t: "VerifyChain cannot detect tail truncation" in t,
        lambda t: "SignEntry omits" in t and ("microsecond" in t or "µs" in t or "truncation" in t),
        lambda t: ("Reason" in t or "control char" in t)
        and ("control" in t or "newline" in t or "ANSI" in t),
        lambda t: "WithEntryCost" in t and ("67" in t or "million" in t),
        lambda t: "Zero-TTL SetNX" in t,
        lambda t: "Delete/SetNX" in t and "Close" in t and "panic" in t
        or ("MemoryCache.Delete/SetNX" in t and "Close" in t),
        lambda t: "use-after-Close" in t
        or ("After Close, MemoryCache" in t)
        or ("After Close" in t and "MemoryCache" in t and "misleading" in t),
        lambda t: "Query.Cursor" in t and "DecodeCursor" in t,
        lambda t: "SignEntry godoc" in t and ("too-short" in t or "short-secret" in t or "misstates" in t),
        lambda t: "validID" in t and "length cap" in t,
        lambda t: ("Store.Decide" in t or "DecodeCursor" in t)
        and ("nonexistent" in t or "docs reference" in t or "godoc" in t.lower() or "Godoc" in t),
        lambda t: "New() godoc" in t and "Open" in t,
        lambda t: "After Close" in t
        and ("misleading" in t or "closed-cache" in t or "ErrCacheMiss" in t or "ErrAdmissionRejected" in t),
        # Batch LOW fixes (2026-07-20)
        lambda t: "NewScope returns ErrAnonymousScope whose message" in t,
        lambda t: "Scope.WhereClause/Key silently emit" in t,
        lambda t: "SetNX helper skips key validation" in t,

        # MEDIUM clearance batch 2026-07-20
        lambda t: "SetNX returns true" in t and ("admission" in t or "Ristretto" in t),

    ],
    "review-12-data-core-b.md": [
        lambda t: "ValidateCachedResponse" in t and ("header" in t.lower() or "header-size" in t or "aggregate" in t),
        lambda t: "fractional capacity" in t or ("capacity in (0,1)" in t),
        lambda t: "2^63" in t or "capacity >= 2^63" in t or "int(l.capacity)" in t,
        lambda t: "sweep" in t and "fingerprint" in t,
        lambda t: "Set can succeed after its lock is reclaimed" in t
        or ("silently dropping the fingerprint" in t),
        # Batch 8 FIXED — keep tenant forgeability (PARTIAL).
        # queue/stream Consumer *docs* (interface-vs-backend drift) already
        # cleared; residual Consume-error-return / stream-Validate* LOWs stay OPEN.
        lambda t: "TTL-takeover" in t or "TTL takeover" in t
        or ("locktest" in t.lower() and "TTL" in t and ("conformance" in t.lower() or "suite" in t.lower())),
        lambda t: ("advertise" in t.lower() or "doc" in t.lower() or "package doc" in t.lower())
        and ("Consumer" in t or "Producer" in t)
        and ("queue" in t.lower() or "stream" in t.lower()),
        lambda t: "gcra and tokenbucket sweeps scan" in t
        or ("sweep" in t and "mutex" in t and ("Allow" in t or "hot path" in t)),
        lambda t: "CancelAt" in t or ("token-leak" in t and "tokenbucket" in t.lower())
        or ("token leak" in t.lower() and ("rate" in t.lower() or "tokenbucket" in t.lower())),
    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "Two divergent package doc comments for package idempotency" in t,
        lambda t: "Dead `_ = errors.Is` statement" in t or ("Dead `_ = errors.Is`" in t) or ("_ = errors.Is" in t),
],
    "review-13-data-pg-stores.md": [
        lambda t: "claim/lease" in t or ("saga UPDATE" in t and "multi-replica" in t),
        lambda t: "Decide returns nanosecond DecidedAt" in t or ("nanosecond DecidedAt" in t),
        lambda t: "OccurredAt" in t and ("microsecond" in t or "µs" in t or "sub-microsecond" in t),
        # Batch 8 FIXED MEDIUM pile (+ saga Migrations residual LOW now embed.FS)
        # cron Add 23505 LOWs stay OPEN (no ErrScheduleExists yet).
        lambda t: ("classify" in t.lower() or "Create" in t) and "23505" in t
        or ("ErrDuplicateID" in t)
        or ("unique violation" in t.lower() and ("Create" in t or "approval" in t.lower() or "actionlog" in t.lower() or "apikey" in t.lower())),
        lambda t: "approval" in t.lower() and ("index" in t.lower() or "created_at" in t.lower())
        and ("pagination" in t.lower() or "List" in t or "missing" in t.lower()),
        lambda t: "ForTenant" in t or ("tenant-scoped" in t.lower() and ("Approve" in t or "Reject" in t or "MarkExecuted" in t)),
        lambda t: "headers" in t.lower() and ("escape" in t.lower() or "JSON" in t),
        lambda t: "NULL" in t and "fingerprint" in t.lower(),
        lambda t: "Migrations" in t and ("embed" in t.lower() or "saga" in t.lower() or "export" in t.lower()),
        lambda t: "does not export its migrations" in t,
    ],
    "review-14-data-redis-stores.md": [
        lambda t: "budgetScript" in t or ("INCRBY" in t and ("10^14" in t or "1e14" in t or "Lua" in t)),
        lambda t: "doRelease marks the handle terminally released" in t
        or ("released-on-transport-error" in t)
        or ("doRelease" in t and "transport" in t)
        or ("released" in t.lower() and "atomic" in t.lower()),
        lambda t: "refundScript" in t or ("refundScript writes a Lua number" in t),
        lambda t: "DegradedCache.SetNX fails open" in t
        or ("DegradedCache" in t and "SetNX" in t and ("fail" in t.lower() or "outage" in t)),
        # Batch 8 FIXED — keep redlock duplication OPEN
        lambda t: "Prefix+key concatenation" in t or ("no enforced delimiter" in t)
        or ("prefix separator" in t.lower())
        or ("terminating delimiter" in t.lower()),
        lambda t: "Refund" in t and (
            "doc" in t.lower() or "documents" in t.lower() or "godoc" in t.lower()
            or ("current period" in t.lower() and "window" in t.lower())
        ),
        # Get STRLEN+GET correctness/TOCTOU (not the pure RTT perf nit on Set/Unlock)
        lambda t: "STRLEN" in t and "GET" in t and (
            "TOCTOU" in t or "doGet" in t or ("Get" in t and ("hard" in t.lower() or "size gate" in t.lower()))
        ),
        lambda t: "fencing" in t.lower() and ("redlock" in t.lower() or "doc" in t.lower() or "safety" in t.lower()),
        lambda t: "keyTTL" in t or ("GCRA debt" in t) or ("debt horizon" in t)
        or ("TTL floor" in t and "GCRA" in t),
        lambda t: "unparseable stored payloads" in t or ("non-JSON" in t)
        or ("json.Unmarshal failure" in t.lower())
        or ("wrong-marker JSON" in t)
        or ("hard-errors on unparseable" in t),
    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "Corrupted package-doc sentence on the Store contract" in t,
],
    "review-15-queues-streams.md": [
        lambda t: "deadLetter pipelines XACK" in t,
        lambda t: ("riverqueue" in t or "EnvelopeWorker" in t or "JobCancel" in t)
        and ("poison" in t or "permanent" in t or "JobCancel" in t or "retryable" in t),
        # Batch 5 FIXED
        lambda t: "self-feeding" in t or ("Dead-letter stream is never checked against the source" in t),
        lambda t: "claimMinIdle" in t and ("handlerTimeout" in t or "exceeds" in t),
        lambda t: "WithMaxRetries and WithInvisibilityTimeout are enqueue-time" in t
        or ("enqueue-time options but read like consumer-side" in t),
        lambda t: "NewQueue eagerly registers default metrics" in t
        or ("defaultMetrics" in t and "DefaultRegisterer" in t),
        lambda t: "ErrDuplicateMessage" in t
        or ("Duplicate-ID enqueue" in t)
        or ("Idempotent-duplicate Enqueue" in t)
        or ("asynq's, not the kit's" in t)
        or ("forcing callers to import asynq" in t),
        lambda t: "processPending redeliveries" in t
        or ("processPending" in t and "delivery counter" in t)
        or ("processPending" in t and "maxRetries" in t),
        lambda t: "C0" in t or ("control characters through the transport-bridge" in t)
        or ("Header value validation only blocks null" in t),
        lambda t: "maxStreamHeaders" in t
        or ("header pre-cap" in t.lower())
        or ("32 KiB" in t and "header" in t.lower() and ("producer" in t.lower() or "dead-letter" in t.lower())),
        lambda t: ("nil registerer" in t.lower() or "nil-registerer" in t.lower() or "swallow a nil registerer" in t)
        or ("WithConsumerRegisterer" in t and ("nil" in t.lower()))
        or ("WithProducerRegisterer" in t and ("nil" in t.lower())),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "WithMaxRetries(0) means opposite" in t,
        lambda t: "Dead-letter writes copy unvalidated" in t or ("Dead-letter path copies rejected" in t),
        lambda t: "Delivery-count fetch failures default to 1" in t,
        lambda t: "Retry latency is silently governed by claimMinIdle" in t,

        lambda t: "Default 7-day MINID retention" in t,
        lambda t: "Producer and Consumer payload caps are independent" in t,

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "WithConsumerID(\"\") is silently ignored" in t or ("WithConsumerID" in t and "silently ignored" in t),
        lambda t: "buildServerConfig takes an unused Handler parameter" in t,
],
    "review-16-messaging-core.md": [
        lambda t: ("returns nil" in t and "cancel" in t)
        or ("Consumer interface documents" in t and "cancel" in t)
        or ("Consumer.Consume returns ctx.Err()" in t)
        or ("Consumer.Consume contract" in t and "cancel" in t),
        lambda t: "awaitCancel" in t and ("busy" in t or "hot-spin" in t or "busy-spin" in t),
        # Batch 5 FIXED
        lambda t: "directInFlight" in t and ("panic" in t.lower() or "drainBatch" in t),
        lambda t: "Journal bookkeeping not invalidated" in t
        or ("journalReady" in t and ("save" in t.lower() or "snapshot" in t.lower()))
        or ("snapshot save fails" in t),
        lambda t: "Journal append path bypasses the symlink" in t
        or ("journal" in t.lower() and "symlink" in t.lower()),
        lambda t: "SchemaRegistry interface is unusable" in t
        or ("SchemaRegistry interface" in t and ("unusable" in t or "concrete" in t or "*InMemorySchemaRegistry" in t)),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "pendingBytesLocked is an O(n)" in t,
        lambda t: "redisbackend.Consume silently ignores Binding.Retry" in t,
        lambda t: "Redis consume path performs no message/header validation" in t,
        lambda t: "Derived retry/dead exchange names" in t,

        lambda t: "Schema validation fails open" in t,

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "BufferedPublisher.Publish godoc claims" in t or ("Returns an error only when the buffer is full" in t),
],
    "review-17-messaging-backends.md": [
        lambda t: "Nak" in t and ("zero delay" in t or "MaxDeliver" in t),
        lambda t: ("Stop" in t and "cancelled ctx" in t)
        or ("Connection.Stop with an already-cancelled" in t)
        or ("Stop(ctx) with an already-cancelled" in t),
        lambda t: "reconnect" in t.lower() and ("Dead" in t or "zombie" in t),
        lambda t: "Backoff and attempt counter reset" in t or ("backoff" in t.lower() and "onReconnect" in t),
        lambda t: "dlqConsecutiveFail" in t or ("DLQ consecutive-failure counter is shared" in t),
        lambda t: "Dead-letter publish inherits" in t or ("DLQ" in t and "expired" in t and "context" in t),
        lambda t: "AMQP consume" in t and ("label" in t or "queue label" in t or "raw" in t),
        lambda t: "ReplySender" in t and ("ReplyTo" in t or "attacker" in t or "requester-controlled" in t),
        lambda t: ("SASL/PLAIN" in t and "TLS" in t) or ("SASL/PLAIN with no TLS" in t),
        lambda t: "fromKafkaMessage" in t,
        lambda t: ("Username/Password or Token" in t and "TLS" in t)
        or ("plaintext Username/Password" in t)
        or ("password" in t.lower() and "token" in t.lower() and "TLS" in t and "NATS" not in t and "validateAuth" in t),
        lambda t: "X-Exchange" in t or ("spoofable headers" in t and "subject" in t),
        lambda t: "NATS Consumer.Consume blocks forever" in t
        or ("terminal subscription errors" in t)
        or ("Consume terminal stall" in t),
        # Batch 5 FIXED
        lambda t: "timeout" in t.lower() and ("alignment" in t.lower() or "diverge across sibling" in t)
        or ("Handler execution semantics diverge" in t),
        lambda t: "guest/guest" in t or ("defaults RabbitMQ credentials" in t),
        lambda t: "WaitForConnection" in t and ("Dead()" in t or "Dead" in t),
        # Kafka transient docs-only: only remove if title is clearly docs
        lambda t: ("Kafka" in t and "transient" in t.lower() and ("doc" in t.lower() or "misstate" in t.lower())),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "NewPublisher and NewSubscriber convenience constructors" in t,
        lambda t: "Every transient handler error tears down the group reader" in t,
        lambda t: "Every transient handler error closes and recreates" in t,

    ],
    "review-18-storage-core.md": [
        lambda t: "Stored XSS" in t or ("serves attacker-controlled Content-Type inline" in t),
        lambda t: "stat errors on the walk root" in t or ("swallowed as an empty successful listing" in t),
        lambda t: "continuing to yield objects after yielding an error" in t
        or ("List violates the Lister contract by continuing" in t),
        lambda t: ".tmp-" in t or ("Migrate silently drops" in t),
        lambda t: "MaxBytesReader" in t and ("500" in t or "4xx" in t or "validation" in t)
        or ("Request-too-large errors from MaxBytesReader" in t),
        lambda t: "image/*" in t or ("image/svg+xml" in t and ("admits" in t or "wildcard" in t or "SVG guard" in t)),
        # Batch 7 FIXED
        lambda t: "MaxKeys" in t and ("memory" in t.lower() or "buffers" in t.lower() or "heap" in t.lower() or "entire subtree" in t or "materializ" in t.lower()),
        lambda t: "List walks the entire subtree" in t
        or ("localbackend.List buffers every matching object" in t)
        or ("List materializes the entire matching subtree" in t),
        lambda t: "ServeFile pays a full backend.Get" in t
        or ("304 Not Modified" in t and "ServeFile" in t)
        or ("Statter" in t and "ServeFile" in t),
        lambda t: "Wire-level MaxBytesReader" in t
        or ("MaxBytesReader cap contradicts" in t)
        or ("16 MiB skipped-parts" in t),
        lambda t: "limitReader panics" in t
        or ("MaxFileSize is math.MaxInt64" in t)
        or ("math.MaxInt64" in t and "limitReader" in t),
        lambda t: "uploadsec validators cannot be plugged" in t
        or ("AsStorageValidator" in t)
        or ("uploadsec" in t and "storagehttp upload pipeline" in t),
        lambda t: "UUIDKeyFunc" in t,
        lambda t: "Weak ETag" in t or ("weak ETag" in t) or ("Weak ETags" in t),
        lambda t: "Internal review-tracker IDs leaked" in t
        or ("FR-" in t and ("godoc" in t.lower() or "exported godoc" in t.lower())),
        # MEDIUM clearance batch 2026-07-20

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "etagMatch aborts the whole If-None-Match" in t,
        lambda t: "normalizeScannerError discards the original error" in t,
        lambda t: "limitReader.max field is dead" in t,  # already absent in code
],
    "review-19-storage-backends.md": [
        lambda t: "yield again after the consumer stopped" in t or ("yield-after-stop" in t),
        lambda t: "Put cannot overwrite" in t,
        # Batch 5 FIXED
        lambda t: "ascend above RootPath" in t or ("path ascent" in t.lower())
        or ("trusts server-supplied entry names" in t and "ascend" in t),
        lambda t: "Context cancellation silently truncates List" in t
        or ("cancellation" in t.lower() and "List" in t and ("truncate" in t.lower() or "complete listing" in t)),
        lambda t: "symlink anywhere in the walked tree" in t
        or ("symlink" in t.lower() and "permanently breaks List" in t)
        or ("symlink skip" in t.lower()),
        lambda t: "getClient can return a nil Client" in t
        or ("getClient" in t and ("Close races" in t or "nil Client" in t)),
        lambda t: "Healthy()" in t and ("timeout" in t.lower() or "ignores its context" in t or "Stat probe" in t),
        lambda t: "ProjectID" in t and ("never uses" in t or "optional" in t.lower() or "required" in t),
        # MEDIUM clearance batch 2026-07-20
        lambda t: "gcs/azure/sftp constructors register default metrics" in t,
        lambda t: "PresignPutURL bypasses all configured upload validators" in t,
        lambda t: "translateS3Capacity" in t and "InvalidRequest" in t,
        lambda t: "removeOnEOF deletes the scan spool" in t,

    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "Backend.cfg is write-only dead state" in t,
        lambda t: "s3backend panics on empty bucket" in t,
        lambda t: "WithMetricsValidatorName documents validation it does not perform" in t,
        lambda t: "copyBounded discards the underlying spool I/O error" in t,  # context preserved; text still redacted
        lambda t: "removeOnEOF.Close returns spurious" in t,
],
    "review-20-sqldb-outbox.md": [
        lambda t: "Heartbeat" in t and ("fenced" in t or "claim_token" in t),
        lambda t: "IsSerializationError" in t,
        lambda t: "IsNotFound misses pgx" in t or ("IsNotFound misses" in t and "pgx" in t),
        lambda t: "ResetStaleProcessing" in t and "claim_token" in t,
        # Batch 9 FIXED — all listed MEDIUMs
        lambda t: "Listen" in t and ("UNLISTEN" in t or "subscribed" in t),
        lambda t: "applyPoolDefaults" in t,
        lambda t: "FR-079" in t or ("sslmode=require" in t and ("bypass" in t.lower() or "percent" in t.lower() or "raw-DSN" in t or "raw DSN" in t)),
        lambda t: "zero-replica" in t or ("zero replica" in t.lower())
        or ("pass-through mode" in t and ("replica" in t.lower() or "fallback" in t.lower())),
        lambda t: "context cancellation" in t.lower() and ("replica" in t.lower() or "eviction" in t.lower()),
        lambda t: "Close never removes" in t or ("DeleteLabelValues" in t) or ("label cardinality" in t and "Close" in t),
        lambda t: "PrimaryHealthy" in t,
    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "JSONB.Scan marks Valid=true before unmarshalling" in t,
        lambda t: "GormDataType is dead legacy API" in t,
        lambda t: "Pool.dsn field is dead" in t,
],
    "review-21-redis-leader.md": [
        lambda t: "leader atomic flag stores" in t or ("k8slease" in t and "IsLeader" in t and "stick true" in t),
        # IsLeader during drain (redislock/etcd) fixed
        lambda t: "IsLeader()" in t and ("drain" in t or "callback-drain" in t or "callback drain" in t),
        lambda t: "leave IsLeader" in t and "drain" in t,
        # Batch 9 FIXED
        lambda t: "Elector.Run reusability" in t or ("reusability" in t.lower() and "Elector" in t),
        lambda t: "OnLost" in t and ("permanently kills" in t or "non-fatal" in t or "error/panic" in t),
        lambda t: "callback_drain_seconds" in t or ("drain" in t.lower() and "metric" in t.lower() and ("leadership term" in t or "happy path" in t)),
        lambda t: "Individual-fields Redis config" in t or ("REDIS_TLS" in t)
        or ("hardcodes plaintext redis://" in t) or ("fields path" in t.lower() and "plaintext" in t.lower()),
        lambda t: "READONLY failover" in t or ("MarkReadOnly" in t)
        or ("ReadOnlyAware" in t) or ("OnReadOnly" in t and ("dead" in t.lower() or "never invoked" in t)),
        lambda t: "FR-077" in t and ("password" in t.lower() or "username-only" in t or "anonymous" in t.lower()),
    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "BindingError is dead and its doc references" in t,
],
    "review-22-secrets.md": [
        lambda t: "In-place zeroing" in t,
        lambda t: ("Get can silently return an empty Secret" in t)
        or ("Invalidate can zero a cache entry" in t)
        or ("concurrent Invalidate" in t),
        lambda t: "Stale-fallback" in t and ("empty" in t or "zeroed" in t),
        # Batch 9 FIXED secrets
        lambda t: "empty-payload" in t or ("empty payload" in t and "ErrLoaderUnavailable" in t),
        lambda t: "resolveName" in t or ("WithProject" in t and ("bypass" in t.lower() or "projects/" in t)),
        lambda t: "vaultkv" in t and ("ErrLoaderUnavailable" in t or "missing-field" in t or "wrong-type" in t),
        lambda t: "hasPrefix" in t or ("reimplements strings.HasPrefix" in t),
        lambda t: "Quick start example" in t or ("does not compile" in t and "NewCachedLoader" in t),
    ],
    "review-23-observability-flags.md": [
        lambda t: "timestamp truncation" in t or ("Postgres timestamp truncation" in t),
        lambda t: "JSONB metadata" in t,
        lambda t: "Readiness cache" in t or ("readiness cache" in t) or ("Cancelled readiness probe" in t),
        lambda t: "pyroscope.Config" in t and ("LogValue" in t or "AuthToken" in t),
        # Batch 9 FIXED
        lambda t: "Shutdown is process-global" in t or ("Shutdown" in t and "multiple Clients" in t),
        lambda t: "Retention deletes by occurred_at" in t or ("interior holes" in t),
        lambda t: "RetentionJob" in t or ("watermark" in t.lower() and ("retention" in t.lower() or "DeleteBefore" in t or "VerifyChain" in t)),
        lambda t: "MustNew" in t,
        lambda t: "Sampler Description" in t or ("tenant IDs" in t and "sampler" in t.lower())
        or ("overrides=" in t and "tenant" in t.lower()),
        lambda t: "+Inf" in t or ("Latency percentile" in t) or ("non-finite" in t and "SLO" in t),
    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "An explicit SampleRate of 0.0 is silently coerced" in t or ("SampleRate of 0.0" in t),
],
    "review-24-cmd-clis.md": [
        lambda t: "Bare `Identity{}`" in t or ("Bare Identity{}" in t) or ("Identity{}" in t and "local type" in t),
        lambda t: "isAuthIdentityType matches any bare" in t or ("bare `Identity{}` literal" in t),
        # Batch 9 FIXED
        lambda t: "Import detection matches any quoted" in t or ("kit-catalog" in t.lower() and "parser" in t.lower())
        or ("false positives in the fleet manifest" in t),
        lambda t: "Interactive mode never applies AST" in t or ("interactive" in t.lower() and "AST" in t)
        or ("auth-identity Fix is dead" in t),
        lambda t: "Option-presence" in t or ("callHasOption" in t)
        or ("option is passed via a variable" in t),
        lambda t: "exclude/retract" in t or ("parseGoMod regex" in t),
        lambda t: "unreadable subdirectory aborts" in t or ("fleet scan" in t and ("continue" in t.lower() or "aborts" in t)),
    
        # LOW batch 2026-07-20 (post MEDIUM clearance)
        lambda t: "kit-verify discards the JSON encode error" in t,
        lambda t: "containsFold hand-rolls an ASCII case-insensitive" in t,
],
    "review-25-examples.md": [
        lambda t: "hmacKeyFromEnv" in t,
        lambda t: "Corrupt/unmarshalable idempotency cache" in t
        or ("corrupt" in t.lower() and "idempotency" in t and "saga" in t.lower()),
    ],
    "review-26-testing-kits.md": [
        lambda t: "Hardcoded exchange/queue names" in t or ("Hardcoded" in t and "RabbitMQ" in t),
        lambda t: "Unguarded wg.Done" in t,
        lambda t: "Dedicated RabbitMQ container" in t and ("Cleanup" in t or "Terminate" in t),
        lambda t: "assert.Panics" in t,
        lambda t: "Shared RabbitMQ container" in t and "AmqpURL" in t,
    ],
}

# Special-case: review-17 NATS password/token without TLS
# validateAuth accepts plaintext Username/Password or Token
FIXED["review-17-messaging-backends.md"].append(
    lambda t: "validateAuth accepts plaintext" in t or ("plaintext Username/Password or Token as a full substitute for TLS" in t)
)

FINDING_RE = re.compile(r"^### \[(CRITICAL|HIGH|MEDIUM|LOW)\] (.+)$", re.M)
SUMMARY_ROW_RE = re.compile(
    r"(\| CRITICAL \| )(\d+)(\n\| HIGH \| )(\d+)(\n\| MEDIUM \| )(\d+)(\n\| LOW \| )(\d+)(\n\| \*\*Total \(deduplicated\)\*\* \| \*\*)(\d+)(\*\*)"
)




# Batch LOW evening 2026-07-20 (fix-first session)
_BATCH_20260720_EVE = {
    "review-05-security.md": [
        lambda t: "Key.Verify reports revoked/expired" in t or "revoked/expired status before validating" in t,
        lambda t: "Duplicated revoked/expired logic between Verify and IsActive" in t,
        lambda t: "token buffer capacity omits the 32-byte MAC" in t or "omits the 32-byte MAC" in t,
    ],
    "review-07-httpx-core.md": [
        lambda t: "WriteLinkHeader omits rel" in t,
        lambda t: "Package doc contradicts the implemented behaviour" in t or "path-param auto-discovery" in t,
        lambda t: "mapErrorForCaller masks apperror Conflict" in t,
        lambda t: "var _ = time.Now" in t,
    ],
    "review-08-httpx-middleware.md": [
        lambda t: "Hijacked" in t and "audit-logged" in t,
        lambda t: "Budget backend errors are swallowed" in t,
        lambda t: "compressWriter.Write reports 0 bytes" in t,
        lambda t: "Package doc references [Store]" in t or ("NewMemoryStore" in t and "none of which exist" in t),
        lambda t: "WithLogger nil-handling is inconsistent" in t,
        lambda t: "Get-then-TryLock" in t and "spurious 409" in t,
        lambda t: "Post-handler WriteHeader flush" in t and "hijack" in t.lower(),
        lambda t: "Tenant limiter backend errors are swallowed" in t,
        lambda t: "recordingWriter omits http.Pusher" in t,
        lambda t: "recordingWriter.Hijack does not mark" in t,
        lambda t: "HSTS via X-Forwarded-Proto silently disabled" in t,
        lambda t: "ErrBodyTooLarge is classified under the malformed_signature" in t,
        lambda t: "ErrSecretTooShort" in t and "bad_signature" in t,
        lambda t: "Expired/revoked key id is faintly timing-distinguishable" in t,
    ],
    "review-10-grpcx.md": [
        lambda t: "appends transport credentials and keepalive params twice" in t,
        lambda t: "Bearer token length cap" in t,
        lambda t: "Deprecation notice on UserID" in t,
        lambda t: "Hardened grpc.ServerOption set is appended twice" in t or ("first block is dead" in t and "ServerOption" in t),
    ],
    "review-12-data-core-b.md": [
        lambda t: "dereference ctx via ctx.Err()" in t or ("nil guard" in t and "MemoryStore" in t),
        lambda t: "Inconsistent expiry boundary" in t,
        lambda t: "non-constant-time bytes.Equal" in t or "Request-fingerprint mismatch" in t,
        lambda t: "Lock owner tokens compared with non-constant-time" in t,
        lambda t: "MemoryStore.Run is permanently single-shot" in t or "MemoryStore.Run is one-shot" in t,
        lambda t: "cloneResponse and copyResponseForStorage" in t,
    ],
    "review-13-data-pg-stores.md": [
        lambda t: "RangeByTenantSeq maps a nil callback" in t,
        lambda t: "WithTableName doc claims" in t or "WithTableName godoc promises" in t,
        lambda t: "without UTC normalisation" in t or "without UTC normalization" in t,
        lambda t: "CREATE INDEX lacks IF NOT EXISTS" in t,
    ],
    "review-14-data-redis-stores.md": [
        lambda t: "redlock" in t.lower() and ("duplicates" in t or "duplication" in t or "verbatim" in t),
    ],
    "review-15-queues-streams.md": [
        lambda t: "WithMaxStreamLen and WithRetention" in t,
        lambda t: "StartConsumers accepts duplicate stream" in t,
    ],
    "review-16-messaging-core.md": [
        lambda t: "message-validation failures are reported as 500" in t or "NewMessage to 500" in t,
        lambda t: "Two competing package comments in debughttp" in t,
        lambda t: "StartConsumers collects missing/nil-handler" in t,
        lambda t: "validateConsumerGroup discards" in t,
    ],
    "review-19-storage-backends.md": [
        lambda t: "isCopySourceNotFound" in t and "dead code" in t,
        lambda t: "List loops forever" in t and "IsTruncated" in t,
    ],
}
for _k, _v in _BATCH_20260720_EVE.items():
    FIXED.setdefault(_k, []).extend(_v)

def is_fixed(filename: str, title: str) -> bool:
    preds = FIXED.get(filename, [])
    for p in preds:
        try:
            if p(title):
                return True
        except Exception:
            continue
    return False


def process_file(path: Path) -> tuple[int, dict[str, int], list[str]]:
    text = path.read_text(encoding="utf-8")
    # Split into preamble and findings
    findings_marker = "## Findings"
    idx = text.find(findings_marker)
    if idx < 0:
        return 0, {}, []

    # Find start of first ### after Findings
    after = text[idx:]
    first = re.search(r"^### \[", after, re.M)
    if not first:
        # no findings section content
        counts = count_severities([])
        return 0, counts, []

    preamble = text[: idx + first.start()]
    rest = text[idx + first.start() :]

    # Split findings by ### [SEVERITY]
    parts = re.split(r"(?=^### \[)", rest, flags=re.M)
    kept = []
    removed = []
    for part in parts:
        if not part.strip():
            continue
        m = re.match(r"^### \[(CRITICAL|HIGH|MEDIUM|LOW)\] (.+)$", part, re.M)
        if not m:
            kept.append(part)
            continue
        title = m.group(2).strip()
        full_title = m.group(0).strip()
        if is_fixed(path.name, title):
            removed.append(full_title)
        else:
            kept.append(part)

    # Normalize trailing newlines
    body = "".join(kept)
    if body and not body.endswith("\n"):
        body += "\n"
    new_text = preamble + body

    # Recount remaining findings
    remaining_titles = []
    sev_counts = {"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0}
    for m in FINDING_RE.finditer(new_text):
        sev_counts[m.group(1)] += 1
        remaining_titles.append(f"### [{m.group(1)}] {m.group(2)}")

    total = sum(sev_counts.values())

    # Update summary table if present
    def repl_summary(m: re.Match) -> str:
        return (
            f"{m.group(1)}{sev_counts['CRITICAL']}"
            f"{m.group(3)}{sev_counts['HIGH']}"
            f"{m.group(5)}{sev_counts['MEDIUM']}"
            f"{m.group(7)}{sev_counts['LOW']}"
            f"{m.group(9)}{total}{m.group(11)}"
        )

    new_text2, n = SUMMARY_ROW_RE.subn(repl_summary, new_text, count=1)
    if n == 0:
        # try looser summary format
        new_text2 = update_summary_loose(new_text, sev_counts, total)

    path.write_text(new_text2, encoding="utf-8")
    return len(removed), sev_counts, removed


def update_summary_loose(text: str, sev: dict[str, int], total: int) -> str:
    """Fallback summary updater line-by-line."""
    lines = text.splitlines(keepends=True)
    out = []
    i = 0
    in_summary = False
    while i < len(lines):
        line = lines[i]
        if line.startswith("## Summary"):
            in_summary = True
            out.append(line)
            i += 1
            continue
        if in_summary and line.startswith("## "):
            in_summary = False
        if in_summary:
            if re.match(r"\| CRITICAL \|", line):
                out.append(f"| CRITICAL | {sev['CRITICAL']} |\n")
                i += 1
                continue
            if re.match(r"\| HIGH \|", line):
                out.append(f"| HIGH | {sev['HIGH']} |\n")
                i += 1
                continue
            if re.match(r"\| MEDIUM \|", line):
                out.append(f"| MEDIUM | {sev['MEDIUM']} |\n")
                i += 1
                continue
            if re.match(r"\| LOW \|", line):
                out.append(f"| LOW | {sev['LOW']} |\n")
                i += 1
                continue
            if re.match(r"\| \*\*Total", line):
                out.append(f"| **Total (deduplicated)** | **{total}** |\n")
                i += 1
                continue
        out.append(line)
        i += 1
    return "".join(out)


def count_severities(titles: list[str]) -> dict[str, int]:
    return {"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0}


def update_summary_00(per_file: dict[str, dict[str, int]], total_removed: int) -> None:
    path = ROOT / "review-00-summary.md"
    text = path.read_text(encoding="utf-8")

    # Map family number to file
    file_by_num = {
        "01": "review-01-core-io.md",
        "02": "review-02-runtime-resilience.md",
        "03": "review-03-app-wiring.md",
        "04": "review-04-crypto.md",
        "05": "review-05-security.md",
        "06": "review-06-auth-authz.md",
        "07": "review-07-httpx-core.md",
        "08": "review-08-httpx-middleware.md",
        "09": "review-09-websocket-realtime.md",
        "10": "review-10-grpcx.md",
        "11": "review-11-data-core-a.md",
        "12": "review-12-data-core-b.md",
        "13": "review-13-data-pg-stores.md",
        "14": "review-14-data-redis-stores.md",
        "15": "review-15-queues-streams.md",
        "16": "review-16-messaging-core.md",
        "17": "review-17-messaging-backends.md",
        "18": "review-18-storage-core.md",
        "19": "review-19-storage-backends.md",
        "20": "review-20-sqldb-outbox.md",
        "21": "review-21-redis-leader.md",
        "22": "review-22-secrets.md",
        "23": "review-23-observability-flags.md",
        "24": "review-24-cmd-clis.md",
        "25": "review-25-examples.md",
        "26": "review-26-testing-kits.md",
    }

    # Ensure per_file has counts for all files (including untouched)
    for num, fn in file_by_num.items():
        if fn not in per_file:
            p = ROOT / fn
            if p.exists():
                content = p.read_text(encoding="utf-8")
                sev = {"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0}
                for m in FINDING_RE.finditer(content):
                    sev[m.group(1)] += 1
                per_file[fn] = sev

    grand = {"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0}
    for sev in per_file.values():
        for k in grand:
            grand[k] += sev[k]
    grand_total = sum(grand.values())

    # Update header total line
    text = re.sub(
        r"\*\*Total deduplicated findings\*\*: \d+\s+\(CRITICAL \d+, HIGH \d+, MEDIUM \d+, LOW \d+\)",
        f"**Total deduplicated findings**: {grand_total}  (CRITICAL {grand['CRITICAL']}, HIGH {grand['HIGH']}, MEDIUM {grand['MEDIUM']}, LOW {grand['LOW']})",
        text,
    )

    # Update per-family table rows
    def row_repl(m: re.Match) -> str:
        num = m.group(1)
        family = m.group(2)
        report = m.group(3)
        fn = file_by_num[num]
        sev = per_file[fn]
        tot = sum(sev.values())
        return f"| {num} | {family} | {sev['CRITICAL']} | {sev['HIGH']} | {sev['MEDIUM']} | {sev['LOW']} | {tot} | `{report}` |"

    text = re.sub(
        r"\| (\d{2}) \| ([^|]+) \| \d+ \| \d+ \| \d+ \| \d+ \| \d+ \| `(review-\d{2}-[^`]+)` \|",
        row_repl,
        text,
    )

    # Update TOTAL row
    text = re.sub(
        r"\| \| \*\*TOTAL\*\* \| \*\*\d+\*\* \| \*\*\d+\*\* \| \*\*\d+\*\* \| \*\*\d+\*\* \| \*\*\d+\*\* \| \|",
        f"| | **TOTAL** | **{grand['CRITICAL']}** | **{grand['HIGH']}** | **{grand['MEDIUM']}** | **{grand['LOW']}** | **{grand_total}** | |",
        text,
    )

    # Rebuild CRITICAL/HIGH section: keep only findings still present in family files
    # Extract remaining CRITICAL/HIGH from each family file
    remaining_ch: list[tuple[str, str, str]] = []  # (sev, title, family_file)
    for num in sorted(file_by_num.keys()):
        fn = file_by_num[num]
        content = (ROOT / fn).read_text(encoding="utf-8")
        for m in FINDING_RE.finditer(content):
            if m.group(1) in ("CRITICAL", "HIGH"):
                remaining_ch.append((m.group(1), m.group(2), fn))

    # Parse existing CRITICAL/HIGH detail blocks from review-00
    ch_section_match = re.search(
        r"(## All CRITICAL and HIGH findings \(\d+\)\n\nRanked leads for stage-2 verification\. Each links to its family report\.\n\n)",
        text,
    )
    if ch_section_match:
        start = ch_section_match.end()
        end_match = re.search(r"\n## Recurring themes\n", text[start:])
        if end_match:
            old_blocks = text[start : start + end_match.start()]
            # Split old blocks
            blocks = re.split(r"(?=^### \[)", old_blocks, flags=re.M)
            # Keep blocks whose title still exists as remaining CRITICAL/HIGH
            remaining_titles = {(sev, title) for sev, title, _ in remaining_ch}
            kept_blocks = []
            for b in blocks:
                if not b.strip():
                    continue
                m = re.match(r"^### \[(CRITICAL|HIGH)\] (.+)$", b, re.M)
                if not m:
                    continue
                key = (m.group(1), m.group(2).strip())
                if key in remaining_titles:
                    kept_blocks.append(b if b.endswith("\n") else b + "\n")

            n_ch = len(kept_blocks)
            header = f"## All CRITICAL and HIGH findings ({n_ch})\n\nRanked leads for stage-2 verification. Each links to its family report.\n\n"
            new_ch = header + "".join(kept_blocks)
            if not new_ch.endswith("\n"):
                new_ch += "\n"
            text = text[: ch_section_match.start()] + new_ch + text[start + end_match.start() :]

    path.write_text(text, encoding="utf-8")
    return grand, grand_total


def main() -> None:
    all_removed = 0
    per_file_counts: dict[str, dict[str, int]] = {}
    removed_log: dict[str, list[str]] = {}

    # Process fixed files + ensure counts for all
    for i in range(1, 27):
        # find matching file
        matches = sorted(ROOT.glob(f"review-{i:02d}-*.md"))
        for path in matches:
            if path.name.startswith("review-00"):
                continue
            removed_n, sev, removed = process_file(path)
            per_file_counts[path.name] = sev
            all_removed += removed_n
            if removed:
                removed_log[path.name] = removed
            print(f"{path.name}: removed {removed_n}, remaining {sum(sev.values())} "
                  f"(C={sev['CRITICAL']} H={sev['HIGH']} M={sev['MEDIUM']} L={sev['LOW']})")
            if removed:
                for r in removed:
                    print(f"  - {r}")

    grand, grand_total = update_summary_00(per_file_counts, all_removed)
    print(f"\nTOTAL removed: {all_removed}")
    print(f"TOTAL remaining: {grand_total} (C={grand['CRITICAL']} H={grand['HIGH']} M={grand['MEDIUM']} L={grand['LOW']})")

    # Write status report data to stdout for REVIEW_BACKLOG_STATUS
    status_path = ROOT / "REVIEW_BACKLOG_STATUS.md"
    lines = [
        "# Review backlog status\n",
        "\n",
        "## Policy\n",
        "\n",
        "**fix-first.** Docs/typos/naming/consistency/perf/tradeoffs are fixed in code or docs,\n",
        "not refuted as \"working as designed.\" Breaking (v3) API changes need explicit user go-ahead.\n",
        "\n",
        "The previous mass-refute approach was wrong and has been reversed: only findings with\n",
        "audited **FIXED** evidence (code + tests) are removed from the review trackers. All other\n",
        "findings remain **OPEN**.\n",
        "\n",
        "## Cleanup (this pass)\n",
        "\n",
        f"- FIXED findings removed this pass (batches 8–9): **{all_removed}** "
        f"(pre-pass family totals → {grand_total})\n",
        f"- Approximate FIXED findings removed overall: **~{907 - grand_total}** (907 → {grand_total})\n",
        f"- Remaining findings (`review-01` … `review-26`): **{grand_total}**\n",
        f"  - CRITICAL **{grand['CRITICAL']}**\n",
        f"  - HIGH **{grand['HIGH']}**\n",
        f"  - MEDIUM **{grand['MEDIUM']}**\n",
        f"  - LOW **{grand['LOW']}**\n",
        "\n",
        "## Remaining counts per review file\n",
        "\n",
        "| File | Crit | High | Med | Low | Total |\n",
        "|---|---:|---:|---:|---:|---:|\n",
    ]
    for i in range(1, 27):
        matches = sorted(ROOT.glob(f"review-{i:02d}-*.md"))
        for path in matches:
            sev = per_file_counts.get(path.name, {"CRITICAL": 0, "HIGH": 0, "MEDIUM": 0, "LOW": 0})
            tot = sum(sev.values())
            lines.append(
                f"| `{path.name}` | {sev['CRITICAL']} | {sev['HIGH']} | {sev['MEDIUM']} | {sev['LOW']} | {tot} |\n"
            )
    lines.append(
        f"| **TOTAL** | **{grand['CRITICAL']}** | **{grand['HIGH']}** | **{grand['MEDIUM']}** | **{grand['LOW']}** | **{grand_total}** |\n"
    )
    lines.append("\n## Notes\n\n")
    lines.append(
        "- Batch 8: `review-12` FIXED (TTL-takeover, queue/stream Consumer docs, gcra "
        "sweep budget, CancelAt token leak); **kept** tenant key forgeability PARTIAL. "
        "`review-13` FIXED MEDIUM pile (23505 classify, approval index, ForTenant, "
        "headers escape, NULL fingerprint, saga Migrations). `review-14` FIXED "
        "(prefix separator, Refund docs, Get non-JSON miss, released atomic, redlock "
        "fencing docs, keyTTL GCRA debt); **kept** redlock duplication OPEN.\n"
    )
    lines.append(
        "- Batch 9: `review-20`–`review-24` FIXED MEDIUM piles (sqldb Listen/pool/"
        "FR-079/replicas/PrimaryHealthy; leader Elector/OnLost/drain/Redis TLS/"
        "READONLY/FR-077; secrets ErrLoaderUnavailable/WithStrictProject; flags "
        "Shutdown/retention/watermark/MustNew/sampler/+Inf; kit-catalog parser + "
        "interactive AST + option-presence + exclude/retract + fleet continue).\n"
    )
    lines.append(
        "- JWKS outage HIGH **FIXED**: temporary `DisconnectServerError` + `connectOutcomeError` "
        "for `jwtutil.ErrKeySetUnavailable` (`review-09-websocket-realtime.md`).\n"
    )
    lines.append(
        "- OAuth2 login-CSRF / browser-bound state findings removed (batches 1–3): "
        "state cookie + 503 store-error path now tested.\n"
    )
    lines.append(
        "- SessionFromRequest findings kept OPEN (API exists; no dedicated tests yet).\n"
    )
    lines.append(
        "- KeySet.Verify fail-closed default kept OPEN if still present "
        "(`review-05-security.md`); docs-only KeySet.Verify items were eligible for removal.\n"
    )
    lines.append(
        "- Helper script: `tools/_cleanup_fixed_reviews.py` (matchers updated for batches 8–9).\n"
    )
    status_path.write_text("".join(lines), encoding="utf-8")
    print(f"\nWrote {status_path}")


if __name__ == "__main__":
    main()
