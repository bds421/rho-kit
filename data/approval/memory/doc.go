// Package memory is a thread-safe in-process [approval.Store] for unit
// tests and small dev environments. Production deployments use
// data/approval/postgres.
//
// The store relies on the caller's clock for the auto-expire decision
// in Decide; injecting a clock keeps the test for "approve a long-
// expired request" hermetic.
package memory
