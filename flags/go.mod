// The kit's flags module wraps the OpenFeature Go SDK with the kit's
// tenant/user context conventions. v2 added this because feature
// flagging needs to be vendor-neutral — services swap LaunchDarkly /
// flagd / GrowthBook / homemade providers without touching call
// sites.
//
// Heavy: pulls the OpenFeature SDK (~stdlib + small deps). Stays in
// its own module so consumers that don't flag-gate code don't pull
// the SDK transitively.
module github.com/bds421/rho-kit/flags/v2

go 1.26.8

require (
	github.com/bds421/rho-kit/core/v2 v2.8.0
	github.com/open-feature/go-sdk v1.19.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	go.uber.org/mock v0.6.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)
