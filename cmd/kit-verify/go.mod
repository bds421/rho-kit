// Command kit-verify runs the kit's conformance suite against a
// running service: it probes the service's HTTP surface, asserts
// the ASVS controls the kit annotates are actually implemented, and
// emits a machine-readable report.
//
// v2 added this as the operational embodiment of the ASVS contract
// — annotation says "this kit middleware satisfies V2.1.5";
// kit-verify proves the running service exhibits that behaviour
// (e.g., rejects passwords below entropy, returns 401 on missing
// JWT). HSTS-header probing is documented for a future probe set
// and is NOT yet implemented; rely on
// httpx/middleware/secheaders tests for HSTS coverage today.
//
// kit-verify is INTENTIONALLY a separate command from kit-doctor:
// kit-doctor analyses source, kit-verify probes a running binary.
// Different inputs, different failure modes.
module github.com/bds421/rho-kit/cmd/kit-verify/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.6.0
	github.com/bds421/rho-kit/security/v2 v2.6.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
