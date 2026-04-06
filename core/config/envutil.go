package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Get returns the value of the environment variable named by key,
// or fallback if the variable is empty or unset.
func Get(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// GetSecretPath returns the file path for a secret env var following the
// Docker Secrets / Kubernetes volume mount convention. If KEY_FILE is set,
// returns its value (the path to the mounted secret file). Otherwise returns
// empty string, meaning the secret is inline (not file-backed).
//
// Use this to determine whether a secret is file-backed and therefore
// eligible for runtime rotation via [SecretWatcher].
func GetSecretPath(key string) string {
	return os.Getenv(key + "_FILE")
}

// GetSecret reads a secret value using the Docker Secrets convention:
// if <key>_FILE is set, the secret is read from that file path (trimmed of
// whitespace); otherwise the value of <key> is returned directly.
// This allows the same code to work in Docker (secrets mounted as files)
// and in local dev (secrets passed as plain env vars via .env).
func GetSecret(key, fallback string) string {
	if filePath := os.Getenv(key + "_FILE"); filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			// Fail hard: if the file path was explicitly configured but is
			// unreadable (permissions, missing mount), silently falling back to
			// an env var is a security risk (may start with empty credentials).
			slog.Error("secret file configured but unreadable",
				"key", key+"_FILE", "path", filePath, "error", err)
			panic(fmt.Sprintf("secret file %s for %s_FILE is unreadable: %v", filePath, key, err))
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	return Get(key, fallback)
}

// GetInt returns the value of the environment variable named by key
// parsed as an integer, or fallback if the variable is empty or unset.
func GetInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer for %s=%q: %w", key, v, err)
	}
	return i, nil
}

// GetBool returns the value of the environment variable named by key
// parsed as a boolean, or fallback if the variable is empty or unset.
func GetBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid boolean for %s=%q: %w", key, v, err)
	}
	return b, nil
}

// GetFloat64 returns the value of the environment variable named by key
// parsed as a float64, or fallback if the variable is empty or unset.
func GetFloat64(key string, fallback float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float for %s=%q: %w", key, v, err)
	}
	return f, nil
}

// Parser collects errors from multiple env var parse calls, reporting all
// failures at once instead of failing on the first one.
type Parser struct {
	errs []error
}

// Int parses an integer environment variable, collecting any error.
func (p *Parser) Int(name string, def int) int {
	v, err := GetInt(name, def)
	if err != nil {
		p.errs = append(p.errs, err)
	}
	return v
}

// Bool parses a boolean environment variable, collecting any error.
func (p *Parser) Bool(name string, def bool) bool {
	v, err := GetBool(name, def)
	if err != nil {
		p.errs = append(p.errs, err)
	}
	return v
}

// Float64 parses a float64 environment variable, collecting any error.
func (p *Parser) Float64(name string, def float64) float64 {
	v, err := GetFloat64(name, def)
	if err != nil {
		p.errs = append(p.errs, err)
	}
	return v
}

// Err returns all collected errors joined together, or nil if none.
func (p *Parser) Err() error {
	return errors.Join(p.errs...)
}
