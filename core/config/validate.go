package config

import (
	"fmt"
	"strings"
)

// ValidatePort checks that a port number is in the valid 1–65535 range.
func ValidatePort(name string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid %s port: %d", name, port)
	}
	return nil
}

// IsDevelopment reports whether the environment string indicates development mode.
// The ENVIRONMENT variable must be explicitly set to "development" to enable
// development features such as debug endpoints.
func IsDevelopment(environment string) bool {
	return environment == "development"
}

// RejectWeakCredential returns an error if the value is too short or contains "changeme".
// Use this in production mode to prevent deployment with default credentials.
func RejectWeakCredential(name, value string) error {
	if len(value) < 12 {
		return fmt.Errorf("%s must be at least 12 characters long", name)
	}
	if strings.Contains(strings.ToLower(value), "changeme") {
		return fmt.Errorf("%s contains 'changeme' — replace with a strong credential before running in production", name)
	}
	return nil
}

// ValidatePositive returns an error if value is not a positive integer.
func ValidatePositive(name string, value int) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive, got %d", name, value)
	}
	return nil
}
