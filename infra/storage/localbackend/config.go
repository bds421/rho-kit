package localbackend

import (
	"fmt"

	"github.com/bds421/rho-kit/core/v2/config"
)

// Config holds configuration for the local filesystem backend.
type Config struct {
	// RootDir is the base directory for storing objects.
	RootDir string
}

// LoadLocalConfig reads the local storage configuration from environment variables.
//
// Environment variables:
//   - STORAGE_LOCAL_ROOT (required)
func LoadLocalConfig() (Config, error) {
	root := config.Get("STORAGE_LOCAL_ROOT", "")
	if root == "" {
		return Config{}, fmt.Errorf("STORAGE_LOCAL_ROOT is required")
	}
	return Config{RootDir: root}, nil
}
