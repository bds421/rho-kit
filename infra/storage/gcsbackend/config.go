package gcsbackend

import (
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/config"
)

// GCSConfig holds Google Cloud Storage connection settings.
type GCSConfig struct {
	// Bucket is the GCS bucket name.
	Bucket string

	// ProjectID is the Google Cloud project ID.
	ProjectID string

	// CredentialsFile is the path to a service account JSON key file.
	// Leave empty to use Application Default Credentials (ADC).
	CredentialsFile string

	// Endpoint overrides the default GCS endpoint (for testing with fake-gcs-server).
	Endpoint string
}

// LogValue implements slog.LogValuer.
func (c GCSConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("bucket", c.Bucket),
		slog.String("project_id", c.ProjectID),
		slog.String("credentials_file", c.CredentialsFile),
		slog.String("endpoint", c.Endpoint),
	)
}

// LoadGCSConfig reads GCS settings from environment variables.
//
// Environment variables:
//   - STORAGE_GCS_BUCKET (required)
//   - STORAGE_GCS_PROJECT_ID (required)
//   - STORAGE_GCS_CREDENTIALS_FILE (optional, path to service account JSON)
//   - STORAGE_GCS_ENDPOINT (optional, for testing)
func LoadGCSConfig() (GCSConfig, error) {
	cfg := GCSConfig{
		Bucket:          config.Get("STORAGE_GCS_BUCKET", ""),
		ProjectID:       config.Get("STORAGE_GCS_PROJECT_ID", ""),
		CredentialsFile: config.Get("STORAGE_GCS_CREDENTIALS_FILE", ""),
		Endpoint:        config.Get("STORAGE_GCS_ENDPOINT", ""),
	}

	if err := cfg.Validate(); err != nil {
		return GCSConfig{}, err
	}

	return cfg, nil
}

// Validate checks that required GCS fields are present.
func (c GCSConfig) Validate() error {
	if c.Bucket == "" {
		return fmt.Errorf("STORAGE_GCS_BUCKET is required")
	}
	if c.ProjectID == "" {
		return fmt.Errorf("STORAGE_GCS_PROJECT_ID is required")
	}
	return nil
}
