package azurebackend

import (
	"fmt"
	"log/slog"

	"github.com/bds421/rho-kit/core/config"
)

// AzureConfig holds Azure Blob Storage connection settings.
type AzureConfig struct {
	// AccountName is the Azure Storage account name.
	AccountName string

	// AccountKey is the storage account access key.
	AccountKey string

	// ContainerName is the blob container name.
	ContainerName string

	// Endpoint overrides the default endpoint (for Azurite or sovereign clouds).
	// Leave empty for the default "https://{account}.blob.core.windows.net".
	Endpoint string
}

// LogValue implements slog.LogValuer to prevent logging credentials.
func (c AzureConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("account_name", c.AccountName),
		slog.String("account_key", "[REDACTED]"),
		slog.String("container_name", c.ContainerName),
		slog.String("endpoint", c.Endpoint),
	)
}

// LoadAzureConfig reads Azure settings from environment variables.
//
// Environment variables:
//   - STORAGE_AZURE_ACCOUNT_NAME (required)
//   - {envPrefix}_AZURE_ACCOUNT_KEY (required, supports _FILE suffix)
//   - STORAGE_AZURE_CONTAINER_NAME (required)
//   - STORAGE_AZURE_ENDPOINT (optional, for Azurite)
func LoadAzureConfig(envPrefix, environment string) (AzureConfig, error) {
	cfg := AzureConfig{
		AccountName:   config.Get("STORAGE_AZURE_ACCOUNT_NAME", ""),
		AccountKey:    config.GetSecret(envPrefix+"_AZURE_ACCOUNT_KEY", ""),
		ContainerName: config.Get("STORAGE_AZURE_CONTAINER_NAME", ""),
		Endpoint:      config.Get("STORAGE_AZURE_ENDPOINT", ""),
	}

	if err := cfg.Validate(environment); err != nil {
		return AzureConfig{}, err
	}

	return cfg, nil
}

// Validate checks that required Azure fields are present.
func (c AzureConfig) Validate(environment string) error {
	if c.AccountName == "" {
		return fmt.Errorf("STORAGE_AZURE_ACCOUNT_NAME is required")
	}
	if c.AccountKey == "" {
		return fmt.Errorf("AZURE_ACCOUNT_KEY is required")
	}
	if c.ContainerName == "" {
		return fmt.Errorf("STORAGE_AZURE_CONTAINER_NAME is required")
	}

	if !config.IsDevelopment(environment) {
		if err := config.RejectWeakCredential("AZURE_ACCOUNT_KEY", c.AccountKey); err != nil {
			return err
		}
	}

	return nil
}
