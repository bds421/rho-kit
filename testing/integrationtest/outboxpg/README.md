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
RHO_OUTBOX_NATIVE_DATABASE_ISOLATED=1 go test -tags 'integration,nativepostgres' \
    -run '^TestFetchPending_ClaimRoutesNative$' ./testing/integrationtest/outboxpg
```

Supply `RHO_OUTBOX_NATIVE_DATABASE_URL` through the test environment. The test
applies the maintained migration sequence, creates no cluster roles, and never
starts a provider. Its caller must remove the unique database afterwards. It
proves exact pair matching before the limit, unchanged deferred rows, due-time
selection, FIFO, concurrent replica claims, legacy selection, and absence of row
locks on excluded recipients while an eligible claim is held in flight.

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
