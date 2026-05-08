// Command kit-verify runs the kit's conformance suite against a
// running service: it probes the service's HTTP surface, asserts
// the ASVS controls the kit annotates are actually implemented, and
// emits a machine-readable report.
//
// v2 added this as the operational embodiment of the ASVS contract
// — annotation says "this kit middleware satisfies V2.1.5";
// kit-verify proves the running service exhibits that behaviour
// (e.g., rejects passwords below entropy, returns 401 on missing
// JWT, sets HSTS headers).
//
// kit-verify is INTENTIONALLY a separate command from kit-doctor:
// kit-doctor analyses source, kit-verify probes a running binary.
// Different inputs, different failure modes.
module github.com/bds421/rho-kit/cmd/kit-verify

go 1.26

require github.com/bds421/rho-kit/security v0.0.0

replace github.com/bds421/rho-kit/core => ../../core

replace github.com/bds421/rho-kit/resilience => ../../resilience

replace github.com/bds421/rho-kit/security => ../../security
