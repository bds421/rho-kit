package azurebackend

import (
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

// Config holds Azure Blob Storage connection settings.
type Config struct {
	// AccountName is the Azure Storage account name.
	AccountName string

	// AccountKey is the storage account access key.
	AccountKey string

	// ContainerName is the blob container name.
	ContainerName string

	// Endpoint overrides the default endpoint (for Azurite or sovereign clouds).
	// Leave empty for the default "https://{account}.blob.core.windows.net".
	Endpoint string

	// AllowInsecureEndpoint permits http:// endpoints for local emulators.
	AllowInsecureEndpoint bool
}

// LogValue implements slog.LogValuer to prevent logging credentials.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("account_name_configured", c.AccountName != ""),
		slog.Bool("account_key_configured", c.AccountKey != ""),
		slog.Bool("container_name_configured", c.ContainerName != ""),
		slog.Bool("endpoint_configured", c.Endpoint != ""),
		slog.Bool("allow_insecure_endpoint", c.AllowInsecureEndpoint),
	)
}

// LoadConfig reads Azure settings from environment variables.
//
// Environment variables:
//   - STORAGE_AZURE_ACCOUNT_NAME (required)
//   - {envPrefix}_AZURE_ACCOUNT_KEY (required, supports _FILE suffix)
//   - STORAGE_AZURE_CONTAINER_NAME (required)
//   - STORAGE_AZURE_ENDPOINT (optional, for Azurite)
//   - STORAGE_AZURE_ALLOW_INSECURE_ENDPOINT (optional bool, default false)
func LoadConfig(envPrefix, environment string) (Config, error) {
	p := &config.Parser{}
	allowInsecureEndpoint := p.Bool("STORAGE_AZURE_ALLOW_INSECURE_ENDPOINT", false)
	if err := p.Err(); err != nil {
		return Config{}, err
	}

	cfg := Config{
		AccountName:           config.Get("STORAGE_AZURE_ACCOUNT_NAME", ""),
		AccountKey:            config.MustGetSecret(envPrefix+"_AZURE_ACCOUNT_KEY", ""),
		ContainerName:         config.Get("STORAGE_AZURE_CONTAINER_NAME", ""),
		Endpoint:              config.Get("STORAGE_AZURE_ENDPOINT", ""),
		AllowInsecureEndpoint: allowInsecureEndpoint,
	}

	if err := cfg.Validate(environment); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks that required Azure fields are present.
func (c Config) Validate(environment string) error {
	if err := c.validateCommon(environment); err != nil {
		return err
	}
	if c.AccountKey == "" {
		return fmt.Errorf("AZURE_ACCOUNT_KEY is required")
	}
	if err := config.RejectWeakCredential("AZURE_ACCOUNT_KEY", c.AccountKey); err != nil {
		return err
	}
	return nil
}

func (c Config) validateTokenCredential(environment string) error {
	return c.validateCommon(environment)
}

func (c Config) validateCommon(environment string) error {
	if c.AccountName == "" {
		return fmt.Errorf("STORAGE_AZURE_ACCOUNT_NAME is required")
	}
	if c.ContainerName == "" {
		return fmt.Errorf("STORAGE_AZURE_CONTAINER_NAME is required")
	}
	if err := storage.ValidateEndpointURL("STORAGE_AZURE_ENDPOINT", c.Endpoint, c.AllowInsecureEndpoint); err != nil {
		return err
	}
	_ = environment // accepted for API compatibility; no longer consulted

	return nil
}
