package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
)

// inherited is a third-party finding as an earlier release carried it:
// component, package and vulnerability, not the version, so a component
// update that still carries the vulnerability inherits it, and one that
// adds a vulnerability does not.
type inherited struct {
	Component string `json:"component"`
	Package   string `json:"package"`
	ID        string `json:"id"`
}

type baseline map[inherited]bool

func readBaseline(r io.Reader) (baseline, error) {
	var list []inherited
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	b := baseline{}
	for i, e := range list {
		if e.Component == "" || e.Package == "" || e.ID == "" {
			return nil, fmt.Errorf("baseline entry %d: component, package and id are required", i+1)
		}
		b[e] = true
	}
	return b, nil
}

// carries tells whether the baseline has the finding under its ID or one
// of its aliases, as the vulnerability database may name it either way.
func (b baseline) carries(f finding) bool {
	for _, id := range append([]string{f.ID}, f.Aliases...) {
		if b[inherited{Component: f.Component, Package: f.Package, ID: id}] {
			return true
		}
	}
	return false
}

// record writes the third-party findings, sorted and once each, as the
// baseline of the next release.
func record(path string, all []finding) error {
	list := []inherited{}
	for _, f := range all {
		if f.Component != "" {
			list = append(list, inherited{Component: f.Component, Package: f.Package, ID: f.ID})
		}
	}
	slices.SortFunc(list, func(a, b inherited) int {
		return cmp.Or(cmp.Compare(a.Component, b.Component), cmp.Compare(a.Package, b.Package), cmp.Compare(a.ID, b.ID))
	})
	body, err := json.MarshalIndent(slices.Compact(list), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644) //nolint:gosec // a committed file, not a secret
}
