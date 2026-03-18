package s3backend

import (
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/config"
	"github.com/bds421/rho-kit/crypto/masking"
)

// S3Config holds AWS S3 connection settings.
type S3Config struct {
	Region          string
	Bucket          string
	Endpoint        string // empty for real AWS; set for localstack/minio
	ForcePathStyle  bool   // required for localstack and minio
	AccessKeyID     string
	SecretAccessKey string
}

// LogValue implements slog.LogValuer to prevent logging credentials.
func (c S3Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("region", c.Region),
		slog.String("bucket", c.Bucket),
		slog.String("endpoint", c.Endpoint),
		slog.Bool("force_path_style", c.ForcePathStyle),
		slog.String("access_key_id", masking.MaskString(c.AccessKeyID, 4)),
		slog.String("secret_access_key", "[REDACTED]"),
	)
}

// LoadS3Config reads S3 settings from environment variables.
//
// Environment variables:
//   - STORAGE_S3_REGION (required, e.g. "eu-central-1")
//   - STORAGE_S3_BUCKET (required)
//   - STORAGE_S3_ENDPOINT (optional, for localstack/minio)
//   - STORAGE_S3_FORCE_PATH_STYLE (optional bool, default false)
//   - {envPrefix}_S3_ACCESS_KEY_ID (required)
//   - {envPrefix}_S3_SECRET_ACCESS_KEY (required, supports _FILE suffix)
func LoadS3Config(envPrefix, environment string) (S3Config, error) {
	p := &config.Parser{}
	forcePathStyle := p.Bool("STORAGE_S3_FORCE_PATH_STYLE", false)
	if err := p.Err(); err != nil {
		return S3Config{}, err
	}

	cfg := S3Config{
		Region:          config.Get("STORAGE_S3_REGION", ""),
		Bucket:          config.Get("STORAGE_S3_BUCKET", ""),
		Endpoint:        config.Get("STORAGE_S3_ENDPOINT", ""),
		ForcePathStyle:  forcePathStyle,
		AccessKeyID:     config.Get(envPrefix+"_S3_ACCESS_KEY_ID", ""),
		SecretAccessKey: config.GetSecret(envPrefix+"_S3_SECRET_ACCESS_KEY", ""),
	}

	if err := cfg.Validate(environment); err != nil {
		return S3Config{}, err
	}

	return cfg, nil
}

// Validate checks that required S3 fields are present and credentials
// are not weak in non-development environments.
func (c S3Config) Validate(environment string) error {
	if c.Region == "" {
		return fmt.Errorf("STORAGE_S3_REGION is required")
	}
	if c.Bucket == "" {
		return fmt.Errorf("STORAGE_S3_BUCKET is required")
	}
	if c.AccessKeyID == "" {
		return fmt.Errorf("S3_ACCESS_KEY_ID is required")
	}
	if c.SecretAccessKey == "" {
		return fmt.Errorf("S3_SECRET_ACCESS_KEY is required")
	}

	if !config.IsDevelopment(environment) {
		if err := config.RejectWeakCredential("S3_SECRET_ACCESS_KEY", c.SecretAccessKey); err != nil {
			return err
		}
	}

	return nil
}
