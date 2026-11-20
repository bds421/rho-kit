// Package pgtemplate gives each test a PostgreSQL database copied from a
// template migrated once per state of the migrations, so a test pays for
// a copy, not for running every migration.
//
// A cluster keeps one template per [Template.Prefix] and hash. The first
// [Template.Clone] that finds none builds it under an exclusive advisory
// lock and drops the templates of other hashes under the prefix; copies
// hold the lock shared, so no template is built or dropped while it is
// copied, and every test binary run against the cluster shares one
// template. [Hash] names the state from the migration files and the
// versions of the modules whose code migrates.
package pgtemplate
