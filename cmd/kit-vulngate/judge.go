package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// judge decides each finding by the policy, the acceptances and the
// previous release's baseline.
type judge struct {
	policy   policy
	accepted []acceptance
	baseline baseline
	now      time.Time
	// deployed is -report: everything scanned is in production already,
	// so every third-party finding is inherited.
	deployed bool
}

// list prints each finding, most urgent first, and counts the late ones.
func (j judge) list(w io.Writer, all []finding) int {
	slices.SortFunc(all, func(a, b finding) int {
		if d := rank(b.Severity) - rank(a.Severity); d != 0 {
			return d
		}
		return strings.Compare(a.ID+a.Target, b.ID+b.Target)
	})
	late, tracked := 0, 0
	for _, f := range all {
		state, isLate, isTracked := j.status(f)
		if isLate {
			late++
		}
		if isTracked {
			tracked++
		}
		exploited := ""
		if f.Exploited {
			exploited = " KEV"
		}
		owner := f.Component
		if owner == "" {
			owner = "own"
		}
		_, _ = fmt.Fprintf(w, "%-8s %-20s%s %s %s -> %s  [%s]  %s  %s\n", strings.ToUpper(f.Severity), f.ID, exploited, f.Package, f.Version, orNone(f.FixedIn), f.Target, owner, state)
	}
	if tracked > 0 {
		_, _ = fmt.Fprintf(w, "%d tracked in third-party components\n", tracked)
	}
	return late
}

// status is the finding's state; late fails the run, tracked is an
// inherited third-party finding with a fix.
func (j judge) status(f finding) (state string, late, tracked bool) {
	switch {
	case accepted(j.accepted, f, j.now):
		return "accepted", false, false
	case !f.Fixed:
		return "no fix yet", false, false
	case f.Component != "" && (j.deployed || j.baseline.carries(f)):
		return "tracked: due once " + f.Component + " ships the fix" + j.libraryDue(f), false, true
	}
	prefix := ""
	if f.Component != "" {
		prefix = "new, "
	}
	if f.FixedSince.IsZero() {
		return "LATE: " + prefix + "a fix exists, its date is unknown", true, false
	}
	due := f.FixedSince.Add(j.policy.deadline(f.Severity, f.Exploited))
	if !j.now.Before(due) {
		return "LATE: " + prefix + "due " + due.Format(time.DateOnly), true, false
	}
	return prefix + "due " + due.Format(time.DateOnly), false, false
}

// libraryDue is the deadline counted from the package's own fix, for
// reference while the component has not shipped it.
func (j judge) libraryDue(f finding) string {
	if f.FixedSince.IsZero() {
		return ""
	}
	return " (package fixed " + f.FixedSince.Format(time.DateOnly) + ", " + f.FixedSince.Add(j.policy.deadline(f.Severity, f.Exploited)).Format(time.DateOnly) + " counted from it)"
}

func orNone(v string) string {
	if v == "" {
		return "no fix"
	}
	return v
}
