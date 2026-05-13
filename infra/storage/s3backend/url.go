package s3backend

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Compile-time interface compliance check.
var _ storage.PublicURLer = (*Backend)(nil)

// URL returns a public, non-expiring URL for the given key.
// The URL is constructed from the backend configuration and does not
// verify that the object exists. This is meaningful for public S3 buckets
// or CDN-fronted storage.
//
// Path-style:     <endpoint>/<bucket>/<key>
// Virtual-hosted: https://<bucket>.s3.<region>.amazonaws.com/<key>
func (b *Backend) URL(_ context.Context, key string) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if b == nil || b.bucket == "" {
		return "", fmt.Errorf("s3backend: backend is not initialized")
	}

	// Percent-encode each path segment of the key to produce a valid URL.
	encodedKey := encodeKeyPath(key)

	if b.cfg.Endpoint != "" {
		if err := storage.ValidateEndpointURL("STORAGE_S3_ENDPOINT", b.cfg.Endpoint, b.cfg.AllowInsecureEndpoint); err != nil {
			return "", err
		}
		// Custom endpoint (localstack, minio, CDN) — always use path-style.
		endpoint := strings.TrimRight(b.cfg.Endpoint, "/")
		return fmt.Sprintf("%s/%s/%s", endpoint, b.bucket, encodedKey), nil
	}

	if tpl := b.cfg.URLTemplate; tpl != "" {
		base, err := renderURLTemplate(tpl, b.bucket, b.cfg.Region)
		if err != nil {
			return "", err
		}
		return base + "/" + encodedKey, nil
	}

	// Standard AWS — use virtual-hosted style.
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", b.bucket, b.cfg.Region, encodedKey), nil
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

func validateURLTemplate(tpl, bucket, region string) error {
	if tpl == "" {
		return nil
	}
	_, err := renderURLTemplate(tpl, bucket, region)
	return err
}

func renderURLTemplate(tpl, bucket, region string) (string, error) {
	base := strings.NewReplacer("{bucket}", bucket, "{region}", region).Replace(tpl)
	base = strings.TrimRight(base, "/")
	if strings.ContainsAny(base, "{}") {
		return "", fmt.Errorf("s3backend: STORAGE_S3_URL_TEMPLATE contains unknown placeholder")
	}
	if err := storage.ValidateEndpointURL("STORAGE_S3_URL_TEMPLATE", base, false); err != nil {
		return "", err
	}
	return base, nil
}
