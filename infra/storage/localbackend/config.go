package localbackend

import (
	"fmt"

	"github.com/bds421/rho-kit/core/config"
)

// LocalConfig holds configuration for the local filesystem backend.
type LocalConfig struct {
	// RootDir is the base directory for storing objects.
	RootDir string
}

// LoadLocalConfig reads the local storage configuration from environment variables.
//
// Environment variables:
//   - STORAGE_LOCAL_ROOT (required)
func LoadLocalConfig() (LocalConfig, error) {
	root := config.Get("STORAGE_LOCAL_ROOT", "")
	if root == "" {
		return LocalConfig{}, fmt.Errorf("STORAGE_LOCAL_ROOT is required")
	}
	return LocalConfig{RootDir: root}, nil
}
