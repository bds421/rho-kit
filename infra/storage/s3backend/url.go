package s3backend

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/bds421/rho-kit/infra/storage"
)

// Compile-time interface compliance check.
var _ storage.PublicURLer = (*S3Backend)(nil)

// URL returns a public, non-expiring URL for the given key.
// The URL is constructed from the backend configuration and does not
// verify that the object exists. This is meaningful for public S3 buckets
// or CDN-fronted storage.
//
// Path-style:     <endpoint>/<bucket>/<key>
// Virtual-hosted: https://<bucket>.s3.<region>.amazonaws.com/<key>
func (b *S3Backend) URL(_ context.Context, key string) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}

	// Percent-encode each path segment of the key to produce a valid URL.
	encodedKey := encodeKeyPath(key)

	if b.cfg.Endpoint != "" {
		// Custom endpoint (localstack, minio, CDN) — always use path-style.
		endpoint := strings.TrimRight(b.cfg.Endpoint, "/")
		return fmt.Sprintf("%s/%s/%s", endpoint, b.cfg.Bucket, encodedKey), nil
	}

	// Standard AWS — use virtual-hosted style.
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", b.cfg.Bucket, b.cfg.Region, encodedKey), nil
}

// encodeKeyPath percent-encodes each segment of a storage key path
// while preserving forward slashes as path separators.
func encodeKeyPath(key string) string {
	segments := strings.Split(key, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}
