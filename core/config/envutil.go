package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Get returns the value of the environment variable named by key,
// or fallback if the variable is empty or unset.
func Get(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// GetSecret reads a secret value using the Docker Secrets convention:
// if <key>_FILE is set, the secret is read from that file path (with
// trailing line terminators trimmed); otherwise the value of <key> is
// returned directly. This allows the same code to work in Docker (secrets
// mounted as files) and in local dev (secrets passed as plain env vars
// via .env).
//
// Returns an error when <key>_FILE is set but unreadable (permissions,
// missing mount, etc.) — silently falling back to an env var on a
// configured-but-unreadable secret file is a security risk because the
// service may start with empty credentials.
//
// Use [MustGetSecret] in startup paths where unreadable secrets should
// crash the process; use this variant when the caller wants to log and
// degrade gracefully.
func GetSecret(key, fallback string) (string, error) {
	if filePath := os.Getenv(key + "_FILE"); filePath != "" {
		b, err := readSecretFile(filePath)
		if err != nil {
			return "", fmt.Errorf("config: secret file for %s_FILE is unreadable: %w", key, err)
		}
		// Copy into a string and zero the originating buffer so the
		// raw bytes don't linger on the heap longer than necessary.
		v := string(b)
		zeroBytes(b)
		return v, nil
	}
	return Get(key, fallback), nil
}

// MustGetSecret is the panic-on-error variant of [GetSecret]. Use in startup
// paths where an unreadable configured secret file means the service should
// not start at all.
func MustGetSecret(key, fallback string) string {
	v, err := GetSecret(key, fallback)
	if err != nil {
		slog.Error("secret file configured but unreadable",
			redact.String("key", key+"_FILE"), redact.Error(err))
		panic("config: MustGetSecret secret file is unreadable")
	}
	return v
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
		return 0, fmt.Errorf("invalid integer for %s", key)
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
		return false, fmt.Errorf("invalid boolean for %s", key)
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
		return 0, fmt.Errorf("invalid float for %s", key)
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
