# Production reference resilience harness

Run the harness with Docker available:

```bash
go test -tags integration ./testing/integrationtest/outboxpg
```

It uses real Postgres and RabbitMQ. The bounded test contexts deliberately
turn a stalled dependency or broker lifecycle into a test failure rather than
an indefinite CI wait.

The focused route-selection proof can also run against an explicitly prepared
native PostgreSQL database without Docker or RabbitMQ:

```bash
# Set a protected DSN for a new empty loopback database named rho_outbox_claims_<unique>.
RHO_OUTBOX_NATIVE_DATABASE_ISOLATED=1 go test \
    -run '^TestFetchPending_ClaimRoutesNative$' \
    ./testing/integrationtest/outboxpg/claim_routes_native_test.go \
    ./testing/integrationtest/outboxpg/postgres_helpers_test.go
```

Supply `RHO_OUTBOX_NATIVE_DATABASE_URL` through the test environment. The test
applies the maintained migration sequence, creates no cluster roles, and never
starts a provider. Its caller must remove the unique database afterwards. It
proves exact topic/key/type/schema tuple matching before the limit, unchanged
foreign rows across claim, stale-reset and retention operations, scoped counts,
due-time selection, FIFO, concurrent replica claims, literal legacy-schema
selection, ABA ownership fencing, cancellation cleanup, and absence of row locks
on excluded recipients while an eligible claim is held in flight.

The supplied database must be empty and disposable. The fixture creates a
database-local advisory-lock trigger to hold a claim, joins all goroutines and
closes pools; its caller must verify cleanup and drop the database. Running this
file-list command compiles and runs only the native fixture and shared helper,
without importing or running the other Docker-backed tests. The equivalent
package selector uses `-tags integration,nativepostgres` when those test
dependencies are already installed.

| Failure / recovery condition | Executable proof | Operator-visible outcome |
| --- | --- | --- |
| Duplicate delivery and concurrent replicas | `TestInbox_AMQPAckAfterCommittedDeduplication`, `TestInboxProcess_ConcurrentReplicasSerializeOneHandler` | One domain row, one inbox receipt, one outbox event; both broker deliveries ACK only after commit. |
| Failed local work | `TestInbox_AMQPFailedWorkRedelivers` | No receipt/effect on rollback; retry topology redelivers and the later successful transaction ACKs. |
| Poison / schema-invalid event | `TestInbox_AMQPSchemaMismatchGoesToDLQ` | No inbox receipt; one retry then the event is retained in the RabbitMQ dead queue. |
| Relay crash or deployment shutdown during publish | `TestResetStaleProcessing_RecoversCrashedRelay`, `TestRelay_ResetsClaimedEntriesOnShutdown` | Pending event becomes publishable again rather than remaining stuck in `processing`. |
| Transactional atomicity | `TestInboxProcess_AtomicWithDomainAndOutbox`, `TestInboxProcessInTx_CallerRollbackLeavesNoReceipt` | Receipt, domain mutation, and outbox entry commit together or none remain. |
| Migration contention / ordering | `openAndMigrate` runs the published migration sequence before every case | The current inbox and outbox schema is applied through the production goose entrypoint before any consumer is started. |
| Expired or rotated authentication material | `security/jwtutil.TestVerify_ExpiredToken`, `security/netutil.TestFilesCertificateSource_PollingPicksUpRotation`, and `auth/oauth2/redis.TestBrowserLoginReplicaContinuity_RealRedis` | Expired tokens fail closed; rotated trust material is reloaded; Redis-backed browser state survives a replica change and rejects replay. |

The lower-level `infra/outbox` package suite adds the bounded shutdown reset
case named above. Keeping it there lets every durable backend share the same
relay invariant while this harness proves the Postgres/RabbitMQ composition.
