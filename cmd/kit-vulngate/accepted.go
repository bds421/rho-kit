package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// acceptance is a finding someone decided not to fix by its deadline, with
// why and until when: a false positive (code that never reaches it), or a
// risk accepted while a fix is prepared. An expired one counts again.
type acceptance struct {
	ID      string `json:"id"`
	Package string `json:"package"`
	// Image, name[:tag]@sha256:<hex>, limits it to findings in that image.
	Image  string `json:"image,omitempty"`
	image  image
	Reason string `json:"reason"`
	Until  string `json:"until"`
	until  time.Time
}

func readAccepted(r io.Reader) ([]acceptance, error) {
	var list []acceptance
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return nil, fmt.Errorf("accepted findings: %w", err)
	}
	for i, a := range list {
		at, err := time.Parse(time.DateOnly, a.Until)
		if err != nil || a.ID == "" || a.Package == "" || a.Reason == "" {
			return nil, fmt.Errorf("accepted finding %d: id, package, reason and until (YYYY-MM-DD) are required", i+1)
		}
		if a.Image != "" {
			im, ok := parseImage(a.Image)
			if !ok {
				return nil, fmt.Errorf("accepted finding %d: image %q is not name[:tag]@sha256:<digest>", i+1, a.Image)
			}
			list[i].image = im
		}
		list[i].until = at.Add(24 * time.Hour)
	}
	return list, nil
}

func accepted(list []acceptance, f finding, now time.Time) bool {
	for _, a := range list {
		if a.ID == f.ID && a.Package == f.Package && now.Before(a.until) && (a.Image == "" || a.image == f.Image) {
			return true
		}
	}
	return false
}
