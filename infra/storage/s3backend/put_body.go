package s3backend

import (
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// DefaultMaxSpoolBytes caps a spooled Put body: 5 GiB, the largest object
// one PutObject stores.
const DefaultMaxSpoolBytes int64 = 5 << 30

// WithSpoolDir sets the directory where Put spools a body that cannot seek
// and comes without ObjectMeta.Size (see [Backend.Put]). Empty, the
// default, is [os.TempDir]. Point it at a disk-backed directory: on a
// tmpfs the spooled object sits in memory again.
func WithSpoolDir(dir string) Option {
	return func(b *Backend) { b.spoolDir = dir }
}

// WithMaxSpoolBytes caps the size of a spooled Put body; a longer body
// fails with [storage.ErrValidation] before anything is uploaded.
// Default: [DefaultMaxSpoolBytes]. Panics if n <= 0.
func WithMaxSpoolBytes(n int64) Option {
	if n <= 0 {
		panic("s3backend: WithMaxSpoolBytes requires n > 0")
	}
	return func(b *Backend) { b.maxSpoolBytes = n }
}

// putBody prepares a PutObject body. A seekable body goes as it is: the
// SDK hashes it for the signature and rewinds it. A body that cannot seek
// (a stream such as an HTTP upload or client-side encryption) cannot be
// hashed in advance, which the SDK requires over plain HTTP; with a known
// size it is sent with an unsigned payload, as the SDK does over HTTPS
// anyway. Over an insecure (http://) endpoint such a payload is neither
// signed nor checked in transit, which is acceptable for local emulators
// only. Without a size, S3 needs a Content-Length, so the body is spooled
// to a file first: memory stays flat, disk holds the object until the
// upload ends. cleanup is never nil.
func (b *Backend) putBody(r io.Reader, size int64) (io.Reader, int64, []func(*s3.Options), func(), error) {
	if _, ok := r.(io.ReadSeeker); ok {
		return r, size, nil, func() {}, nil
	}
	if size > 0 {
		return r, size, []func(*s3.Options){s3.WithAPIOptions(v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)}, func() {}, nil
	}
	file, n, err := b.spool(r)
	if err != nil {
		return nil, 0, nil, func() {}, err
	}
	return file, n, nil, func() { removeSpool(file) }, nil
}

// spool copies r into a new file under the spool directory, at most the
// spool limit, and rewinds it. On any failure, a panic included, the file
// is removed.
func (b *Backend) spool(r io.Reader) (*os.File, int64, error) {
	limit := b.maxSpoolBytes
	if limit == 0 {
		limit = DefaultMaxSpoolBytes
	}
	file, err := os.CreateTemp(b.spoolDir, "rho-kit-s3-put-*")
	if err != nil {
		return nil, 0, storage.WrapSafe("s3backend: create put spool failed", err)
	}
	done := false
	defer func() {
		if !done {
			removeSpool(file)
		}
	}()
	n, err := io.Copy(file, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, 0, storage.WrapSafe("s3backend: spool put body failed", err)
	}
	if n > limit {
		return nil, 0, fmt.Errorf("%w: put body of unknown size exceeds the %d-byte spool limit", storage.ErrValidation, limit)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, storage.WrapSafe("s3backend: rewind put spool failed", err)
	}
	done = true
	return file, n, nil
}

func removeSpool(file *os.File) {
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
}

// putInput is the PutObject request for body of size bytes (zero when
// unknown), with the configured server-side encryption.
func (b *Backend) putInput(key string, body io.Reader, size int64, meta storage.ObjectMeta) (*s3.PutObjectInput, error) {
	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	input := &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
		Metadata:    storage.CloneCustomMeta(meta.Custom),
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if err := applySSE(input, b.cfg); err != nil {
		return nil, err
	}
	return input, nil
}
