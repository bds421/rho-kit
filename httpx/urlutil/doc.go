// Package urlutil offers safe [*url.URL] join/copy/parse helpers.
//
// Use these instead of [path.Join] for URL paths: [path.Join] strips trailing
// slashes, double-encodes already-percent-encoded segments, and discards
// query/fragment. The helpers here preserve trailing slashes, treat encoded
// segments as opaque, and never mutate their inputs.
//
// asvs: V5.2.5
package urlutil
