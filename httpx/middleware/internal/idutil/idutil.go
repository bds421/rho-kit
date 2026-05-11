// Package idutil provides shared ID validation helpers
// for the requestid and correlationid middleware packages.
package idutil

import "github.com/bds421/rho-kit/core/v2/contextutil"

// IsValid returns true if id is non-empty, within the given maxLen, and uses
// the kit's safe request/correlation token alphabet.
func IsValid(id string, maxLen int) bool {
	return contextutil.IsValidCorrelationToken(id, maxLen)
}
