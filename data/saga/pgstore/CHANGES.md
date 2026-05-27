# Changes

## Unreleased — v2.0

- Initial release. Implements saga.StateStore against Postgres.
- Put routes to two distinct SQL paths based on Instance.UpdatedAt:
  - Zero → INSERT…ON CONFLICT DO NOTHING (first-write, never overwrites)
  - Non-zero → UPDATE…WHERE updated_at=$old (strict optimistic
    concurrency)
  No NULL escape — a misbehaving caller cannot bypass the concurrency
  check by passing a fresh Instance{} with an existing ID. (Replaces
  the original single INSERT…ON CONFLICT DO UPDATE with `OR $9 IS NULL`
  which had that escape.)
- Partial index on (state, updated_at) covers ListResumable.
- Migration `20260601000002_create_saga_instances.sql` ships in
  `migrations/`.
