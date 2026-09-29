// The kit's core module bundles primitives every consumer needs:
// apperror, clock, config, contextutil, randstr, safecast, secret,
// tenant, validate. v2 collapsed these from per-package modules into
// one because the dependency footprint is uniformly stdlib-only or
// near-stdlib, and every consumer ends up importing several of them
// regardless. See AGENTS.md "Module shape" for the consolidation map.
module github.com/bds421/rho-kit/core/v2

go 1.26.8

require (
	github.com/fsnotify/fsnotify v1.10.1
	github.com/google/jsonschema-go v0.4.3
	github.com/google/uuid v1.6.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/stretchr/testify v1.12.1
)

require (
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)
