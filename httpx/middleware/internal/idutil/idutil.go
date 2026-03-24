// Package idutil provides shared ID validation helpers
// for the requestid and correlationid middleware packages.
package idutil

// IsValid returns true if id is non-empty, within the given maxLen,
// and contains only printable ASCII characters excluding space (0x21-0x7E).
// Space (0x20) is excluded because spaces in trace IDs cause log-parsing issues.
func IsValid(id string, maxLen int) bool {
	if id == "" || len(id) > maxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c <= 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}
