package main

import (
	"strings"
	"time"
)

// Deadlines from when a fix is available: an actively exploited
// vulnerability (CISA KEV) 72 hours, critical 7 days, high 30, medium 90;
// a low one ships with the next release, so any release with a fix
// available is late.
var deadlines = map[string]time.Duration{
	"critical": 7 * 24 * time.Hour,
	"high":     30 * 24 * time.Hour,
	"medium":   90 * 24 * time.Hour,
	"low":      0,
}

const exploitedDeadline = 72 * time.Hour

// unknownSeverity is how a vulnerability without a severity is held.
const unknownSeverity = "medium"

func deadline(severity string, exploited bool) time.Duration {
	if exploited {
		return exploitedDeadline
	}
	if d, ok := deadlines[strings.ToLower(severity)]; ok {
		return d
	}
	return deadlines[unknownSeverity]
}

// rank orders severities, highest first.
func rank(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}
