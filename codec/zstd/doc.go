// Package zstd decodes zstd data from untrusted senders within bounds: a
// frame may expand to at most MaxSize bytes and at most MaxRatio times its
// compressed size, a frame declaring more is refused before anything is
// allocated, and at most Concurrency decodes hold memory at once. MaxSize
// is at least MinMaxSize (1 KiB), the smallest decoder window. It wraps
// github.com/klauspost/compress/zstd.
//
//	d, err := zstd.NewDecoder(zstd.Limits{MaxSize: 64 << 20, MaxRatio: 200})
//	defer d.Close()
//	plain, err := d.Decode(body) // errors.Is(err, zstd.ErrTooLarge) for a bomb
package zstd
