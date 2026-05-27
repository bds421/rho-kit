# Changes

## Unreleased — v2.0

- Initial release.
- Postgres-backed schedule store: Add, Upsert, Remove, Enable, Get, List.
- ApplyTo wires enabled records to a runtime/cron.Scheduler given a
  jobs map; returns stored-but-unknown names for caller warnings.
- Migration `20260601000001_create_cron_schedules.sql` ships in
  `migrations/` for the kit-migrate runner.
- Unit tests cover validators + panic guards; SQL integration belongs
  under `//go:build integration` with infra/sqldb/dbtest.
