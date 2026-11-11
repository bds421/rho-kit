package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// upstream is the newest image of a pinned image's tag, as a scan of the
// tag found it, and the findings it still carries. A late finding in the
// pinned image that the newest one still carries waits on upstream: no
// release of ours can fix it, so it is listed and recorded, not failed.
type upstream struct {
	newest  string
	carries map[findingKey]bool
}

type findingKey struct{ id, pkg string }

// readUpstream reads pinned=report.json: the pinned image as the gate's
// reports name it, and the grype report of its tag's newest image.
func readUpstream(arg string) (image, upstream, error) {
	ref, path, ok := strings.Cut(arg, "=")
	pinned, valid := parseImage(ref)
	if !ok || !valid {
		return image{}, upstream{}, fmt.Errorf("-upstream %q is not name[:tag]@sha256:<digest>=report.json", arg)
	}
	u := upstream{carries: map[findingKey]bool{}}
	err := readFile(path, func(r io.Reader) error {
		var rep report
		if err := json.NewDecoder(r).Decode(&rep); err != nil {
			return fmt.Errorf("%s: not a grype JSON report: %w", path, err)
		}
		u.newest = newestImage(rep.Source.Target, ref)
		for _, m := range rep.Matches {
			u.carries[findingKey{m.Vulnerability.ID, m.Artifact.Name}] = true
			for _, related := range m.RelatedVulnerabilities {
				u.carries[findingKey{related.ID, m.Artifact.Name}] = true
			}
		}
		return nil
	})
	return pinned, u, err
}

// newestImage names the scanned image as name:tag@digest, from the scan's
// input (registry:name:tag) and the manifest digest it resolved; the
// pinned reference when the report names neither.
func newestImage(target json.RawMessage, pinned string) string {
	var t struct {
		UserInput      string `json:"userInput"`
		ManifestDigest string `json:"manifestDigest"`
	}
	if json.Unmarshal(target, &t) != nil || t.UserInput == "" {
		return pinned
	}
	name := strings.TrimPrefix(t.UserInput, "registry:")
	if t.ManifestDigest != "" && !strings.Contains(name, "@") {
		name += "@" + t.ManifestDigest
	}
	return name
}

// waits names the newest image of the finding's tag when it still carries
// the finding, under its ID or an alias.
func (j judge) waits(f finding) (string, bool) {
	u, ok := j.upstream[f.Image]
	if !ok {
		return "", false
	}
	for _, id := range append([]string{f.ID}, f.Aliases...) {
		if u.carries[findingKey{id, f.Package}] {
			return u.newest, true
		}
	}
	return "", false
}

// loadUpstream reads each -upstream argument; two for one pinned image
// are refused.
func loadUpstream(args []string) (map[image]upstream, error) {
	out := map[image]upstream{}
	for _, arg := range args {
		pinned, u, err := readUpstream(arg)
		if err != nil {
			return nil, err
		}
		if _, twice := out[pinned]; twice {
			return nil, fmt.Errorf("-upstream names %s@%s twice", pinned.repository, pinned.digest)
		}
		out[pinned] = u
	}
	return out, nil
}
