// codec/zstd decodes zstd from untrusted senders within size and ratio
// limits; it owns the klauspost/compress dependency so other modules stay
// free of it.
module github.com/bds421/rho-kit/codec/zstd/v2

go 1.26.8

require github.com/klauspost/compress v1.19.2
