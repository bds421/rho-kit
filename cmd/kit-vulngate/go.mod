// Command kit-vulngate holds a release to the patch deadlines: it reads
// grype's JSON reports of the images and SBOMs that ship and fails when a
// fixed vulnerability is older than its severity allows.
module github.com/bds421/rho-kit/cmd/kit-vulngate/v2

go 1.26.8
