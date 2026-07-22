# Production reference resilience harness

Run the harness with Docker available:

```bash
go test -tags integration ./testing/integrationtest/outboxpg
```

It uses real Postgres and RabbitMQ. The bounded test contexts deliberately
turn a stalled dependency or broker lifecycle into a test failure rather than
an indefinite CI wait.

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
