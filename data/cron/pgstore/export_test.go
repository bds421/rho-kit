package pgstore

import "github.com/bds421/rho-kit/runtime/v2/cron"

// Test helpers — exported only to _test files via the test build tag.

// ApplyRecords promotes the unexported applyRecords loop so tests can
// exercise the per-record decision (register / skip-unknown /
// skip-invalid) without a live database.
func ApplyRecords(scheduler *cron.Scheduler, records []ScheduleRecord, jobs map[string]JobFunc) []string {
	return applyRecords(scheduler, records, jobs)
}

// ValidateRecord is the unexported validator promoted for tests so we
// can exercise the Name/Spec rules without touching the DB layer.
func ValidateRecord(rec ScheduleRecord) error {
	s := &Store{table: "cron_schedules"}
	return s.validate(rec)
}

// IsValidName exposes the name regex check for unit tests.
func IsValidName(name string) bool {
	return validName.MatchString(name)
}

// IsValidIdent exposes the identifier regex check for unit tests
// (used by WithTableName).
func IsValidIdent(ident string) bool {
	return validIdent.MatchString(ident)
}
