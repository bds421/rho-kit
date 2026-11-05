package main

import (
	"regexp"
	"strings"
)

// image is a container image by repository and manifest digest; the tag
// does not identify it.
type image struct {
	repository, digest string
}

var digestRef = regexp.MustCompile(`^([^@\s]+)@(sha256:[0-9a-f]{64})$`)

// parseImage reads name[:tag]@sha256:<hex>, with Docker Hub's implied
// registry and library/ namespace spelled out or not.
func parseImage(ref string) (image, bool) {
	m := digestRef.FindStringSubmatch(ref)
	if m == nil {
		return image{}, false
	}
	name := m[1]
	if slash := strings.LastIndex(name, "/"); strings.LastIndex(name, ":") > slash {
		name = name[:strings.LastIndex(name, ":")]
	}
	for _, prefix := range []string{"index.docker.io/", "registry-1.docker.io/", "docker.io/"} {
		name = strings.TrimPrefix(name, prefix)
	}
	name = strings.TrimPrefix(name, "library/")
	return image{repository: name, digest: m[2]}, true
}
