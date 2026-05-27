# Changes

## Unreleased — v2.0

- Initial release. Implements saga.StateStore against Postgres.
- Put uses optimistic-concurrency check (updated_at) so multiple
  replicas can safely share an instance pool.
- Partial index on (state, updated_at) covers ListResumable.
- Migration `20260601000002_create_saga_instances.sql` ships in
  `migrations/`.
