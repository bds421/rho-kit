// The kit's core module bundles primitives every consumer needs:
// apperror, clock, config, contextutil, randstr, safecast, secret,
// tenant, validate. v2 collapsed these from per-package modules into
// one because the dependency footprint is uniformly stdlib-only or
// near-stdlib, and every consumer ends up importing several of them
// regardless. See AGENTS.md "Module shape" for the consolidation map.
module github.com/bds421/rho-kit/core/v2

go 1.26.2

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/google/jsonschema-go v0.4.3
	github.com/google/uuid v1.6.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
