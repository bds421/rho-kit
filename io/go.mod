// The kit's io module bundles tiny stdlib-only utilities: atomicfile
// (atomic file rename) and progress (rate-limited progress callbacks).
// Both are paired and stdlib-only, so collapsing them removes module
// sprawl without changing dep weight. See AGENTS.md "Module shape".
module github.com/bds421/rho-kit/io

go 1.26
