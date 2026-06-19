# Changes

## Unreleased — v2.0

- Initial release. Implements saga.StateStore against Postgres.
- Put routes to two distinct SQL paths based on Instance.UpdatedAt:
  - Zero → INSERT…ON CONFLICT DO NOTHING (first-write, never overwrites;
    a collision returns ErrConcurrentUpdate)
  - Non-zero → UPDATE the row in place by ID (overwrites mutable
    columns, matching the "writes (or overwrites)" StateStore contract;
    a vanished row returns ErrConcurrentUpdate)
  The UPDATE path does NOT gate on updated_at: DurableExecutor reads an
  instance once via Get and then Puts repeatedly without re-reading, so
  a stale snapshot would otherwise fail every multi-step saga with a
  spurious ErrConcurrentUpdate. (An earlier draft gated on
  `WHERE updated_at=$old`, which broke every multi-step saga on
  Postgres while staying invisible to the UpdatedAt-ignoring memory
  store.)
- Partial index on (state, updated_at) covers ListResumable.
- Migration `20260601000002_create_saga_instances.sql` ships in
  `migrations/`.
