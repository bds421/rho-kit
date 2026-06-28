// The kit's authz module defines the [Decider] interface — the
// vendor-neutral seam between handler-level authorization checks and
// the underlying decision engine. v2 added this so the kit doesn't
// grow a custom RBAC/ABAC implementation; engines (OpenFGA, Cedar,
// Casbin, in-memory for tests) plug in via a single interface.
//
// Stays in its own module because the interface + memory adapter
// have no third-party deps; engine adapters live in subdirectories
// (authz/openfga, future authz/cedar) so consumers pull only the
// engine they actually use.
module github.com/bds421/rho-kit/authz/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.3.1
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
