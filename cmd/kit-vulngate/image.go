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
	return image{repository: repository(m[1]), digest: m[2]}, true
}

// repository is name[:tag] without its tag, Docker Hub's registry and
// its library/ namespace.
func repository(name string) string {
	if slash := strings.LastIndex(name, "/"); strings.LastIndex(name, ":") > slash {
		name = name[:strings.LastIndex(name, ":")]
	}
	for _, prefix := range []string{"index.docker.io/", "registry-1.docker.io/", "docker.io/"} {
		name = strings.TrimPrefix(name, prefix)
	}
	return strings.TrimPrefix(name, "library/")
}
