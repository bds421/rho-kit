package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"time"
)

// Entry states. Inherited is debt a component had when the baseline was
// created (or that a reviewed commit declared so): tracked until the
// component ships the fix. New is a finding a release added: held to its
// due date in every later gate until it is gone.
const (
	stateInherited = "inherited"
	stateNew       = "new"
)

// entry is a third-party finding as a release carried it: component,
// package and vulnerability, not the version, so a component update that
// still carries the vulnerability keeps its entry, and one that adds a
// vulnerability gets a new one.
type entry struct {
	Component string `json:"component"`
	Package   string `json:"package"`
	ID        string `json:"id"`
	State     string `json:"state"`
	// FirstSeen and Due (YYYY-MM-DD) are a new entry's: the day a release
	// first carried it and its deadline then, kept while it stays.
	FirstSeen string `json:"firstSeen,omitempty"`
	Due       string `json:"due,omitempty"`
	due       time.Time
}

type entryKey struct{ component, pkg, id string }

// baseline is the third-party findings of the release it was recorded
// for (-record, -release).
type baseline struct {
	Release  string  `json:"release"`
	Recorded string  `json:"recorded"`
	Findings []entry `json:"findings"`
	byKey    map[entryKey]entry
}

func readBaseline(r io.Reader) (*baseline, error) {
	var b baseline
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if b.Release == "" {
		return nil, fmt.Errorf("baseline: release is required")
	}
	b.byKey = map[entryKey]entry{}
	for i, e := range b.Findings {
		if e.Component == "" || e.Package == "" || e.ID == "" || (e.State != stateInherited && e.State != stateNew) {
			return nil, fmt.Errorf("baseline entry %d: component, package, id and state (inherited or new) are required", i+1)
		}
		if e.Due != "" {
			at, err := time.Parse(time.DateOnly, e.Due)
			if err != nil {
				return nil, fmt.Errorf("baseline entry %d: due %q is not YYYY-MM-DD", i+1, e.Due)
			}
			e.due = at
		}
		b.byKey[entryKey{e.Component, e.Package, e.ID}] = e
	}
	return &b, nil
}

// lookup finds the finding under its ID or one of its aliases, as the
// vulnerability database may name it either way.
func (b *baseline) lookup(f finding) (entry, bool) {
	if b == nil {
		return entry{}, false
	}
	for _, id := range append([]string{f.ID}, f.Aliases...) {
		if e, ok := b.byKey[entryKey{f.Component, f.Package, id}]; ok {
			return e, true
		}
	}
	return entry{}, false
}

// record writes this run's third-party findings, sorted and once each, as
// the baseline of the next release. An entry the previous baseline had
// keeps its state, first day and due date; any other is new, unless
// j.initial (-initial: the first baseline, a report of what is deployed)
// marks all inherited.
func (j judge) record(path, release string, all []finding) error {
	out := baseline{Release: release, Recorded: j.now.UTC().Format(time.DateOnly), Findings: []entry{}}
	seen := map[entryKey]bool{}
	for _, f := range all {
		if f.Component == "" {
			continue
		}
		e, ok := j.baseline.lookup(f)
		if !ok {
			e = entry{Component: f.Component, Package: f.Package, ID: f.ID, State: stateNew, FirstSeen: out.Recorded}
			if j.initial {
				e = entry{Component: f.Component, Package: f.Package, ID: f.ID, State: stateInherited}
			}
		}
		if e.State == stateNew && e.Due == "" {
			if due, known := j.libraryDue(f); known {
				e.Due = due.Format(time.DateOnly)
			}
		}
		k := entryKey{e.Component, e.Package, e.ID}
		if !seen[k] {
			seen[k] = true
			out.Findings = append(out.Findings, e)
		}
	}
	slices.SortFunc(out.Findings, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.Component, b.Component), cmp.Compare(a.Package, b.Package), cmp.Compare(a.ID, b.ID))
	})
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644) //nolint:gosec // a committed file, not a secret
}
