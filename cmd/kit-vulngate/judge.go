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
	baseline *baseline
	now      time.Time
	// deployed is -report: everything scanned is in production already.
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
	}
	if f.Component == "" {
		due, dated := j.libraryDue(f)
		return j.deadlineState("", due, dated)
	}
	e, known := j.baseline.lookup(f)
	if (j.deployed && j.baseline == nil) || (known && e.State == stateInherited) {
		return j.inherited(f)
	}
	due, dated := j.libraryDue(f)
	if known && !e.due.IsZero() && (!dated || e.due.Before(due)) {
		due, dated = e.due, true
	}
	return j.deadlineState("new, ", due, dated)
}

// inherited is tracked until the component ships the fix, except a known
// exploited one, which fails once the policy's inheritedKev has passed
// since the package's fix.
func (j judge) inherited(f finding) (string, bool, bool) {
	fixed := ""
	if !f.FixedSince.IsZero() {
		due, _ := j.libraryDue(f)
		fixed = " (package fixed " + f.FixedSince.Format(time.DateOnly) + ", " + due.Format(time.DateOnly) + " counted from it)"
	}
	if f.Exploited {
		if f.FixedSince.IsZero() {
			return j.deadlineState("inherited, known exploited, ", time.Time{}, false)
		}
		due := f.FixedSince.Add(j.policy.inheritedExploited)
		if !j.now.Before(due) {
			return j.deadlineState("inherited, known exploited, ", due, true)
		}
		fixed = ", known exploited: fails " + due.Format(time.DateOnly)
	}
	return "tracked: due once " + f.Component + " ships the fix" + fixed, false, true
}

// libraryDue is the deadline counted from the package's own fix; false
// when its date is unknown.
func (j judge) libraryDue(f finding) (time.Time, bool) {
	if f.FixedSince.IsZero() {
		return time.Time{}, false
	}
	return f.FixedSince.Add(j.policy.deadline(f.Severity, f.Exploited)), true
}

func (j judge) deadlineState(prefix string, due time.Time, dated bool) (string, bool, bool) {
	if !dated {
		return "LATE: " + prefix + "a fix exists, its date is unknown", true, false
	}
	if !j.now.Before(due) {
		return "LATE: " + prefix + "due " + due.Format(time.DateOnly), true, false
	}
	return prefix + "due " + due.Format(time.DateOnly), false, false
}

func orNone(v string) string {
	if v == "" {
		return "no fix"
	}
	return v
}
