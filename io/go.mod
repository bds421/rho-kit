// The kit's io module bundles tiny stdlib-only utilities: atomicfile
// (atomic file rename) and progress (rate-limited progress callbacks).
// Both are paired and stdlib-only, so collapsing them removes module
// sprawl without changing dep weight. See AGENTS.md "Module shape".
module github.com/bds421/rho-kit/io/v2

go 1.26.8

require github.com/stretchr/testify v1.12.1

require go.yaml.in/yaml/v3 v3.0.5 // indirect
