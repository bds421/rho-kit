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

go 1.26.8

require (
	github.com/bds421/rho-kit/core/v2 v2.8.0
	github.com/stretchr/testify v1.12.1
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect
