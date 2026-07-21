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

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.5.0
	github.com/open-feature/go-sdk v1.17.2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	go.uber.org/mock v0.6.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
