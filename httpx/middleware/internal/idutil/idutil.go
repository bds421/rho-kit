// Package idutil provides shared ID generation and validation helpers
// for the requestid and correlationid middleware packages.
package idutil

import "github.com/bds421/rho-kit/core/contextutil"

// Generate produces a UUID v7 string (time-ordered, random).
// Falls back to a UUID-formatted time+counter string if crypto/rand is unavailable.
//
// Deprecated: Use contextutil.NewID instead.
func Generate() string {
	return contextutil.NewID()
}

// IsValid returns true if id is non-empty, within the given maxLen,
// and contains only printable ASCII characters excluding space (0x21-0x7E).
// Space (0x20) is excluded because spaces in trace IDs cause log-parsing issues.
func IsValid(id string, maxLen int) bool {
	if id == "" || len(id) > maxLen {
		return false
	}
	for _, c := range id {
		if c <= 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}
