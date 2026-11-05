package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// policy holds the deadlines counted from when a fix is available. The
// defaults: an actively exploited vulnerability (CISA KEV) 72 hours,
// critical 7 days, high 30, medium 90; a low one ships with the next
// release, so any release with a fix available is late.
type policy struct {
	exploited time.Duration
	severity  map[string]time.Duration
}

// unknownSeverity is how a vulnerability without a severity is held.
const unknownSeverity = "medium"

func defaultPolicy() policy {
	return policy{
		exploited: 72 * time.Hour,
		severity: map[string]time.Duration{
			"critical": 7 * 24 * time.Hour,
			"high":     30 * 24 * time.Hour,
			"medium":   90 * 24 * time.Hour,
			"low":      0,
		},
	}
}

// register binds one flag per deadline to p, defaulting to its values.
func (p *policy) register(flags *flag.FlagSet) map[string]*time.Duration {
	flags.DurationVar(&p.exploited, "kev", p.exploited, "deadline for an actively exploited (CISA KEV) vulnerability")
	set := map[string]*time.Duration{}
	for _, s := range []string{"critical", "high", "medium", "low"} {
		d := p.severity[s]
		set[s] = &d
		flags.DurationVar(set[s], s, d, "deadline for a "+s+" vulnerability")
	}
	return set
}

// apply copies the parsed severity flags into p and refuses negative ones.
func (p *policy) apply(set map[string]*time.Duration) error {
	if p.exploited < 0 {
		return fmt.Errorf("-kev must not be negative")
	}
	for s, d := range set {
		if *d < 0 {
			return fmt.Errorf("-%s must not be negative", s)
		}
		p.severity[s] = *d
	}
	return nil
}

func (p policy) deadline(severity string, exploited bool) time.Duration {
	if exploited {
		return p.exploited
	}
	if d, ok := p.severity[strings.ToLower(severity)]; ok {
		return d
	}
	return p.severity[unknownSeverity]
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
