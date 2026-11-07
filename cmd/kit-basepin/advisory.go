package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// advisory names a pinned base digest that carries a fixed advisory
// (a DSA without a CVE, say, that no scanner reports): that pin must move
// to the tag's digest at once, without the dependency wait.
type advisory struct {
	Image    string `json:"image"`
	Digest   string `json:"digest"`
	Advisory string `json:"advisory"`
	Reason   string `json:"reason"`
	used     bool
}

func readAdvisories(r io.Reader) ([]*advisory, error) {
	var list []*advisory
	if err := json.NewDecoder(r).Decode(&list); err != nil {
		return nil, fmt.Errorf("advisories: %w", err)
	}
	for i, a := range list {
		p, ok := parsePin(a.Image + ":x@" + a.Digest)
		if !ok || a.Advisory == "" || a.Reason == "" {
			return nil, fmt.Errorf("advisory %d: image (name), digest (sha256:<hex>), advisory and reason are required", i+1)
		}
		a.Image = p.Host + "/" + p.Repository
	}
	return list, nil
}

func advisoryFor(list []*advisory, p pin) *advisory {
	for _, a := range list {
		if a.Image == p.Host+"/"+p.Repository && a.Digest == p.Digest {
			a.used = true
			return a
		}
	}
	return nil
}
