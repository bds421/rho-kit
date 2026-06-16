// Package pgstore persists cron schedules to Postgres so an operator
// (or a control-plane API) can add / remove / disable jobs without a
// service restart, and so a service restart does not lose schedule
// state.
//
// # Use this package when
//
//   - You want operators to edit schedules at runtime via SQL or a
//     control-plane endpoint without redeploying.
//   - You have a fleet of replicas and want all of them to converge on
//     the same active schedule set.
//
// # Do NOT use this package for
//
//   - Single-process daemons with hard-coded schedules. Use
//     [runtime/cron.Scheduler] directly — adding pgx for one
//     `@hourly` job is overkill.
//   - Storing the job *function*. Functions don't serialize; this
//     store records (name, schedule, enabled) tuples only. The caller
//     supplies a map[name]func at startup and [Store.ApplyTo] wires
//     enabled records to their handler.
//
// # Schema
//
// `cron_schedules` table — apply via the migration shipped in
// `migrations/` (the kit's existing migration runner reads them).
//
// # Quick start
//
//	jobs := map[string]pgstore.JobFunc{
//	    "nightly-cleanup": cleanup,
//	    "hourly-report":   reportFn,
//	}
//
//	store := pgstore.New(db)
//	unknown, err := store.ApplyTo(ctx, scheduler, jobs)
//	if err != nil {
//	    return err
//	}
//	// unknown lists stored schedules whose name is absent from jobs.
//
//	// Operator adds a third schedule via SQL or admin handler:
//	//   INSERT INTO cron_schedules (name, spec, enabled)
//	//   VALUES ('weekly-vacuum', '0 4 * * 0', true);
//	// On next scheduler restart, ApplyTo picks it up. Hot-reload
//	// is the caller's choice (poll the store + diff + Re-Add).
//
// # Hot reload (optional)
//
// The Store does NOT poll the database on its own. If you want
// schedule changes to apply without a restart, run a control-plane
// ticker that calls [Store.List], compares against the last applied
// set, and Add/Remove on the live Scheduler.
package pgstore
