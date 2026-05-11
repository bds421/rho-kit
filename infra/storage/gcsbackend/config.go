package gcsbackend

import (
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/infra/v2/storage"
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

	// AllowInsecureEndpoint permits http:// endpoints for local emulators.
	AllowInsecureEndpoint bool
}

// LogValue implements slog.LogValuer.
func (c GCSConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("bucket_configured", c.Bucket != ""),
		slog.Bool("project_id_configured", c.ProjectID != ""),
		slog.Bool("credentials_file_configured", c.CredentialsFile != ""),
		slog.Bool("endpoint_configured", c.Endpoint != ""),
		slog.Bool("allow_insecure_endpoint", c.AllowInsecureEndpoint),
	)
}

// LoadGCSConfig reads GCS settings from environment variables.
//
// Environment variables:
//   - STORAGE_GCS_BUCKET (required)
//   - STORAGE_GCS_PROJECT_ID (required)
//   - STORAGE_GCS_CREDENTIALS_FILE (optional, path to service account JSON)
//   - STORAGE_GCS_ENDPOINT (optional, for testing)
//   - STORAGE_GCS_ALLOW_INSECURE_ENDPOINT (optional bool, default false)
func LoadGCSConfig() (GCSConfig, error) {
	p := &config.Parser{}
	allowInsecureEndpoint := p.Bool("STORAGE_GCS_ALLOW_INSECURE_ENDPOINT", false)
	if err := p.Err(); err != nil {
		return GCSConfig{}, err
	}

	cfg := GCSConfig{
		Bucket:                config.Get("STORAGE_GCS_BUCKET", ""),
		ProjectID:             config.Get("STORAGE_GCS_PROJECT_ID", ""),
		CredentialsFile:       config.Get("STORAGE_GCS_CREDENTIALS_FILE", ""),
		Endpoint:              config.Get("STORAGE_GCS_ENDPOINT", ""),
		AllowInsecureEndpoint: allowInsecureEndpoint,
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
	if err := storage.ValidateEndpointURL("STORAGE_GCS_ENDPOINT", c.Endpoint, c.AllowInsecureEndpoint); err != nil {
		return err
	}
	return nil
}
