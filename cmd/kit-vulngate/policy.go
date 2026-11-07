package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// policy holds the deadlines counted from when a fix is available and the
// third-party components. The default deadlines: an actively exploited
// vulnerability (CISA KEV) 72 hours, critical 7 days, high 30, medium, low
// and one without a severity 90; a known exploited one a third-party component
// inherited 14 days after the package's fix (inheritedKev).
type policy struct {
	exploited          time.Duration
	inheritedExploited time.Duration
	severity           map[string]time.Duration
	thirdParty         []component
}

func defaultPolicy() policy {
	day := 24 * time.Hour
	return policy{
		exploited:          72 * time.Hour,
		inheritedExploited: 14 * day,
		severity: map[string]time.Duration{
			"critical": 7 * day,
			"high":     30 * day,
			"medium":   90 * day,
			"low":      90 * day,
			"unknown":  90 * day,
		},
	}
}

// policyFile is a repository's policy (-policy): deadlines ("7d" or a Go
// duration such as "72h") by "kev", "inheritedKev" and severity, and the
// third-party components.
type policyFile struct {
	Deadlines  map[string]string `json:"deadlines"`
	ThirdParty []component       `json:"thirdParty"`
}

// readPolicy returns the defaults with the file's deadlines and
// components; an unknown key or deadline, and a component without a name
// or without image, target or path, are refused. A component no report
// holds is fine: a gate may scan only some images (one proxy build).
func readPolicy(r io.Reader) (policy, error) {
	p := defaultPolicy()
	var file policyFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return p, fmt.Errorf("policy: %w", err)
	}
	for name, value := range file.Deadlines {
		d, err := parseDeadline(value)
		if err != nil {
			return p, fmt.Errorf("policy: deadline %s: %w", name, err)
		}
		switch name {
		case "kev":
			p.exploited = d
			continue
		case "inheritedKev":
			p.inheritedExploited = d
			continue
		}
		if _, ok := p.severity[name]; !ok {
			return p, fmt.Errorf("policy: deadline %q is none of kev, inheritedKev, critical, high, medium, low, unknown", name)
		}
		p.severity[name] = d
	}
	for i, c := range file.ThirdParty {
		if err := c.validate(); err != nil {
			return p, fmt.Errorf("policy: third-party component %d: %w", i+1, err)
		}
	}
	p.thirdParty = file.ThirdParty
	return p, nil
}

// parseDeadline reads "<n>d" as n days, anything else as a Go duration.
func parseDeadline(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if days, ok := strings.CutSuffix(value, "d"); ok {
		var n int
		n, err = strconv.Atoi(days)
		d = time.Duration(n) * 24 * time.Hour
	}
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not \"<days>d\" or a duration of at least zero", value)
	}
	return d, nil
}

func (p policy) deadline(severity string, exploited bool) time.Duration {
	if exploited {
		return p.exploited
	}
	if d, ok := p.severity[strings.ToLower(severity)]; ok {
		return d
	}
	return p.severity["unknown"]
}

// componentOf is the third-party component a finding is in, empty when we
// own it.
func (p policy) componentOf(f finding) string {
	for _, c := range p.thirdParty {
		if c.holds(f) {
			return c.Name
		}
	}
	return ""
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
